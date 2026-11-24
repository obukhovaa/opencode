package flow

import (
	"context"
	"slices"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/contextfile"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// listingScopeFactory records the listing scope each NewAgent ctx carries.
type listingScopeFactory struct {
	*stubAgentFactory
	agent      agentpkg.Service
	atNewAgent map[string]agentregistry.ListingScope
}

func (f *listingScopeFactory) NewAgent(ctx context.Context, agentID string, schema map[string]any, stepID string, interactive bool, peers []bridge.PeerRef, stepCtx *contextfile.StepContext, vars contextfile.TemplateVars, override agentpkg.ModelOverride) (agentpkg.Service, error) {
	f.mu.Lock()
	f.atNewAgent[stepID] = tools.ListingScopeFrom(ctx)
	f.mu.Unlock()
	return f.agent, nil
}

func runListingScopeFlow(t *testing.T, id string, args map[string]any, steps []Step, responses []agentpkg.AgentEvent) map[string]agentregistry.ListingScope {
	t.Helper()
	setIncludeWorkspace(t, t.TempDir())
	config.Get().ListingScope = &config.ListingScopeConfig{
		Skills:    []string{".agents/skills/**", "${args.team}/skills/**"},
		Subagents: []string{".agents/types/**"},
	}
	f := Flow{ID: id, Name: id, Spec: FlowSpec{Steps: steps}}
	registerTestFlow(t, f)
	factory := &listingScopeFactory{
		stubAgentFactory: &stubAgentFactory{},
		agent:            &stubAgent{Broker: pubsub.NewBroker[agentpkg.AgentEvent](), responses: responses},
		atNewAgent:       map[string]agentregistry.ListingScope{},
	}
	svc := NewService(&stubSessions{}, nil, &stubQuerier{}, &stubPermissions{}, factory)
	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", f.ID, args, true)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	drainFlow(t, agentEvents, flowStates)
	return factory.atNewAgent
}

func TestRunStep_ListingScopeFromRunArgs(t *testing.T) {
	steps := []Step{{ID: "work", Prompt: "work"}}

	t.Run("resolved team scopes the step", func(t *testing.T) {
		got := runListingScopeFlow(t, "listing-scope-team", map[string]any{"team": "composer"}, steps, nil)["work"]
		if !slices.Equal(got.Skills, []string{".agents/skills/**", "composer/skills/**"}) {
			t.Errorf("skills = %q", got.Skills)
		}
		if !slices.Equal(got.Subagents, []string{".agents/types/**"}) {
			t.Errorf("subagents = %q", got.Subagents)
		}
	})

	t.Run("missing team leaves the skill listing unscoped", func(t *testing.T) {
		got := runListingScopeFlow(t, "listing-scope-no-team", map[string]any{}, steps, nil)["work"]
		if got.Skills != nil {
			t.Errorf("skills = %q, want unscoped", got.Skills)
		}
		if got.Subagents == nil {
			t.Error("the token-free subagents list applies on its own")
		}
	})
}

func TestRunStep_ListingScopeFollowsArgsSetByEarlierStep(t *testing.T) {
	steps := []Step{
		{ID: "resolve", Prompt: "resolve", Output: &StepOutput{Schema: map[string]any{"type": "object"}}, Rules: []Rule{{Then: "work"}}},
		{ID: "work", Prompt: "work"},
	}
	responses := []agentpkg.AgentEvent{{
		Type:         agentpkg.AgentEventTypeResponse,
		Message:      message.Message{Role: message.Assistant},
		StructOutput: &message.ToolResult{Name: "struct_output", Content: `{"team":"platform"}`},
	}}
	got := runListingScopeFlow(t, "listing-scope-later", map[string]any{}, steps, responses)
	if got["resolve"].Skills != nil {
		t.Errorf("resolve skills = %q, want unscoped", got["resolve"].Skills)
	}
	if !slices.Equal(got["work"].Skills, []string{".agents/skills/**", "platform/skills/**"}) {
		t.Errorf("work skills = %q", got["work"].Skills)
	}
}
