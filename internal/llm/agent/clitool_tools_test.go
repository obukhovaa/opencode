package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/clitool"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/permission"
)

// cliToolRegistry is a Registry stub whose gating answers come from the
// AgentInfo it holds, exactly as the real registry derives them.
type cliToolRegistry struct {
	agentregistry.Registry
	info AgentInfoLike
}

// AgentInfoLike is the subset of AgentInfo the stub needs.
type AgentInfoLike = agentregistry.AgentInfo

func (r cliToolRegistry) Get(string) (agentregistry.AgentInfo, bool) { return r.info, true }

// Builtins stay out (their constructors need a loaded config); only the CLI
// tools under test and toolsearch (the deferral ladder) are answered.
func (r cliToolRegistry) IsToolEnabled(_, name string) bool {
	switch name {
	case "snow", "helper", tools.ToolSearchToolName:
		return r.info.ToolEnabled(name)
	}
	return false
}

func (r cliToolRegistry) IsToolExplicitlyEnabled(_, name string) bool {
	switch name {
	case "snow", "helper":
		return r.info.ToolExplicitlyEnabled(name)
	}
	return false
}
func (r cliToolRegistry) HasTools(string) bool              { return true }
func (r cliToolRegistry) GlobalPermissions() map[string]any { return nil }

func parseManifest(t *testing.T, name, body string) *clitool.Manifest {
	t.Helper()
	wd := t.TempDir()
	m, err := clitool.Parse([]byte(body), filepath.Join(wd, name+".yaml"), wd)
	if err != nil {
		t.Fatalf("manifest %s: %v", name, err)
	}
	return m
}

// noMCP satisfies MCPRegistry with an empty, closed tool stream so the MCP
// goroutine in NewToolSet has nothing to load (and nothing to panic on).
type noMCP struct{ MCPRegistry }

func (noMCP) LoadTools(*MCPRegistryFiler) <-chan tools.BaseTool {
	ch := make(chan tools.BaseTool)
	close(ch)
	return ch
}

func cliToolNamesOf(t *testing.T, info *agentregistry.AgentInfo) map[string]tools.BaseTool {
	t.Helper()
	reg := cliToolRegistry{info: *info}
	out := map[string]tools.BaseTool{}
	for tool := range NewToolSet(info, reg, nil, nil, nil, nil, nil, noMCP{}, nil, "") {
		out[tool.Info().Name] = tool
	}
	return out
}

func TestCLITools_GrantModes(t *testing.T) {
	explicit := parseManifest(t, "snow", "name: snow\ndescription: d\ncommand: /bin/echo\n")
	implicit := parseManifest(t, "helper", "name: helper\ndescription: d\ncommand: /bin/echo\ngrant: implicit\n")
	restore := clitool.OverrideForTest([]*clitool.Manifest{explicit, implicit})
	defer restore()

	cases := []struct {
		name       string
		tools      map[string]bool
		allowTools []string
		wantSnow   bool
		wantHelper bool
	}{
		{"nothing declared", nil, nil, false, true},
		{"bare star does not grant explicit", map[string]bool{"*": true}, nil, false, true},
		{"named grant", map[string]bool{"snow": true}, nil, true, true},
		{"specific wildcard grants", map[string]bool{"sn*": true}, nil, true, true},
		{"implicit can be denied", map[string]bool{"helper": false}, nil, false, false},
		{"allowlist names it", nil, []string{"snow"}, true, false},
		{"allowlist star alone", nil, []string{"*"}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := &agentregistry.AgentInfo{ID: "a", Mode: config.AgentModeSubagent, Tools: tc.tools, AllowTools: tc.allowTools}
			got := cliToolNamesOf(t, info)
			_, snow := got["snow"]
			_, helper := got["helper"]
			if snow != tc.wantSnow || helper != tc.wantHelper {
				t.Errorf("snow=%v helper=%v, want %v/%v", snow, helper, tc.wantSnow, tc.wantHelper)
			}
		})
	}
}

func TestCLITools_DeferredAndExternal(t *testing.T) {
	m := parseManifest(t, "snow", "name: snow\ndescription: d\ncommand: /bin/echo\n")
	defer clitool.OverrideForTest([]*clitool.Manifest{m})()

	info := &agentregistry.AgentInfo{ID: "a", Mode: config.AgentModeSubagent,
		Tools: map[string]bool{"snow": true}, DeferredTools: map[string]bool{"snow": true}}
	got := cliToolNamesOf(t, info)
	tool, ok := got["snow"]
	if !ok {
		t.Fatal("snow not in toolset")
	}
	if _, deferred := tool.(*tools.DeferredWrapper); !deferred {
		t.Errorf("expected a DeferredWrapper, got %T", tool)
	}
	if tool.IsBaseline() {
		t.Error("CLI tools must sort with the external tail")
	}
	if _, ok := got[tools.ToolSearchToolName]; !ok {
		t.Error("toolsearch should be injected when deferral is in effect")
	}
}

