package flow

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
)

// Tests in this file cover the per-step `agent` / `model` / `reasoningEffort`
// substitution (GENAI-312): how the runtime resolves the three fields, how
// the resolved override reaches AgentFactory.NewAgent, and how a resolution
// failure routes into the step's fallback. The runStep tests mirror
// TestRunStep_ThreadsStepContextIntoNewAgent in step_context_test.go.

func TestResolveStepAgent(t *testing.T) {
	tests := []struct {
		name    string
		step    Step
		args    map[string]any
		want    string
		wantErr bool
	}{
		{"empty defaults to coder", Step{ID: "s"}, nil, "coder", false},
		{"literal passes through", Step{ID: "s", Agent: "piano-developer"}, nil, "piano-developer", false},
		{"args placeholder resolves", Step{ID: "s", Agent: "${args.impl_agent}"}, map[string]any{"impl_agent": "piano-developer"}, "piano-developer", false},
		{"step placeholder resolves", Step{ID: "s", Agent: "agent-${step.iteration}"}, nil, "agent-3", false},
		{"unresolved placeholder is an error", Step{ID: "s", Agent: "${args.impl_agent}"}, map[string]any{}, "", true},
		{"resolves to empty is an error", Step{ID: "s", Agent: "${args.impl_agent}"}, map[string]any{"impl_agent": "  "}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveStepAgent(tt.step, tt.args, map[string]any{"iteration": 3})
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveStepAgent() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("resolveStepAgent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveStepModelOverride(t *testing.T) {
	const (
		noXHigh = "bedrock.eu-claude-opus-4-6" // SupportsXHighThinking false
		xHigh   = "bedrock.eu-claude-opus-4-7" // SupportsXHighThinking true
	)
	if models.SupportedModels[noXHigh].SupportsXHighThinking || !models.SupportedModels[xHigh].SupportsXHighThinking {
		t.Fatal("test fixtures assume 4.6 opus lacks xhigh and 4.7 opus has it; update the ids")
	}

	tests := []struct {
		name    string
		step    Step
		args    map[string]any
		want    agentpkg.ModelOverride
		wantErr error
	}{
		{"no fields is zero", Step{ID: "s"}, nil, agentpkg.ModelOverride{}, nil},
		{"literal model and effort", Step{ID: "s", Model: xHigh, ReasoningEffort: "high"}, nil,
			agentpkg.ModelOverride{Model: xHigh, ReasoningEffort: "high"}, nil},
		{"templated model and effort", Step{ID: "s", Model: "${args.impl_model}", ReasoningEffort: "${args.impl_effort}"},
			map[string]any{"impl_model": xHigh, "impl_effort": "high"},
			agentpkg.ModelOverride{Model: xHigh, ReasoningEffort: "high"}, nil},
		{"missing args yield zero override, not an error", Step{ID: "s", Model: "${args.impl_model}", ReasoningEffort: "${args.impl_effort}"},
			map[string]any{}, agentpkg.ModelOverride{}, nil},
		{"empty arg yields zero override", Step{ID: "s", Model: "${args.impl_model}"},
			map[string]any{"impl_model": ""}, agentpkg.ModelOverride{}, nil},
		{"model missing but effort present keeps the effort", Step{ID: "s", Model: "${args.impl_model}", ReasoningEffort: "medium"},
			map[string]any{}, agentpkg.ModelOverride{ReasoningEffort: "medium"}, nil},
		{"unknown model is ErrInvalidModel", Step{ID: "s", Model: "${args.impl_model}"},
			map[string]any{"impl_model": "bedrock.eu-claude-nope"}, agentpkg.ModelOverride{}, ErrInvalidModel},
		{"effort is lower-cased", Step{ID: "s", ReasoningEffort: "HIGH"}, nil,
			agentpkg.ModelOverride{ReasoningEffort: "high"}, nil},
		{"unknown effort word is ErrInvalidReasoningEffort", Step{ID: "s", ReasoningEffort: "${args.e}"},
			map[string]any{"e": "extreme"}, agentpkg.ModelOverride{}, ErrInvalidReasoningEffort},
		{"XHIGH rejected on a model without the flag", Step{ID: "s", Model: noXHigh, ReasoningEffort: "${args.e}"},
			map[string]any{"e": "XHIGH"}, agentpkg.ModelOverride{}, ErrInvalidReasoningEffort},
		{"XHIGH accepted on a model with the flag", Step{ID: "s", Model: xHigh, ReasoningEffort: "${args.e}"},
			map[string]any{"e": "XHIGH"}, agentpkg.ModelOverride{Model: xHigh, ReasoningEffort: "xhigh"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveStepModelOverride(tt.step, tt.args, map[string]any{"iteration": 1})
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("resolveStepModelOverride() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveStepModelOverride() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveStepModelOverride() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// runOverrideFlow registers f, runs it once with args against a recording
// stub factory and returns the factory's NewAgent calls keyed by step ID
// (last call per step) plus the published flow states.
func runOverrideFlow(t *testing.T, f Flow, args map[string]any, agent *stubAgent) (*stubAgentFactory, []*FlowState) {
	t.Helper()
	registerTestFlow(t, f)
	factory := &stubAgentFactory{agent: agent}
	svc := NewService(&stubSessions{}, nil, &stubQuerier{}, &stubPermissions{}, factory)

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", f.ID, args, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	states := drainFlow(t, agentEvents, flowStates)
	return factory, states
}

func lastCallByStep(calls []stubNewAgentCall) map[string]stubNewAgentCall {
	byStep := make(map[string]stubNewAgentCall, len(calls))
	for _, c := range calls {
		byStep[c.stepID] = c
	}
	return byStep
}

// TestRunStep_ThreadsModelOverrideIntoNewAgent pins the runStep → NewAgent
// plumbing for the model override: a templated pair resolves from args, a
// step without the fields passes the zero override, and a step whose arg is
// absent still runs — on the agent's own model — instead of failing.
func TestRunStep_ThreadsModelOverrideIntoNewAgent(t *testing.T) {
	testFlow := Flow{
		ID:   "test-model-override",
		Name: "Test Model Override",
		Spec: FlowSpec{
			Steps: []Step{
				{
					ID:              "tiered",
					Prompt:          "implement",
					Model:           "${args.impl_model}",
					ReasoningEffort: "${args.impl_effort}",
					Rules:           []Rule{{Then: "plain"}},
				},
				{ID: "plain", Prompt: "more work", Rules: []Rule{{Then: "unrouted"}}},
				{ID: "unrouted", Prompt: "no gate ran before me", Model: "${args.absent_model}", ReasoningEffort: "${args.absent_effort}"},
			},
		},
	}
	factory, states := runOverrideFlow(t, testFlow, map[string]any{
		"impl_model":  "bedrock.eu-claude-sonnet-5",
		"impl_effort": "high",
	}, newStubAgent())

	byStep := lastCallByStep(factory.snapshotNewAgentCalls())

	tiered, ok := byStep["tiered"]
	if !ok {
		t.Fatalf("NewAgent was not called for step 'tiered'; calls: %+v", byStep)
	}
	want := agentpkg.ModelOverride{Model: "bedrock.eu-claude-sonnet-5", ReasoningEffort: "high"}
	if tiered.override != want {
		t.Errorf("tiered override = %+v, want %+v", tiered.override, want)
	}
	if tiered.agentID != "coder" {
		t.Errorf("tiered agent = %q, want the coder default", tiered.agentID)
	}

	plain, ok := byStep["plain"]
	if !ok {
		t.Fatalf("NewAgent was not called for step 'plain'")
	}
	if !plain.override.IsZero() {
		t.Errorf("a step without model fields must pass the zero override, got %+v", plain.override)
	}

	unrouted, ok := byStep["unrouted"]
	if !ok {
		t.Fatalf("a step whose model args are absent must still run; calls: %+v", byStep)
	}
	if !unrouted.override.IsZero() {
		t.Errorf("unresolved model args must yield the zero override, got %+v", unrouted.override)
	}
	if st := findLatestByStepID(states, "unrouted"); st == nil || st.Status != FlowStatusCompleted {
		t.Errorf("unrouted step state = %+v, want completed", st)
	}
}

// TestRunStep_SubstitutesAgentFromArgs pins the asymmetry with the model
// override: `agent: ${args.x}` resolves from args like any placeholder, but
// an arg that is absent FAILS the step and routes into its fallback rather
// than silently running some default agent.
func TestRunStep_SubstitutesAgentFromArgs(t *testing.T) {
	testFlow := Flow{
		ID:   "test-agent-from-args",
		Name: "Test Agent From Args",
		Spec: FlowSpec{
			Steps: []Step{
				{ID: "routed", Prompt: "work", Agent: "${args.impl_agent}", Rules: []Rule{{Then: "unrouted"}}},
				{ID: "unrouted", Prompt: "work", Agent: "${args.absent_agent}", Fallback: &Fallback{To: "salvage"}},
				{ID: "salvage", Prompt: "salvage"},
			},
		},
	}
	factory, states := runOverrideFlow(t, testFlow, map[string]any{"impl_agent": "piano-developer"}, newStubAgent())

	byStep := lastCallByStep(factory.snapshotNewAgentCalls())
	if routed, ok := byStep["routed"]; !ok || routed.agentID != "piano-developer" {
		t.Errorf("routed step agent = %+v, want piano-developer", routed)
	}
	if _, ok := byStep["unrouted"]; ok {
		t.Errorf("a step whose agent placeholder is unresolved must not reach NewAgent")
	}
	if st := findLatestByStepID(states, "unrouted"); st == nil || st.Status != FlowStatusFailed || !strings.Contains(st.Output, "unresolved variables") {
		t.Errorf("unrouted step state = %+v, want failed with an unresolved-variables error", st)
	}
	if _, ok := byStep["salvage"]; !ok {
		t.Errorf("fallback step was not scheduled after the agent resolution failure; calls: %+v", byStep)
	}
}

// TestRunStep_InvalidModelRoutesToFallback: a model that resolved to
// something the catalog does not know is a routing bug, so the step fails
// with ErrInvalidModel and `fallback.to` fires — the same path a NewAgent
// construction error takes.
func TestRunStep_InvalidModelRoutesToFallback(t *testing.T) {
	testFlow := Flow{
		ID:   "test-invalid-model",
		Name: "Test Invalid Model",
		Spec: FlowSpec{
			Steps: []Step{
				{ID: "implement", Prompt: "work", Model: "${args.impl_model}", Fallback: &Fallback{To: "salvage"}},
				{ID: "salvage", Prompt: "salvage"},
			},
		},
	}
	factory, states := runOverrideFlow(t, testFlow, map[string]any{"impl_model": "bedrock.eu-claude-nope"}, newStubAgent())

	byStep := lastCallByStep(factory.snapshotNewAgentCalls())
	if _, ok := byStep["implement"]; ok {
		t.Errorf("a step with an unknown model must fail before NewAgent")
	}
	st := findLatestByStepID(states, "implement")
	if st == nil || st.Status != FlowStatusFailed {
		t.Fatalf("implement state = %+v, want failed", st)
	}
	if !strings.Contains(st.Output, ErrInvalidModel.Error()) {
		t.Errorf("implement failure output = %q, want it to carry %q", st.Output, ErrInvalidModel.Error())
	}
	if _, ok := byStep["salvage"]; !ok {
		t.Errorf("fallback step was not scheduled; calls: %+v", byStep)
	}
	if st := findLatestByStepID(states, "salvage"); st == nil || st.Status != FlowStatusCompleted {
		t.Errorf("salvage state = %+v, want completed", st)
	}
}

// TestRunStep_FallbackReentersAfterCycle proves a fallback target that
// already ran in this invocation is admitted again. Shape: `implement`
// fails → fallback `escalated` runs and routes back to `implement` with a
// cycle rule → `implement` fails again → fallback `escalated` a SECOND
// time. Before the fallback stepWork carried cycle: true, the diamond
// convergence guard in Run dropped that second arrival and the run ended
// `completed` with nothing salvaged.
func TestRunStep_FallbackReentersAfterCycle(t *testing.T) {
	testFlow := Flow{
		ID:   "test-fallback-reenter",
		Name: "Test Fallback Re-entry",
		Spec: FlowSpec{
			Steps: []Step{
				// A literal unknown model fails resolution deterministically
				// on every arrival (registerTestFlow bypasses validateFlow).
				{ID: "implement", Prompt: "work", Model: "bedrock.eu-claude-nope", Fallback: &Fallback{To: "escalated"}},
				{
					ID:     "escalated",
					Prompt: "escalate",
					Output: &StepOutput{Schema: map[string]any{"type": "object"}},
					Rules:  []Rule{{If: "${args.again} == true", Then: "implement", Cycle: true}},
				},
			},
		},
	}
	// escalated is the only step that reaches the agent: first run asks
	// for another implement attempt, second run stops the cycle.
	agent := newStubAgent()
	agent.responses = []agentpkg.AgentEvent{
		loopRespond(`{"again": true}`),
		loopRespond(`{"again": false}`),
	}
	factory, states := runOverrideFlow(t, testFlow, map[string]any{}, agent)

	escalatedRuns := 0
	for _, c := range factory.snapshotNewAgentCalls() {
		if c.stepID == "escalated" {
			escalatedRuns++
		}
	}
	if escalatedRuns != 2 {
		t.Fatalf("escalated ran %d times, want 2 (second fallback arrival must not be dropped as diamond convergence)", escalatedRuns)
	}
	if got := agent.callCount(); got != 2 {
		t.Errorf("agent calls = %d, want 2", got)
	}
	if n := countCompletedByStepID(states, "escalated"); n != 2 {
		t.Errorf("escalated completed %d times, want 2", n)
	}
	if st := findLatestByStepID(states, "escalated"); st == nil || st.Iteration != 2 {
		t.Errorf("second escalated arrival iteration = %+v, want 2 (cycle bump from the prior row)", st)
	}
	implementFailures := 0
	for _, st := range states {
		if st.StepID == "implement" && st.Status == FlowStatusFailed {
			implementFailures++
		}
	}
	if implementFailures != 2 {
		t.Errorf("implement failed %d times, want 2", implementFailures)
	}
}
