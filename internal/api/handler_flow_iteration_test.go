package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/flow"
)

func TestStatusPreservesExecutionIteration(t *testing.T) {
	t.Parallel()
	_, fr := newFlowTestServerWithSessions(t, newStubFlowService(nil), &stubSessions{})
	state := &flowRunState{RunID: "r", FlowID: "f", Status: flowRunRunning}
	step := &flow.FlowState{StepID: "preview", SessionID: "session", Status: flow.FlowStatusRunning, Iteration: 3, UpdatedAt: 1000}
	fr.observeStep(state, step)
	read := func() *flowRunSnapshot {
		t.Helper()
		wire, err := json.Marshal(snapshotOf(state))
		if err != nil {
			t.Fatal(err)
		}
		var got flowRunSnapshot
		if err := json.Unmarshal(wire, &got); err != nil {
			t.Fatal(err)
		}
		return &got
	}
	for range 2 {
		if got := read().CurrentStep; got == nil || got.Iteration != 3 {
			t.Fatalf("current=%+v", got)
		}
	}
	step.Status = flow.FlowStatusWaitingForInput
	fr.observeStep(state, step)
	step.Status = flow.FlowStatusRetrying
	fr.observeStep(state, step)
	if got := read(); got.CurrentStep.Iteration != 3 || got.Status != flowRunWaitingForInput || len(got.CompletedSteps) != 0 {
		t.Fatalf("retry changed execution: %+v", got)
	}
	step.Status = flow.FlowStatusCompleted
	fr.observeStep(state, step)
	if got := read(); got.CurrentStep != nil || len(got.CompletedSteps) != 1 || got.CompletedSteps[0].Iteration != 3 {
		t.Fatalf("completion=%+v", got)
	}
	// Same session and timestamp, distinct persisted execution.
	step.Status = flow.FlowStatusRunning
	step.Iteration = 4
	fr.observeStep(state, step)
	got := read()
	if got.CurrentStep == nil || got.CurrentStep.Iteration != 4 || got.CompletedSteps[0].Iteration != 3 {
		t.Fatalf("re-entry=%+v", got)
	}
}

func TestStatusOmitsUnknownIteration(t *testing.T) {
	t.Parallel()
	wire, err := json.Marshal(flowStepRecord{ID: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), `"iteration"`) {
		t.Fatalf("unknown iteration emitted: %s", wire)
	}
}

func TestStatusHTTPIterationSurvivesTerminalRetention(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		stepStatus flow.FlowStatus
		runStatus  flowRunStatus
	}{
		{"completed", flow.FlowStatusCompleted, flowRunCompleted},
		{"failed", flow.FlowStatusFailed, flowRunFailed},
		{"postponed", flow.FlowStatusPostponed, flowRunPostponed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, fr := newFlowTestServerWithSessions(t, newStubFlowService(nil), &stubSessions{})
			fr.poolMode = true
			fr.idleResetGrace = 0
			state := &flowRunState{RunID: "first-run", FlowID: "review", Status: flowRunRunning}
			fr.currentRun = state
			read := func(runID string) *flowRunSnapshot {
				t.Helper()
				resp, err := server.Client().Get(server.URL + "/flow/status?runID=" + runID)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("status=%d", resp.StatusCode)
				}
				var got flowRunSnapshot
				if err = json.NewDecoder(resp.Body).Decode(&got); err != nil {
					t.Fatal(err)
				}
				return &got
			}
			step := &flow.FlowState{StepID: "review", SessionID: "shared-session", Status: flow.FlowStatusRunning, Iteration: 3}
			fr.observeStep(state, step)
			for range 2 {
				got := read("first-run")
				if got.RunID != "first-run" || got.CurrentStep == nil || got.CurrentStep.Iteration != 3 {
					t.Fatalf("current=%+v", got)
				}
			}
			step.Status = tc.stepStatus
			fr.observeStep(state, step)
			fr.finish(state, tc.runStatus, "")
			if got := fr.Snapshot(); got != nil {
				t.Fatalf("live state not reset: %+v", got)
			}
			next := &flowRunState{RunID: "next-run", FlowID: "review", Status: flowRunRunning}
			fr.mu.Lock()
			fr.currentRun = next
			fr.mu.Unlock()
			step.Status = flow.FlowStatusRunning
			step.Iteration = 4
			fr.observeStep(next, step)
			for range 2 {
				got := read("first-run")
				if got.RunID != "first-run" || got.Status != tc.runStatus || len(got.CompletedSteps) != 1 || got.CompletedSteps[0].Iteration != 3 || got.CompletedSteps[0].Status != string(tc.stepStatus) {
					t.Fatalf("retained=%+v", got)
				}
			}
			if got := read("next-run"); got.CurrentStep == nil || got.CurrentStep.Iteration != 4 {
				t.Fatalf("next=%+v", got)
			}
		})
	}
}
