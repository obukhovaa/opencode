package flow

import (
	"errors"
	"strings"
	"testing"

	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
)

// TestRun_MutualFallbackTerminates is the reviewer's probe from PR #57: two
// steps whose failures keep feeding each other must terminate, and must
// terminate as a visible failure. Both loop shapes are covered:
//
//   - pure mutual fallback (a --fallback--> b --fallback--> a);
//   - a fallback edge closed by a rule (a --fallback--> b --rule cycle--> a),
//     where `b` completes and its cycle rule sends `a` back to fail again.
//
// Bound: with N = maxFallbackEntries, each step is entered via fallback at
// most N times, so a two-step loop performs at most 1 + 2N step runs (one
// initial run of `a` plus N fallback-origin runs of each step); the (N+1)th
// fallback arrival is refused with a failed flow state instead of running.
// Only steps that reach the agent contribute NewAgent calls, so the assert
// below is the exact number for each shape rather than just the ceiling.
func TestRun_MutualFallbackTerminates(t *testing.T) {
	const n = DefaultMaxFallbackEntries
	alwaysFail := agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeError, Error: errors.New("boom")}

	tests := []struct {
		name string
		flow Flow
		args map[string]any
		// responses scripts the shared stub agent.
		responses []agentpkg.AgentEvent
		// refused is the step whose (N+1)th fallback arrival trips the cap.
		refused string
		// wantAgentCalls is the exact NewAgent count for this shape.
		wantAgentCalls int
		// wantRuns maps step id -> number of runStep entries that reached
		// the agent (asserted through NewAgent's stepID).
		wantRuns map[string]int
	}{
		{
			name: "pure mutual fallback",
			flow: Flow{
				ID:   "test-mutual-fallback",
				Name: "Mutual Fallback",
				Spec: FlowSpec{Steps: []Step{
					{ID: "a", Prompt: "a", Fallback: &Fallback{To: "b"}},
					{ID: "b", Prompt: "b", Fallback: &Fallback{To: "a"}},
				}},
			},
			responses: []agentpkg.AgentEvent{alwaysFail},
			// Arrival order: b1 a1 b2 a2 b3 a3 b4(refused).
			refused:        "b",
			wantAgentCalls: 1 + 2*n,
			wantRuns:       map[string]int{"a": 1 + n, "b": n},
		},
		{
			name: "fallback edge closed by a cycle rule",
			flow: Flow{
				ID:   "test-fallback-cycle-rule",
				Name: "Fallback + Cycle Rule",
				Spec: FlowSpec{Steps: []Step{
					// A templated model that resolves to an unknown id fails
					// `a` deterministically before NewAgent (and, being
					// templated, passes load validation), so only `b`
					// reaches the agent.
					{ID: "a", Prompt: "a", Model: "${args.impl_model}", Fallback: &Fallback{To: "b"}},
					{
						ID:     "b",
						Prompt: "b",
						Output: &StepOutput{Schema: map[string]any{"type": "object"}},
						Rules:  []Rule{{If: "${args.again} == true", Then: "a", Cycle: true}},
					},
				}},
			},
			args:      map[string]any{"impl_model": "bedrock.eu-claude-nope"},
			responses: []agentpkg.AgentEvent{loopRespond(`{"again": true}`)},
			// a fails, b1 completes -> a fails, b2 ... b3 -> a fails -> b4 refused.
			refused:        "b",
			wantAgentCalls: n,
			wantRuns:       map[string]int{"b": n},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := newStubAgent()
			agent.responses = tt.responses
			factory, states := runOverrideFlow(t, tt.flow, tt.args, agent)

			calls := factory.snapshotNewAgentCalls()
			if len(calls) != tt.wantAgentCalls {
				t.Fatalf("NewAgent calls = %d, want %d (1 initial + %d fallback entries per step)", len(calls), tt.wantAgentCalls, n)
			}
			runs := map[string]int{}
			for _, c := range calls {
				runs[c.stepID]++
			}
			for id, want := range tt.wantRuns {
				if runs[id] != want {
					t.Errorf("step %q reached the agent %d times, want %d", id, runs[id], want)
				}
			}

			if len(states) == 0 {
				t.Fatal("no flow states emitted")
			}
			last := states[len(states)-1]
			if last.Status != FlowStatusFailed {
				t.Fatalf("final flow state = %s/%s, want failed (the refusal must be the terminal event)", last.StepID, last.Status)
			}
			if last.StepID != tt.refused {
				t.Errorf("final failed step = %q, want %q", last.StepID, tt.refused)
			}
			if !strings.Contains(last.Output, "maxFallbackEntries=3") || !strings.Contains(last.Output, "fallback re-entry limit exceeded") {
				t.Errorf("refusal output = %q, want it to name the limit", last.Output)
			}
			// The refused arrival is the (N+1)th, and it never ran, so the
			// target's fallback-origin completions/failures stop at N.
			refusedArrivals := 0
			for _, st := range states {
				if st.StepID == tt.refused && strings.Contains(st.Output, "fallback re-entry limit exceeded") {
					refusedArrivals++
				}
			}
			if refusedArrivals != 1 {
				t.Errorf("refusal states = %d, want exactly 1 (no further fallback may be enqueued after a refusal)", refusedArrivals)
			}
		})
	}
}

