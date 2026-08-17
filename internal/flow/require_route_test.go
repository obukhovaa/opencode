package flow

import (
	"context"
	"testing"

	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// A step whose rules all evaluate false schedules nothing, so the run ends there.
// Historically that was silent: the step persisted `completed` and the run was
// announced as a success. `require_route: true` turns it into a step error so the
// author's `fallback.to` carries an explanation instead.
//
// The absent-key predicate is the realistic trigger and is used throughout: a
// missing args key makes EVERY atom referencing it false, including `!=`, because
// resolveArgsPath returns ok=false before any operator runs.

// runDeadEndFlow builds `start` (which routes onward unconditionally) followed by
// `gate`, whose single rule can never match. gateFallback and requireRoute shape
// the case under test.
func runDeadEndFlow(t *testing.T, flowID string, requireRoute bool, gateFallback *Fallback) (*stubAgent, []*FlowState) {
	t.Helper()

	steps := []Step{
		{
			ID:     "start",
			Prompt: "begin",
			Rules:  []Rule{{Then: "gate"}},
		},
		{
			ID:           "gate",
			Prompt:       "produce a routing signal",
			Output:       &StepOutput{Schema: map[string]any{"type": "object"}},
			RequireRoute: requireRoute,
			// `decision` is never present in args, so this rule is always false.
			Rules:    []Rule{{If: "${args.decision} == go", Then: "arrive"}},
			Fallback: gateFallback,
		},
		{
			ID:     "arrive",
			Prompt: "the happy successor, must not run in these tests",
		},
		{
			ID:     "explain",
			Prompt: "the fallback target — reports what happened",
		},
	}
	registerTestFlow(t, Flow{ID: flowID, Name: "Dead End", Spec: FlowSpec{Steps: steps}})

	agent := &stubAgent{Broker: pubsub.NewBroker[agentpkg.AgentEvent](), responses: nil}
	svc := NewService(&stubSessions{}, nil, &stubQuerier{}, &stubPermissions{}, &stubAgentFactory{agent: agent})

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", flowID, map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	var states []*FlowState
	for s := range flowStates {
		states = append(states, s)
	}
	for range agentEvents {
	}
	return agent, states
}

func stepStatus(states []*FlowState, id string) FlowStatus {
	var last FlowStatus
	for _, s := range states {
		if s.StepID == id {
			last = s.Status
		}
	}
	return last
}

func ranStep(states []*FlowState, id string) bool {
	for _, s := range states {
		if s.StepID == id {
			return true
		}
	}
	return false
}

// require_route + a fallback: the fallback step runs, so the run reaches something
// that can explain the stop, and the gate itself is recorded failed rather than
// completed.
func TestRequireRoute_NoMatchRoutesToFallback(t *testing.T) {
	_, states := runDeadEndFlow(t, "require-route-fallback", true,
		&Fallback{Retry: 0, To: "explain"})

	if !ranStep(states, "explain") {
		t.Fatalf("expected the fallback step 'explain' to run; states: %+v", stepIDs(states))
	}
	if got := stepStatus(states, "gate"); got != FlowStatusFailed {
		t.Errorf("gate status = %q, want failed (a required route was not selected)", got)
	}
	if ranStep(states, "arrive") {
		t.Error("'arrive' ran, but its rule could never match")
	}
}

// require_route with no fallback: nothing absorbs it, so the run ends through the
// failure path rather than reporting a completion.
func TestRequireRoute_NoMatchNoFallbackFails(t *testing.T) {
	_, states := runDeadEndFlow(t, "require-route-nofallback", true, nil)

	if got := stepStatus(states, "gate"); got != FlowStatusFailed {
		t.Errorf("gate status = %q, want failed", got)
	}
	if ranStep(states, "arrive") || ranStep(states, "explain") {
		t.Error("no successor should have run")
	}
}

// The default. Without the field, a zero-match keeps its historical behaviour:
// the step completes, nothing is scheduled, the run ends. This is the regression
// guard for every flow authored before the field existed.
func TestRequireRoute_AbsentPreservesSilentCompletion(t *testing.T) {
	_, states := runDeadEndFlow(t, "require-route-absent", false,
		&Fallback{Retry: 0, To: "explain"})

	if got := stepStatus(states, "gate"); got != FlowStatusCompleted {
		t.Errorf("gate status = %q, want completed — the default must not change", got)
	}
	if ranStep(states, "explain") {
		t.Error("fallback ran without require_route; a zero-match is not a step error by default")
	}
	if ranStep(states, "arrive") {
		t.Error("'arrive' ran, but its rule could never match")
	}
}

// A bounded self-loop ends by predicate: at the capping iteration its only rule
// goes false and the run stops. That is a SANCTIONED termination (flow-runtime-resume,
// "Self-loop terminated by predicate restarts on re-trigger"), so it must not become
// an error just because the step selected no successor.
func TestRequireRoute_BoundedSelfLoopStillTerminatesNormally(t *testing.T) {
	steps := []Step{{
		ID:     "loop",
		Prompt: "iterate",
		Rules:  []Rule{{If: "${step.iteration} != 2", Then: "loop"}},
	}}
	registerTestFlow(t, Flow{ID: "require-route-loop", Name: "Loop", Spec: FlowSpec{Steps: steps}})

	agent := &stubAgent{Broker: pubsub.NewBroker[agentpkg.AgentEvent](), responses: nil}
	svc := NewService(&stubSessions{}, nil, &stubQuerier{}, &stubPermissions{}, &stubAgentFactory{agent: agent})
	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", "require-route-loop", map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	var states []*FlowState
	for s := range flowStates {
		states = append(states, s)
	}
	for range agentEvents {
	}

	if got := stepStatus(states, "loop"); got != FlowStatusCompleted {
		t.Errorf("loop status = %q, want completed — a predicate-terminated loop is not a failure", got)
	}
	if got := agent.callCount(); got != 2 {
		t.Errorf("agent calls = %d, want 2 (iteration 1 self-routes, iteration 2 ends the loop)", got)
	}
}

// A terminal step declares no rules at all. Selecting no successor is its entire
// purpose, so it must complete cleanly — and the dead-end branch must not consider
// it, with or without the field.
func TestRequireRoute_TerminalStepWithNoRulesCompletes(t *testing.T) {
	steps := []Step{
		{ID: "work", Prompt: "do it", Rules: []Rule{{Then: "finish"}}},
		{ID: "finish", Prompt: "summarise", RequireRoute: true},
	}
	registerTestFlow(t, Flow{ID: "require-route-terminal", Name: "Terminal", Spec: FlowSpec{Steps: steps}})

	agent := &stubAgent{Broker: pubsub.NewBroker[agentpkg.AgentEvent](), responses: nil}
	svc := NewService(&stubSessions{}, nil, &stubQuerier{}, &stubPermissions{}, &stubAgentFactory{agent: agent})
	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", "require-route-terminal", map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	var states []*FlowState
	for s := range flowStates {
		states = append(states, s)
	}
	for range agentEvents {
	}

	if got := stepStatus(states, "finish"); got != FlowStatusCompleted {
		t.Errorf("finish status = %q, want completed — a step with no rules is terminal by design", got)
	}
}

func stepIDs(states []*FlowState) []string {
	out := make([]string, 0, len(states))
	for _, s := range states {
		out = append(out, s.StepID+"="+string(s.Status))
	}
	return out
}
