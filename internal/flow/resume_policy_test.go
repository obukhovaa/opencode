package flow

import (
	"context"
	"database/sql"
	"reflect"
	"sync"
	"testing"

	"github.com/opencode-ai/opencode/internal/db"
)

func TestModelPoolResumeRetainsRuntimePick(t *testing.T) {
	f := &Flow{ID: "review-pool-repro", Spec: FlowSpec{Steps: []Step{
		{ID: "review", Prompt: "review", ModelPool: `[{"model":"bedrock.eu-claude-sonnet-5-5","weight":1}]`, Rules: []Rule{{Then: "main"}, {If: "${args.pool.review.shadow} == true", Then: "shadow"}}},
		{ID: "main", Prompt: "main"},
		{ID: "shadow", Prompt: "shadow", Model: "${args.pool.review.shadow_model}", BestEffort: true},
	}}}
	pickArgs := map[string]any{"pool": map[string]any{"review": map[string]any{"model": "bedrock.eu-claude-sonnet-5-5", "shadow": true, "shadow_model": "bedrock.us-kimi-k3.1"}}}
	states := map[string]*FlowState{
		"review": {StepID: "review", Status: FlowStatusCompleted, Args: pickArgs, Iteration: 1},
		"main":   {StepID: "main", Status: FlowStatusCompleted, Args: pickArgs, Iteration: 1},
	}
	svc := NewService(&stubSessions{}, nil, &stubQuerier{}, &stubPermissions{}, &stubAgentFactory{agent: newStubAgent()}).(*service)
	got := svc.collectResumableSteps(context.Background(), f, f.Spec.Steps[0], map[string]any{}, nil, states, map[string]bool{}, &sync.Map{}, make(chan *FlowState, 20))
	if len(got) != 1 || got[0].step.ID != "shadow" {
		t.Fatalf("resume work = %+v; wanted pending shadow branch", got)
	}
	if got[0].args["pool"] == nil {
		t.Fatal("shadow work lost pool pick")
	}
}

func TestReplayRetainsBestEffort(t *testing.T) {
	f := Flow{ID: "review-besteffort-replay", Spec: FlowSpec{Steps: []Step{
		{ID: "main", Prompt: "main"}, {ID: "shadow", Prompt: "shadow", BestEffort: true},
	}}}
	registerTestFlow(t, f)
	root := "prefix-" + f.ID + "-main"
	q := &stubQuerier{flowStates: []db.FlowState{
		{SessionID: root, RootSessionID: root, FlowID: f.ID, StepID: "main", Status: string(FlowStatusRunning)},
		{SessionID: "prefix-" + f.ID + "-shadow", RootSessionID: root, FlowID: f.ID, StepID: "shadow", Status: string(FlowStatusFailed), Output: sql.NullString{String: "optional failure", Valid: true}},
	}}
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: newStubAgent()})
	events, states, err := svc.Run(context.Background(), "prefix", f.ID, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var shadow *FlowState
	for st := range states {
		if st.StepID == "shadow" {
			shadow = st
		}
	}
	for range events {
	}
	if shadow == nil {
		t.Fatal("no shadow state replayed")
	}
	if !shadow.BestEffort {
		t.Fatalf("replayed failure lost BestEffort: %+v", shadow)
	}
}

func TestRestorePoolPickPreservesOtherBranchesAndPicks(t *testing.T) {
	first := map[string]any{"model": poolSonnet}
	second := map[string]any{"model": poolKimi}
	oldSecond := map[string]any{"model": poolSol}
	shared := map[string]any{"first": first, "second": oldSecond}
	args := map[string]any{ReservedPoolArg: shared, "instructions": "current"}
	persistedPool := map[string]any{"first": oldSecond, "second": second}
	persisted := map[string]any{ReservedPoolArg: persistedPool, "instructions": "stale"}

	restorePoolPick(args, persisted, "second")
	got := args[ReservedPoolArg].(map[string]any)
	if !reflect.DeepEqual(got, map[string]any{"first": first, "second": second}) {
		t.Fatalf("restored picks = %v", got)
	}
	if !reflect.DeepEqual(shared["second"], oldSecond) || !reflect.DeepEqual(persistedPool["first"], oldSecond) {
		t.Fatal("restoration changed a shared or persisted pool")
	}
	if args["instructions"] != "current" {
		t.Fatal("restoration overwrote a fresh caller argument")
	}
	restorePoolPick(args, persisted, "absent")
	if _, exists := got["absent"]; exists {
		t.Fatal("restoration fabricated a pick")
	}
}

func TestResumeRetainsBestEffortOnCompletedStep(t *testing.T) {
	f := Flow{ID: "test-resume-best-effort-completed", Spec: FlowSpec{Steps: []Step{
		{ID: "optional", Prompt: "optional", BestEffort: true, Rules: []Rule{{Then: "pending"}}},
		{ID: "pending", Prompt: "pending"},
	}}}
	registerTestFlow(t, f)
	root := "prefix-" + f.ID + "-optional"
	q := &stubQuerier{flowStates: []db.FlowState{
		{SessionID: root, RootSessionID: root, FlowID: f.ID, StepID: "optional", Status: string(FlowStatusCompleted)},
	}}
	agent := newStubAgent()
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})
	events, states, err := svc.Run(context.Background(), "prefix", f.ID, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	result := drainFlowRun(t, events, states)
	optional := findLatestByStepID(result.states, "optional")
	if optional == nil || !optional.BestEffort {
		t.Fatalf("completed optional step lost BestEffort: %+v", optional)
	}
	if agent.callCount() != 1 {
		t.Fatalf("agent calls = %d, want only the pending step", agent.callCount())
	}
}