// TestRun_MutualFallbackTerminates_RespectsFlowKey pins that the cap reads
// flow.maxFallbackEntries rather than the default.
func TestRun_MutualFallbackTerminates_RespectsFlowKey(t *testing.T) {
	f := Flow{
		ID:   "test-mutual-fallback-key",
		Name: "Mutual Fallback Key",
		Spec: FlowSpec{
			MaxFallbackEntries: 1,
			Steps: []Step{
				{ID: "a", Prompt: "a", Fallback: &Fallback{To: "b"}},
				{ID: "b", Prompt: "b", Fallback: &Fallback{To: "a"}},
			},
		},
	}
	agent := newStubAgent()
	agent.responses = []agentpkg.AgentEvent{{Type: agentpkg.AgentEventTypeError, Error: errors.New("boom")}}
	factory, states := runOverrideFlow(t, f, map[string]any{}, agent)
	// a, b1, a1, then b2 refused.
	if got := len(factory.snapshotNewAgentCalls()); got != 3 {
		t.Errorf("NewAgent calls = %d, want 3 (1 + 2*maxFallbackEntries)", got)
	}
	last := states[len(states)-1]
	if last.Status != FlowStatusFailed || !strings.Contains(last.Output, "maxFallbackEntries=1") {
		t.Errorf("final state = %+v, want failed naming maxFallbackEntries=1", last)
	}
}

func TestFlowSpec_EffectiveMaxFallbackEntries(t *testing.T) {
	tests := []struct {
		name string
		set  int
		want int
	}{
		{name: "unset defaults to 3", set: 0, want: 3},
		{name: "explicit value used as-is", set: 5, want: 5},
		{name: "one is honoured, not treated as unset", set: 1, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := FlowSpec{MaxFallbackEntries: tt.set}
			if got := spec.EffectiveMaxFallbackEntries(); got != tt.want {
				t.Errorf("EffectiveMaxFallbackEntries() = %d, want %d", got, tt.want)
			}
		})
	}

	t.Run("validateFlow rejects negative", func(t *testing.T) {
		err := validateFlow(&Flow{ID: "neg", Spec: FlowSpec{
			MaxFallbackEntries: -1,
			Steps:              []Step{{ID: "a", Prompt: "x"}},
		}})
		if !errors.Is(err, ErrInvalidMaxFallbackEntries) {
			t.Fatalf("validateFlow() = %v, want %v", err, ErrInvalidMaxFallbackEntries)
		}
	})
}
