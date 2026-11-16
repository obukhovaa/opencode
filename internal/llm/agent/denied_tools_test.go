package agent

import (
	"context"
	"strings"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/clitool"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// TestDeniedToolsRegistry_NarrowsToolSet builds a real tool set through the
// wrapper: a denied tool is left out even though the agent grants it, the
// rest is untouched.
func TestDeniedToolsRegistry_NarrowsToolSet(t *testing.T) {
	snow := parseManifest(t, "snow", "name: snow\ndescription: d\ncommand: /bin/echo\n")
	helper := parseManifest(t, "helper", "name: helper\ndescription: d\ncommand: /bin/echo\ngrant: implicit\n")
	restore := clitool.OverrideForTest([]*clitool.Manifest{snow, helper})
	defer restore()

	info := &agentregistry.AgentInfo{ID: "a", Tools: map[string]bool{"snow": true}}
	reg := deniedToolsRegistry{Registry: cliToolRegistry{info: *info}, deny: []string{"SN*"}}
	got := map[string]bool{}
	for tool := range NewToolSet(info, reg, nil, nil, nil, nil, nil, noMCP{}, nil, "") {
		got[tool.Info().Name] = true
	}
	if got["snow"] {
		t.Error("denied tool snow is in the tool set")
	}
	if !got["helper"] {
		t.Errorf("helper was dropped too; tool set %v", got)
	}
}

// allEnabledRegistry answers yes to every gating question.
type allEnabledRegistry struct{ agentregistry.Registry }

func (allEnabledRegistry) IsToolEnabled(string, string) bool           { return true }
func (allEnabledRegistry) IsToolExplicitlyEnabled(string, string) bool { return true }

func TestDeniedToolsRegistry_Gating(t *testing.T) {
	reg := deniedToolsRegistry{Registry: allEnabledRegistry{}, deny: []string{"gitlab_create_*", "bash", "*"}}
	for _, name := range []string{"gitlab_create_note", "GitLab_Create_Note", "bash", "read"} {
		if reg.IsToolEnabled("a", name) || reg.IsToolExplicitlyEnabled("a", name) {
			t.Errorf("%s is enabled under a catch-all deny", name)
		}
	}
	if !reg.IsToolEnabled("a", tools.StructOutputToolName) || !reg.IsToolExplicitlyEnabled("a", tools.StructOutputToolName) {
		t.Error("struct_output must never be denied")
	}
	narrow := deniedToolsRegistry{Registry: allEnabledRegistry{}, deny: []string{"gitlab_create_*"}}
	if !narrow.IsToolEnabled("a", "gitlab_list_notes") {
		t.Error("a tool outside the globs was denied")
	}
}

// TestDispatchRefusesDeniedTool: a call to a denied tool that reaches the
// agent loop gets an error result and never runs; other calls in the same
// turn run as usual.
func TestDispatchRefusesDeniedTool(t *testing.T) {
	denied := &repeatResultTool{name: "gitlab_create_note", result: func(int) (tools.ToolResponse, error) { return tools.NewTextResponse("posted"), nil }}
	allowed := &repeatResultTool{name: "read", result: func(int) (tools.ToolResponse, error) { return tools.NewTextResponse("file"), nil }}
	p := &scriptedProvider{respond: func(int, bool) *provider.ProviderResponse {
		return toolTurn(call(denied.name, 1), call(allowed.name, 1))
	}}
	toolSet := []tools.BaseTool{denied, allowed}
	a := newLoopAgentWithTools(t, p, toolSet)
	ctx := tools.WithDeniedTools(context.Background(), []string{"gitlab_create_*"})

	_, results, err := a.streamAndHandleEvents(ctx, t.Name(), nil, toolSet, newCallTracker())
	if err != nil {
		t.Fatal(err)
	}
	if denied.calls.Load() != 0 {
		t.Fatal("the denied tool ran")
	}
	if allowed.calls.Load() != 1 {
		t.Fatal("the allowed tool did not run")
	}
	for _, r := range results.ToolResults() {
		if r.Name == denied.name && (!r.IsError || !strings.Contains(r.Content, "denyTools")) {
			t.Errorf("denied call result = %+v", r)
		}
		if r.Name == allowed.name && r.IsError {
			t.Errorf("allowed call result = %+v", r)
		}
	}
}