func TestCLITools_NoManifestsNoChange(t *testing.T) {
	defer clitool.OverrideForTest(nil)()
	info := &agentregistry.AgentInfo{ID: "a", Mode: config.AgentModeSubagent, Tools: map[string]bool{"*": true}}
	for name := range cliToolNamesOf(t, info) {
		if name == "snow" || name == "helper" {
			t.Errorf("unexpected CLI tool %q", name)
		}
	}
}

// TestCLITools_ReservedNamesCoverBuiltins keeps clitool.ReservedNames in step
// with every tool name NewToolSet can register (it cannot import this
// package: the tools package wraps manifests, so the dependency points the
// other way).
func TestCLITools_ReservedNamesCoverBuiltins(t *testing.T) {
	var names []string
	names = append(names, viewerToolNames...)
	names = append(names, editorToolNames...)
	names = append(names, managerToolNames...)
	names = append(names, tools.WebSearchToolName, tools.LSPToolName, tools.StructOutputToolName, tools.ToolSearchToolName)
	for _, n := range names {
		if !clitool.ReservedNames[n] {
			t.Errorf("builtin tool name %q is missing from clitool.ReservedNames", n)
		}
	}
}

// --- wrapper behaviour -----------------------------------------------------

type permRegistry struct {
	agentregistry.Registry
	perm map[string]any
}

func (r permRegistry) Get(string) (agentregistry.AgentInfo, bool) {
	return agentregistry.AgentInfo{ID: "a", Tools: map[string]bool{"*": true}, Permission: r.perm}, true
}
func (r permRegistry) GlobalPermissions() map[string]any { return nil }

func runCtx() context.Context {
	ctx := context.WithValue(context.Background(), tools.SessionIDContextKey, "S1")
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, "M1")
	return context.WithValue(ctx, tools.AgentIDContextKey, config.AgentName("a"))
}

func TestCLIToolRun_AllowedCall(t *testing.T) {
	m := parseManifest(t, "say", "name: say\ndescription: d\ncommand: /bin/echo\nprefixArgs: [\"-n\"]\npermission: allow\n")
	tool := tools.NewCLITool(m, nil, permRegistry{})
	resp, err := tool.Run(runCtx(), tools.ToolCall{Input: `{"args":["hello","a && b"]}`})
	if err != nil {
		t.Fatal(err)
	}
	if resp.IsError || !strings.HasPrefix(resp.Content, "hello a && b\nexit status 0") {
		t.Errorf("resp = %+v", resp)
	}
	if !strings.Contains(resp.Metadata, `"exit_code":0`) || !strings.Contains(resp.Metadata, `"args":["-n","hello","a \u0026\u0026 b"]`) {
		t.Errorf("metadata = %s", resp.Metadata)
	}
}

func TestCLIToolRun_PolicyRejectionIsModelVisible(t *testing.T) {
	m := parseManifest(t, "say", "name: say\ndescription: d\ncommand: /bin/echo\npermission: allow\nargs:\n  deny: [\"-x\"]\n")
	tool := tools.NewCLITool(m, nil, permRegistry{})
	resp, err := tool.Run(runCtx(), tools.ToolCall{Input: `{"args":["ok","-x"]}`})
	if err != nil {
		t.Fatalf("a policy rejection must not surface as a Go error (that ends the run): %v", err)
	}
	if !resp.IsError || !strings.Contains(resp.Content, `deny pattern "-x"`) {
		t.Errorf("resp = %+v", resp)
	}
}

func TestCLIToolRun_PermissionLayers(t *testing.T) {
	m := parseManifest(t, "say", "name: say\ndescription: d\ncommand: /bin/echo\npermission:\n  \"*\": ask\n  \"sql *\": allow\n")
	// Agent rule for the tool beats the manifest default.
	tool := tools.NewCLITool(m, nil, permRegistry{perm: map[string]any{"say": "deny"}})
	_, err := tool.Run(runCtx(), tools.ToolCall{Input: `{"args":["sql","1"]}`})
	if !errors.Is(err, permission.ErrorPermissionDenied) {
		t.Errorf("agent deny should win, got %v", err)
	}
	// Manifest default beats the agent's blanket "*".
	tool = tools.NewCLITool(m, nil, permRegistry{perm: map[string]any{"*": "deny"}})
	resp, err := tool.Run(runCtx(), tools.ToolCall{Input: `{"args":["sql","1"]}`})
	if err != nil || resp.IsError {
		t.Errorf("manifest default allow should beat agent wildcard: %v %+v", err, resp)
	}
	// Non-zero exit is an error response with the exit status.
	m2 := parseManifest(t, "f", "name: f\ndescription: d\ncommand: /usr/bin/false\npermission: allow\n")
	resp, err = tools.NewCLITool(m2, nil, permRegistry{}).Run(runCtx(), tools.ToolCall{Input: `{"args":[]}`})
	if err != nil || !resp.IsError || !strings.Contains(resp.Content, "exit status 1") {
		t.Errorf("false: %v %+v", err, resp)
	}
}
