package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

type compositionTestRegistry struct {
	agentregistry.Registry
	info agentregistry.AgentInfo
	deny string
}

func (r *compositionTestRegistry) Get(string) (agentregistry.AgentInfo, bool) { return r.info, true }
func (r *compositionTestRegistry) IsToolEnabled(_, name string) bool {
	return name != r.deny && (name == tools.MCPComposeToolName || strings.HasPrefix(name, "srv_")) && r.info.ToolEnabled(name)
}
func (r *compositionTestRegistry) IsToolExplicitlyEnabled(string, string) bool { return false }
func (r *compositionTestRegistry) HasTools(string) bool                        { return true }
func (r *compositionTestRegistry) GlobalPermissions() map[string]any           { return nil }

func compositionTestConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.ProfileEnv, "")
	if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(`{"providers":{"anthropic":{"apiKey":"test"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	loadConfigIn(t, dir)
	cfg := config.Get()
	cfg.MCPComposition = &config.MCPCompositionConfig{ReadTools: []string{"srv_read"}}
	cfg.MCPServers = map[string]config.MCPServer{"srv": {Type: config.MCPStdio, Command: "unused"}}
	return cfg
}

func TestMCPCompositionRegistrationRequiresLiteralGrant(t *testing.T) {
	cfg := compositionTestConfig(t)
	var absent []byte
	for _, tc := range []struct {
		name string
		info agentregistry.AgentInfo
		off  bool
		want bool
	}{
		{"implicit", agentregistry.AgentInfo{}, false, false},
		{"bare wildcard", agentregistry.AgentInfo{AllowTools: []string{"*"}}, false, false},
		{"partial wildcard", agentregistry.AgentInfo{AllowTools: []string{"mcp_*"}}, false, false},
		{"legacy wildcard", agentregistry.AgentInfo{Tools: map[string]bool{"mcp_*": true}}, false, false},
		{"no endpoints", agentregistry.AgentInfo{AllowTools: []string{tools.MCPComposeToolName}}, true, false},
		{"literal", agentregistry.AgentInfo{AllowTools: []string{tools.MCPComposeToolName}}, false, true},
		{"legacy literal", agentregistry.AgentInfo{Tools: map[string]bool{tools.MCPComposeToolName: true}}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg.MCPComposition = &config.MCPCompositionConfig{ReadTools: []string{"srv_read"}}
			if tc.off {
				cfg.MCPComposition = nil
			}
			tc.info.ID = "test"
			reg := &compositionTestRegistry{info: tc.info}
			var found bool
			infos := []tools.ToolInfo{}
			for tool := range NewToolSet(&tc.info, reg, nil, nil, nil, nil, nil, noMCP{}, nil, "") {
				info := tool.Info()
				infos = append(infos, info)
				if info.Name == tools.MCPComposeToolName {
					found = true
					properties := info.Parameters["steps"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
					candidate := properties["tool"].(map[string]any)
					if names, ok := candidate["enum"].([]string); !ok || len(names) != 1 || names[0] != "srv_read" {
						t.Fatalf("configured candidates missing from model schema: %v", candidate)
					}
				}
			}
			if found != tc.want {
				t.Fatalf("registered=%v want=%v", found, tc.want)
			}
			if !tc.want {
				raw, err := json.Marshal(infos)
				if err != nil {
					t.Fatal(err)
				}
				if absent == nil {
					absent = raw
				} else if string(absent) != string(raw) {
					t.Fatal("disabled composition changed tool declarations")
				}
			}
		})
	}
}

func TestMCPCompositionNameCollisionFailsClosed(t *testing.T) {
	foreign := &mcpTool{mcpName: "mcp", tool: mcp.NewTool("compose")}
	builtin := tools.NewMCPComposeTool()
	other := newMock("srv_read", false)
	for _, tc := range []struct {
		name string
		all  []tools.BaseTool
		want int
	}{
		{"disabled feature preserves MCP", []tools.BaseTool{foreign, other}, 2},
		{"builtin only", []tools.BaseTool{builtin, other}, 2},
		{"collision", []tools.BaseTool{builtin, foreign, other}, 1},
		{"deferred collision", []tools.BaseTool{builtin, tools.WrapDeferred(foreign, &atomic.Int64{}), other}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := filterCompositionNameCollision(tc.all)
			if len(got) != tc.want || got[len(got)-1] != other {
				t.Fatalf("unexpected remaining tools: %v", toolNames(got))
			}
		})
	}
}

func TestMCPCompositionEligibilityRefreshAndScope(t *testing.T) {
	cfg := compositionTestConfig(t)
	reg := &compositionTestRegistry{info: agentregistry.AgentInfo{ID: "test", AllowTools: []string{tools.MCPComposeToolName, "srv_read"}}}
	leaf := &mcpTool{mcpName: "srv", tool: mcp.NewTool("read", mcp.WithReadOnlyHintAnnotation(true)), reg: reg}
	a := &agent{agentID: "test", reg: reg, tools: []tools.BaseTool{leaf}}
	a.toolsOnce.Do(func() {})
	ctx := context.WithValue(context.Background(), tools.AgentIDContextKey, config.AgentName("test"))
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, "session")
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, "message")
	if got, err := a.compositionChildTool(ctx, "srv_read"); err != nil || got != leaf {
		t.Fatalf("reviewed read not admitted: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func()
	}{
		{"removed endpoint", func() { cfg.MCPComposition.ReadTools = nil }},
		{"revoked grant", func() { reg.info.AllowTools = []string{"srv_read"} }},
		{"runtime tool disable", func() { reg.deny = "srv_read" }},
		{"disabled server", func() { cfg.MCPServers["srv"] = config.MCPServer{Disabled: true} }},
		{"mutating hint", func() { leaf.tool.Annotations.ReadOnlyHint = mcp.ToBoolPtr(false) }},
		{"non-MCP shadow", func() { a.tools = []tools.BaseTool{newMock("srv_read", false)} }},
		{"duplicate name", func() { a.tools = []tools.BaseTool{leaf, leaf} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg.MCPComposition.ReadTools = []string{"srv_read"}
			cfg.MCPServers["srv"] = config.MCPServer{}
			reg.info.AllowTools = []string{tools.MCPComposeToolName, "srv_read"}
			reg.deny = ""
			leaf.tool.Annotations.ReadOnlyHint = nil
			a.tools = []tools.BaseTool{leaf}
			tc.change()
			if _, err := a.compositionChildTool(ctx, "srv_read"); err == nil {
				t.Fatal("ineligible child admitted")
			}
		})
	}
	cfg.MCPServers["srv"] = config.MCPServer{}
	reg.info.AllowTools = []string{tools.MCPComposeToolName, "srv_read"}
	reg.deny = ""
	leaf.tool.Annotations.ReadOnlyHint = nil
	w := tools.WrapDeferred(leaf, &atomic.Int64{})
	a.tools = []tools.BaseTool{w}
	if got, err := a.compositionChildTool(ctx, "srv_read"); err != nil || got != w {
		t.Fatalf("configured deferred read requires unnecessary discovery: %v", err)
	}
	if _, active := w.ActivatedAt("session"); active || len(tools.SerializableFor("session", a.tools)) != 0 {
		t.Fatal("internal composition changed discovery state or fallback schemas")
	}
	w.Activate("session")
	if got, err := a.compositionChildTool(ctx, "srv_read"); err != nil || got != w {
		t.Fatalf("discovered wrapper not preserved: %v", err)
	}
	for _, other := range []context.Context{context.Background(), context.WithValue(ctx, tools.SessionIDContextKey, ""), context.WithValue(ctx, tools.AgentIDContextKey, config.AgentName("other")), tools.WithDeniedTools(ctx, []string{"srv_*"}), tools.WithDeniedTools(ctx, []string{tools.MCPComposeToolName})} {
		if _, err := a.compositionChildTool(other, "srv_read"); err == nil {
			t.Fatal("foreign or missing context admitted")
		}
	}
}
