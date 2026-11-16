package flow

import (
	"context"
	"testing"
)

// TestRunStep_BestEffortFailure: a failing best-effort step publishes its
// failed state flagged BestEffort and no error agent event (session-owned
// runs fail on any), while its sibling branch completes normally. The same
// step without the flag still emits the error event.
func TestRunStep_BestEffortFailure(t *testing.T) {
	for _, bestEffort := range []bool{true, false} {
		name := "best-effort"
		if !bestEffort {
			name = "regular"
		}
		t.Run(name, func(t *testing.T) {
			f := Flow{
				ID:   "test-best-effort-" + name,
				Name: "Test Best Effort",
				Spec: FlowSpec{Steps: []Step{
					{ID: "review", Prompt: "review", Rules: []Rule{{Then: "approve"}, {Then: "shadow"}}},
					{ID: "approve", Prompt: "approve"},
					// A literal unknown model fails before NewAgent on every
					// arrival (registerTestFlow bypasses validateFlow).
					{ID: "shadow", Prompt: "shadow", Model: "bedrock.eu-claude-nope", BestEffort: bestEffort},
				}},
			}
			registerTestFlow(t, f)
			svc := NewService(&stubSessions{}, nil, &stubQuerier{}, &stubPermissions{}, &stubAgentFactory{agent: newStubAgent()})
			agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", f.ID, nil, true)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			res := drainFlowRun(t, agentEvents, flowStates)

			shadow := findLatestByStepID(res.states, "shadow")
			if shadow == nil || shadow.Status != FlowStatusFailed || shadow.BestEffort != bestEffort {
				t.Fatalf("shadow state = %+v, want failed with BestEffort=%v", shadow, bestEffort)
			}
			for _, st := range res.states {
				if st.StepID == "shadow" && st.BestEffort != bestEffort {
					t.Errorf("shadow %s state BestEffort = %v", st.Status, st.BestEffort)
				}
				if st.StepID != "shadow" && st.BestEffort {
					t.Errorf("%s %s state is flagged best-effort", st.StepID, st.Status)
				}
			}
			if approve := findLatestByStepID(res.states, "approve"); approve == nil || approve.Status != FlowStatusCompleted {
				t.Fatalf("approve state = %+v, want completed", approve)
			}
			wantErrors := 1
			if bestEffort {
				wantErrors = 0
			}
			if len(res.errors) != wantErrors {
				t.Fatalf("error agent events = %v, want %d", res.errors, wantErrors)
			}
		})
	}
}
