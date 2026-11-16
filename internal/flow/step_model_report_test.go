package flow

import (
	"context"
	"errors"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/contextfile"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
)

// modelAgent is a stubAgent that reports the model it was built on.
type modelAgent struct {
	*stubAgent
	model models.Model
}

func (a modelAgent) Model() models.Model { return a.model }

// modelAgentFactory builds per-step stub agents that report the override's
// model, or defaultModel without one, like the real factory does. agents
// lets a test script one step's agent.
type modelAgentFactory struct {
	*stubAgentFactory
	defaultModel models.ModelID
	agents       map[string]*stubAgent
}

func newModelAgentFactory(defaultModel models.ModelID) *modelAgentFactory {
	return &modelAgentFactory{
		stubAgentFactory: &stubAgentFactory{},
		defaultModel:     defaultModel,
		agents:           map[string]*stubAgent{},
	}
}

func (f *modelAgentFactory) NewAgent(ctx context.Context, agentID string, schema map[string]any, stepID string, interactive bool, peers []bridge.PeerRef, stepCtx *contextfile.StepContext, vars contextfile.TemplateVars, override agentpkg.ModelOverride) (agentpkg.Service, error) {
	if _, err := f.stubAgentFactory.NewAgent(ctx, agentID, schema, stepID, interactive, peers, stepCtx, vars, override); err != nil {
		return nil, err
	}
	agent, ok := f.agents[stepID]
	if !ok {
		agent = newStubAgent()
	}
	id := f.defaultModel
	if override.Model != "" {
		id = override.Model
	}
	return modelAgent{stubAgent: agent, model: models.Model{ID: id}}, nil
}

// TestRunStep_StatesCarryResolvedModel: every state a step publishes after
// its agent is built names the agent's model — the step's own override, or
// the agent's configured model without one — including failures; a step that
// fails before NewAgent has no model to report.
func TestRunStep_StatesCarryResolvedModel(t *testing.T) {
	const fallbackModel = models.BedrockSonnet55
	f := Flow{
		ID:   "test-step-model-report",
		Name: "Test Step Model Report",
		Spec: FlowSpec{
			Steps: []Step{
				{ID: "picked", Prompt: "work", Model: "${args.impl_model}", Rules: []Rule{{Then: "plain"}, {Then: "broken"}, {Then: "early"}}},
				{ID: "plain", Prompt: "work"},
				{ID: "broken", Prompt: "work"},
				{ID: "early", Prompt: "work", Model: "bedrock.eu-claude-nope"},
			},
		},
	}
	registerTestFlow(t, f)
	factory := newModelAgentFactory(fallbackModel)
	broken := newStubAgent()
	broken.responses = []agentpkg.AgentEvent{{Type: agentpkg.AgentEventTypeError, Error: errors.New("provider exploded")}}
	factory.agents["broken"] = broken
	svc := NewService(&stubSessions{}, nil, &stubQuerier{}, &stubPermissions{}, factory)

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", f.ID, map[string]any{"impl_model": string(models.BedrockUSKimiK31)}, true)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	states := drainFlow(t, agentEvents, flowStates)

	want := map[string]string{
		"picked": string(models.BedrockUSKimiK31),
		"plain":  string(fallbackModel),
		"broken": string(fallbackModel),
		"early":  "",
	}
	seen := map[string]int{}
	for _, st := range states {
		wantModel, ok := want[st.StepID]
		if !ok {
			continue
		}
		seen[st.StepID]++
		if st.Model != wantModel {
			t.Errorf("step %s %s state model = %q, want %q", st.StepID, st.Status, st.Model, wantModel)
		}
	}
	if seen["picked"] < 2 || seen["plain"] < 2 || seen["broken"] < 2 || seen["early"] < 1 {
		t.Fatalf("missing states: %v", seen)
	}
	if st := findLatestByStepID(states, "broken"); st == nil || st.Status != FlowStatusFailed {
		t.Fatalf("broken step = %+v, want failed", st)
	}
	if st := findLatestByStepID(states, "early"); st == nil || st.Status != FlowStatusFailed {
		t.Fatalf("early step = %+v, want failed", st)
	}
}
