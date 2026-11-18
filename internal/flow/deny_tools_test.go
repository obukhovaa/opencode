package flow

import (
	"context"
	"slices"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/contextfile"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

// deniedToolsAgent records the denyTools each RunWith ctx carries.
type deniedToolsAgent struct {
	*stubAgent
	seen *[][]string
}

func (a deniedToolsAgent) RunWith(ctx context.Context, sessionID, prompt string, maxTurns int, opts agentpkg.RunOptions, atts ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	a.mu.Lock()
	*a.seen = append(*a.seen, tools.DeniedTools(ctx))
	a.mu.Unlock()
	return a.stubAgent.RunWith(ctx, sessionID, prompt, maxTurns, opts, atts...)
}

// deniedToolsFactory records the denyTools each NewAgent ctx carries and
// hands out a recording agent.
type deniedToolsFactory struct {
	*stubAgentFactory
	agent      deniedToolsAgent
	atNewAgent map[string][]string
}

func (f *deniedToolsFactory) NewAgent(ctx context.Context, agentID string, schema map[string]any, stepID string, interactive bool, peers []bridge.PeerRef, stepCtx *contextfile.StepContext, vars contextfile.TemplateVars, override agentpkg.ModelOverride) (agentpkg.Service, error) {
	f.mu.Lock()
	f.atNewAgent[stepID] = tools.DeniedTools(ctx)
	f.mu.Unlock()
	return f.agent, nil
}

// TestRunStep_DenyToolsRidesTheStepContext: a step's denyTools reach both
// the agent factory (which narrows the tool set) and the run ctx (dispatch
// guard, subagents); a step without the key carries none, so the list does
// not leak to the next step.
func TestRunStep_DenyToolsRidesTheStepContext(t *testing.T) {
	f := Flow{
		ID:   "test-deny-tools",
		Name: "Test Deny Tools",
		Spec: FlowSpec{Steps: []Step{
			{ID: "shadow", Prompt: "review", DenyTools: []string{"gitlab_create_*", "router_send"}, Rules: []Rule{{Then: "post"}}},
			{ID: "post", Prompt: "post"},
		}},
	}
	registerTestFlow(t, f)
	var seen [][]string
	factory := &deniedToolsFactory{
		stubAgentFactory: &stubAgentFactory{},
		agent:            deniedToolsAgent{stubAgent: newStubAgent(), seen: &seen},
		atNewAgent:       map[string][]string{},
	}
	svc := NewService(&stubSessions{}, nil, &stubQuerier{}, &stubPermissions{}, factory)
	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", f.ID, nil, true)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	drainFlow(t, agentEvents, flowStates)

	want := []string{"gitlab_create_*", "router_send"}
	if got := factory.atNewAgent["shadow"]; !slices.Equal(got, want) {
		t.Errorf("NewAgent ctx denyTools = %q, want %q", got, want)
	}
	if got := factory.atNewAgent["post"]; got != nil {
		t.Errorf("the next step inherited denyTools %q", got)
	}
	if len(seen) != 2 || !slices.Equal(seen[0], want) || seen[1] != nil {
		t.Errorf("RunWith ctx denyTools = %q, want [%q, nil]", seen, want)
	}
}
