package flow

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// loopRespond returns the same struct_output response for any call. Use when
// the agent just needs to succeed and let rules drive the loop.
func loopRespond(content string) agentpkg.AgentEvent {
	return agentpkg.AgentEvent{
		Type:         agentpkg.AgentEventTypeResponse,
		Message:      message.Message{Role: message.Assistant},
		StructOutput: &message.ToolResult{Name: "struct_output", Content: content},
	}
}

// drainFlow consumes both channels until close and collects the published states.
func drainFlow(t *testing.T, agentEvents <-chan agentpkg.AgentEvent, flowStates <-chan *FlowState) []*FlowState {
	t.Helper()
	var states []*FlowState
	doneStates := make(chan struct{})
	doneEvents := make(chan struct{})
	go func() {
		for range agentEvents {
		}
		close(doneEvents)
	}()
	go func() {
		for s := range flowStates {
			states = append(states, s)
		}
		close(doneStates)
	}()
	<-doneEvents
	<-doneStates
	return states
}

// findLatestByStepID returns the last state observed for a step.
func findLatestByStepID(states []*FlowState, stepID string) *FlowState {
	var found *FlowState
	for _, s := range states {
		if s.StepID == stepID {
			found = s
		}
	}
	return found
}

// countCompletedByStepID counts completed events for a given step.
func countCompletedByStepID(states []*FlowState, stepID string) int {
	count := 0
	for _, s := range states {
		if s.StepID == stepID && s.Status == FlowStatusCompleted {
			count++
		}
	}
	return count
}

// TestSelfLoop_InProcess verifies a step routes to itself in-process across
// multiple iterations within a single flow invocation. The loop terminates via
// a `${step.iteration} == N` rule.
func TestSelfLoop_InProcess(t *testing.T) {
	testFlow := Flow{
		ID:   "test-self-loop",
		Name: "Test Self Loop",
		Spec: FlowSpec{
			Steps: []Step{
				{
					ID:     "loop",
					Prompt: "iter=${step.iteration}",
					Output: &StepOutput{Schema: map[string]any{"type": "object"}},
					Rules: []Rule{
						{If: "${step.iteration} != 3", Then: "loop"},
						{If: "${step.iteration} == 3", Then: "end"},
					},
				},
				{ID: "end", Prompt: "all done"},
			},
		},
	}
	registerTestFlow(t, testFlow)

	agent := &stubAgent{
		Broker: pubsub.NewBroker[agentpkg.AgentEvent](),
		responses: []agentpkg.AgentEvent{
			loopRespond(`{"ok":true}`),
		},
	}
	q := &stubQuerier{}
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", testFlow.ID, map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	states := drainFlow(t, agentEvents, flowStates)

	// Loop step should have completed 3 times.
	if got := countCompletedByStepID(states, "loop"); got != 3 {
		t.Errorf("loop completed = %d, want 3", got)
	}
	// End step should have completed once.
	if got := countCompletedByStepID(states, "end"); got != 1 {
		t.Errorf("end completed = %d, want 1", got)
	}
	// Latest persisted iteration for loop should be 3.
	final := findLatestByStepID(states, "loop")
	if final == nil || final.Iteration != 3 {
		t.Errorf("final loop iteration = %v, want 3", final)
	}
	// Agent should have been called 3 (loop) + 1 (end) = 4 times.
	if c := agent.callCount(); c != 4 {
		t.Errorf("agent calls = %d, want 4", c)
	}
	// Verify each iteration saw the right step.iteration in its prompt —
	// a count-only assertion would still pass under an off-by-one in
	// the iteration bump, so check the ordering explicitly.
	prompts := agent.snapshotPrompts()
	wantPromptPrefixes := []string{"iter=1", "iter=2", "iter=3"}
	if len(prompts) < len(wantPromptPrefixes) {
		t.Fatalf("agent.prompts length = %d, want at least %d", len(prompts), len(wantPromptPrefixes))
	}
	for i, want := range wantPromptPrefixes {
		if !strings.HasPrefix(prompts[i], want) {
			t.Errorf("agent.prompts[%d] = %q, want prefix %q", i, prompts[i], want)
		}
	}
}

// TestSelfLoop_PostponeAtIterationNGreaterThanOne verifies the iteration
// counter survives a postpone-self that fires from iteration N > 1.
// Regression: the rule-enqueue site originally reset iteration to 1 for
// any postpone branch, including postpone-self, causing the persisted row
// to lose the iteration counter and resume to restart at iter 1.
func TestSelfLoop_PostponeAtIterationNGreaterThanOne(t *testing.T) {
	testFlow := Flow{
		ID:   "test-postpone-iter-gt-1",
		Name: "Postpone at N>1",
		Spec: FlowSpec{
			Steps: []Step{
				{
					ID:     "loop",
					Prompt: "iter=${step.iteration}",
					Output: &StepOutput{Schema: map[string]any{"type": "object"}},
					Rules: []Rule{
						// iter 1 → in-process loop to iter 2
						{If: "${step.iteration} == 1", Then: "loop"},
						// iter 2 → postpone-self; should persist iteration=2
						{If: "${step.iteration} == 2", Then: "loop", Postpone: true},
					},
				},
			},
		},
	}
	registerTestFlow(t, testFlow)

	agent := &stubAgent{
		Broker: pubsub.NewBroker[agentpkg.AgentEvent](),
		responses: []agentpkg.AgentEvent{
			loopRespond(`{"ok":true}`),
		},
	}
	q := &stubQuerier{}
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", testFlow.ID, map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	states := drainFlow(t, agentEvents, flowStates)

	final := findLatestByStepID(states, "loop")
	if final == nil {
		t.Fatalf("expected loop state, got: %+v", states)
	}
	if final.Status != FlowStatusPostponed {
		t.Errorf("final loop status = %q, want %q", final.Status, FlowStatusPostponed)
	}
	if final.Iteration != 2 {
		t.Errorf("postponed iteration = %d, want 2 (postpone fired from iter 2)", final.Iteration)
	}

	// Check the persisted row matches.
	for _, fs := range q.snapshotFlowStates() {
		if fs.StepID == "loop" {
			if fs.Status != string(FlowStatusPostponed) {
				t.Errorf("persisted status = %q, want %q", fs.Status, FlowStatusPostponed)
			}
			if fs.Iteration != 2 {
				t.Errorf("persisted iteration = %d, want 2", fs.Iteration)
			}
		}
	}
}

// TestSelfLoop_ResumeRespectsMaxIterationsCap verifies that resume of a
// self-looping step honors `maxIterations`. Scenario: iter N completed and
// process died before either iter N+1's running write happened OR the
// cap-tripping pre-check on iter N's row fired. Without an explicit check
// on the resume path, iter N+1 would run the agent (one wasted call past
// the cap) before failing. The resume path should fail the step directly
// and route to fallback.
func TestSelfLoop_ResumeRespectsMaxIterationsCap(t *testing.T) {
	testFlow := Flow{
		ID:   "test-resume-cap",
		Name: "Resume Cap",
		Spec: FlowSpec{
			Steps: []Step{
				{
					ID:            "loop",
					Prompt:        "iter=${step.iteration}",
					Output:        &StepOutput{Schema: map[string]any{"type": "object"}},
					MaxIterations: 2,
					Rules:         []Rule{{Then: "loop"}}, // unconditional self-route
					Fallback:      &Fallback{To: "failed"},
				},
				{ID: "failed", Prompt: "the loop hit its cap"},
			},
		},
	}
	registerTestFlow(t, testFlow)

	rootSessionID := "prefix-test-resume-cap-loop"
	now := time.Now().Unix()
	q := &stubQuerier{
		flowStates: []db.FlowState{
			{
				SessionID:      rootSessionID,
				RootSessionID:  rootSessionID,
				FlowID:         "test-resume-cap",
				StepID:         "loop",
				Status:         string(FlowStatusCompleted),
				Args:           sql.NullString{String: `{}`, Valid: true},
				Output:         sql.NullString{String: `{"ok":true}`, Valid: true},
				IsStructOutput: true,
				Iteration:      2, // already at the cap
				CreatedAt:      now,
				UpdatedAt:      now,
			},
		},
	}

	agent := newStubAgent()
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	// Resume — loop is already at MaxIterations. Next iteration would trip
	// the cap, so no agent call should happen for the loop step.
	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", testFlow.ID, map[string]any{}, false)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	states := drainFlow(t, agentEvents, flowStates)

	// Only the fallback step should call the agent.
	if c := agent.callCount(); c != 1 {
		t.Errorf("agent calls = %d, want 1 (only the fallback step should run; loop must not exceed the cap)", c)
	}

	// Loop step's terminal state should be `failed` with the cap message,
	// at iteration 2 (the iter that completed before the cap fired).
	loopFinal := findLatestByStepID(states, "loop")
	if loopFinal == nil {
		t.Fatalf("expected loop state, got: %+v", states)
	}
	if loopFinal.Status != FlowStatusFailed {
		t.Errorf("loop status = %q, want %q", loopFinal.Status, FlowStatusFailed)
	}
	if loopFinal.Iteration != 2 {
		t.Errorf("loop iteration on failure = %d, want 2", loopFinal.Iteration)
	}
	if !strings.Contains(loopFinal.Output, "exceeded maxIterations") {
		t.Errorf("loop output = %q, want substring 'exceeded maxIterations'", loopFinal.Output)
	}

	// Fallback step should have completed.
	if got := countCompletedByStepID(states, "failed"); got != 1 {
		t.Errorf("fallback step completed = %d, want 1", got)
	}
}

// TestSelfLoop_ResumeAfterCompletedIterationCrash verifies that a flow can
// resume a self-loop when the prior process died between writing iter N's
// "completed" row and iter N+1's "running" row. The completed row is
// pre-populated to simulate this state; resume must schedule iter N+1.
//
// Regression: collectResumableSteps marked the step as visited before
// walking its rules, so a recursive self-route returned nil and the loop
// silently ended without scheduling the next iteration.
func TestSelfLoop_ResumeAfterCompletedIterationCrash(t *testing.T) {
	testFlow := Flow{
		ID:   "test-resume-after-crash",
		Name: "Resume After Crash",
		Spec: FlowSpec{
			Steps: []Step{
				{
					ID:     "loop",
					Prompt: "iter=${step.iteration}",
					Output: &StepOutput{Schema: map[string]any{"type": "object"}},
					Rules: []Rule{
						{If: "${step.iteration} != 3", Then: "loop"},
					},
				},
			},
		},
	}
	registerTestFlow(t, testFlow)

	rootSessionID := "prefix-test-resume-after-crash-loop"
	now := time.Now().Unix()
	q := &stubQuerier{
		flowStates: []db.FlowState{
			{
				SessionID:      rootSessionID,
				RootSessionID:  rootSessionID,
				FlowID:         "test-resume-after-crash",
				StepID:         "loop",
				Status:         string(FlowStatusCompleted),
				Args:           sql.NullString{String: `{}`, Valid: true},
				Output:         sql.NullString{String: `{"ok":true}`, Valid: true},
				IsStructOutput: true,
				Iteration:      2,
				CreatedAt:      now,
				UpdatedAt:      now,
			},
		},
	}

	agent := &stubAgent{
		Broker: pubsub.NewBroker[agentpkg.AgentEvent](),
		responses: []agentpkg.AgentEvent{
			loopRespond(`{"ok":true}`),
		},
	}
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	// fresh=false → resume path; should schedule iter 3 directly because the
	// completed iter 2 row's rules self-route.
	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", testFlow.ID, map[string]any{}, false)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	states := drainFlow(t, agentEvents, flowStates)

	// Agent must have run exactly once — iter 3 (after which the rule's
	// predicate flips false and the step is terminal).
	if c := agent.callCount(); c != 1 {
		t.Errorf("agent calls = %d, want 1 (iter 3 only)", c)
	}

	// Persisted row should advance to iteration 3.
	final := findLatestByStepID(states, "loop")
	if final == nil {
		t.Fatalf("expected loop state, got states: %+v", states)
	}
	if final.Iteration != 3 {
		t.Errorf("final iteration = %d, want 3", final.Iteration)
	}
	if final.Status != FlowStatusCompleted {
		t.Errorf("final status = %q, want %q", final.Status, FlowStatusCompleted)
	}
}

// TestSelfLoop_DiamondGuardPreserved verifies that a non-self diamond
// (two upstream paths converging on the same downstream step) still runs
// the downstream step exactly once.
func TestSelfLoop_DiamondGuardPreserved(t *testing.T) {
	testFlow := Flow{
		ID:   "test-diamond",
		Name: "Diamond",
		Spec: FlowSpec{
			Steps: []Step{
				{
					ID:     "fork",
					Prompt: "fork",
					Rules: []Rule{
						{Then: "left"},
						{Then: "right"},
					},
				},
				{ID: "left", Prompt: "left", Rules: []Rule{{Then: "join"}}},
				{ID: "right", Prompt: "right", Rules: []Rule{{Then: "join"}}},
				{ID: "join", Prompt: "join"},
			},
		},
	}
	registerTestFlow(t, testFlow)

	agent := newStubAgent()
	q := &stubQuerier{}
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", testFlow.ID, map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	states := drainFlow(t, agentEvents, flowStates)

	if got := countCompletedByStepID(states, "join"); got != 1 {
		t.Errorf("join completed = %d, want 1", got)
	}
}

// TestSelfLoop_MaxIterationsCap verifies maxIterations halts the loop and
// routes through fallback. The completed event for the cap-tripping iteration
// is NOT emitted — only a single terminal `failed` event surfaces.
func TestSelfLoop_MaxIterationsCap(t *testing.T) {
	testFlow := Flow{
		ID:   "test-max-iter",
		Name: "Max Iter",
		Spec: FlowSpec{
			Steps: []Step{
				{
					ID:            "loop",
					Prompt:        "iter ${step.iteration}",
					MaxIterations: 2,
					Rules:         []Rule{{Then: "loop"}}, // unconditional self-route
					Fallback:      &Fallback{To: "failed"},
				},
				{ID: "failed", Prompt: "we failed"},
			},
		},
	}
	registerTestFlow(t, testFlow)

	agent := newStubAgent()
	q := &stubQuerier{}
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", testFlow.ID, map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	states := drainFlow(t, agentEvents, flowStates)

	// Loop should run 2 times (maxIterations=2). The 2nd run trips the cap
	// before publishing `completed`, so we observe exactly 1 completed and
	// 1 failed for the loop step.
	completedLoop := countCompletedByStepID(states, "loop")
	if completedLoop != 1 {
		t.Errorf("loop completed events = %d, want 1 (cap trips before 2nd completion publishes)", completedLoop)
	}

	final := findLatestByStepID(states, "loop")
	if final == nil {
		t.Fatal("expected at least one state for loop")
	}
	if final.Status != FlowStatusFailed {
		t.Errorf("final loop status = %q, want %q", final.Status, FlowStatusFailed)
	}
	if !strings.Contains(final.Output, "exceeded maxIterations") {
		t.Errorf("final loop output = %q, want substring 'exceeded maxIterations'", final.Output)
	}
	if final.Iteration != 2 {
		t.Errorf("final loop iteration = %d, want 2", final.Iteration)
	}

	// Fallback step should have run.
	if got := countCompletedByStepID(states, "failed"); got != 1 {
		t.Errorf("failed step completed = %d, want 1", got)
	}
}

// TestStepIteration_TemplateSubstitution verifies the agent's prompt is
// rendered with ${step.iteration} expanded to the correct value each
// iteration. Uses a struct-output step so the previous-text prepend doesn't
// fire (otherwise the agent prompt would also include "Previous step output: ...").
func TestStepIteration_TemplateSubstitution(t *testing.T) {
	testFlow := Flow{
		ID:   "test-iter-template",
		Name: "Iter Template",
		Spec: FlowSpec{
			Steps: []Step{
				{
					ID:     "loop",
					Prompt: "iter=${step.iteration}",
					Output: &StepOutput{Schema: map[string]any{"type": "object"}},
					Rules: []Rule{
						{If: "${step.iteration} != 3", Then: "loop"},
					},
				},
			},
		},
	}
	registerTestFlow(t, testFlow)

	agent := &stubAgent{
		Broker: pubsub.NewBroker[agentpkg.AgentEvent](),
		responses: []agentpkg.AgentEvent{
			loopRespond(`{"ok":true}`),
		},
	}
	q := &stubQuerier{}
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", testFlow.ID, map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	_ = drainFlow(t, agentEvents, flowStates)

	prompts := agent.snapshotPrompts()
	if len(prompts) != 3 {
		t.Fatalf("agent.prompts length = %d, want 3", len(prompts))
	}
	for i, want := range []string{"iter=1", "iter=2", "iter=3"} {
		if prompts[i] != want {
			t.Errorf("agent.prompts[%d] = %q, want %q", i, prompts[i], want)
		}
	}
}

// TestStepIteration_NotInArgs verifies the iteration counter is not leaked
// into args (and therefore not persisted on flow_states.args).
func TestStepIteration_NotInArgs(t *testing.T) {
	testFlow := Flow{
		ID:   "test-iter-not-args",
		Name: "Iter Not In Args",
		Spec: FlowSpec{
			Steps: []Step{
				{
					ID:     "loop",
					Prompt: "iter=${step.iteration}",
					Rules:  []Rule{{If: "${step.iteration} != 2", Then: "loop"}},
				},
			},
		},
	}
	registerTestFlow(t, testFlow)

	agent := newStubAgent()
	q := &stubQuerier{}
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", testFlow.ID, map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	states := drainFlow(t, agentEvents, flowStates)

	for _, s := range states {
		if _, ok := s.Args["iteration"]; ok {
			t.Errorf("args contain 'iteration' key — should be step-scoped only: %v", s.Args)
		}
	}
	// And the persisted args (via stubQuerier flow_states) must not contain it.
	for _, fs := range q.snapshotFlowStates() {
		if !fs.Args.Valid {
			continue
		}
		if strings.Contains(fs.Args.String, `"iteration"`) {
			t.Errorf("persisted args contain iteration key: %q", fs.Args.String)
		}
	}
}

// TestSelfLoop_PostponeStoresIteration verifies that a postpone-self rule
// persists the iteration counter so a future invocation can resume at the
// correct iteration. The postpone path itself stops at iteration N's "I am
// postponed" marker — it does not enter the agent loop again.
// TestMultiStepCycle_VerifyImplementLoop verifies that a two-step sequential
// cycle (verify → implement → verify), marked with `cycle: true` on the
// back-edge rule, admits the second `implement` schedule instead of dropping
// it as diamond convergence. Regression coverage for the pre-fix bug that
// caused CD-4497 to terminate at openspec-verify with drift notes populated
// but implement never re-running.
//
// The flow: implement (always ok) → verify (returns verified=false first,
// verified=true second) → cycle back to implement when verified=false OR
// route to end when verified=true.
func TestMultiStepCycle_VerifyImplementLoop(t *testing.T) {
	testFlow := Flow{
		ID:   "test-multi-step-cycle",
		Name: "Verify-Implement Cycle",
		Spec: FlowSpec{
			Steps: []Step{
				{
					ID:     "implement",
					Prompt: "impl iter=${step.iteration}",
					Output: &StepOutput{Schema: map[string]any{"type": "object"}},
					Rules: []Rule{
						// Cycle: true — this edge participates in the
						// verify/implement cycle. Both sides of the loop must
						// be marked so the guard admits re-entry from either
						// direction.
						{Then: "verify", Cycle: true},
					},
				},
				{
					ID:     "verify",
					Prompt: "verify iter=${step.iteration}",
					Output: &StepOutput{Schema: map[string]any{"type": "object"}},
					Rules: []Rule{
						{If: "${args.verified} == true", Then: "end"},
						// Cycle: true — back-edge of the two-step verify/implement
						// cycle. Without this flag the runtime treats the second
						// schedule of implement as diamond convergence.
						{If: "${args.verified} != true", Then: "implement", Cycle: true},
					},
				},
				{ID: "end", Prompt: "done"},
			},
		},
	}
	registerTestFlow(t, testFlow)

	// Call sequence:
	//   1. implement (iter 1) → {} (routes to verify)
	//   2. verify (iter 1) → {verified:false} (routes back to implement)
	//   3. implement (iter 1 again — cross-step target starts at 1) → {} (routes to verify)
	//   4. verify (iter 1 again) → {verified:true} (routes to end)
	//   5. end (iter 1) → {}
	agent := &stubAgent{
		Broker: pubsub.NewBroker[agentpkg.AgentEvent](),
		responses: []agentpkg.AgentEvent{
			loopRespond(`{}`),
			loopRespond(`{"verified":false}`),
			loopRespond(`{}`),
			loopRespond(`{"verified":true}`),
			loopRespond(`{}`),
		},
	}
	q := &stubQuerier{}
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", testFlow.ID, map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	states := drainFlow(t, agentEvents, flowStates)

	// Pre-fix bug: implement ran once, verify ran once, cycle-back was silently
	// dropped by the diamond-guard, end never ran. After the fix implement and
	// verify each run TWICE and end runs once.
	if got := countCompletedByStepID(states, "implement"); got != 2 {
		t.Errorf("implement completed = %d, want 2 (multi-step cycle should admit the second schedule)", got)
	}
	if got := countCompletedByStepID(states, "verify"); got != 2 {
		t.Errorf("verify completed = %d, want 2", got)
	}
	if got := countCompletedByStepID(states, "end"); got != 1 {
		t.Errorf("end completed = %d, want 1 (cycle should terminate cleanly)", got)
	}
}

// TestMultiStepCycle_IterationIncrements verifies that on a cycle:true
// back-edge, the target step's iteration counter is bumped to prior+1
// instead of resetting to 1 (as it does for a normal cross-step route).
// Regression coverage for the CD-4497 finding where
// `${step.iteration} < 3` on the verify→implement back-edge failed to
// cap the loop because both steps' flow_states rows always showed
// iteration=1 no matter how many times the cycle spun.
func TestMultiStepCycle_IterationIncrements(t *testing.T) {
	testFlow := Flow{
		ID:   "test-cycle-iter",
		Name: "Cycle Iteration",
		Spec: FlowSpec{
			Steps: []Step{
				{
					ID:     "implement",
					Prompt: "impl iter=${step.iteration}",
					Output: &StepOutput{Schema: map[string]any{"type": "object"}},
					Rules: []Rule{
						{Then: "verify", Cycle: true},
					},
				},
				{
					ID:     "verify",
					Prompt: "verify iter=${step.iteration}",
					Output: &StepOutput{Schema: map[string]any{"type": "object"}},
					Rules: []Rule{
						// step.iteration-driven cap: after 3 verify passes,
						// exit via `end`. Pre-fix bug: iteration reset to 1
						// on every re-entry, so this rule always failed the
						// `< 3` clause and the >= 3 branch never fired.
						{If: "${step.iteration} >= 3", Then: "end"},
						{If: "${step.iteration} < 3", Then: "implement", Cycle: true},
					},
				},
				{ID: "end", Prompt: "done"},
			},
		},
	}
	registerTestFlow(t, testFlow)

	// Verify keeps saying `verified: false` — the cap decides when to
	// exit, not the agent's output. All responses are empty {} so both
	// steps flow strictly by rule.
	agent := &stubAgent{
		Broker: pubsub.NewBroker[agentpkg.AgentEvent](),
		responses: []agentpkg.AgentEvent{
			loopRespond(`{}`),
		},
	}
	q := &stubQuerier{}
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", testFlow.ID, map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	states := drainFlow(t, agentEvents, flowStates)

	// The cycle should run 3 verify passes then exit through `end`.
	// implement runs 3 times (once before each verify): iter 1, 2, 3.
	// verify runs 3 times as well: iter 1, 2, 3.
	// On verify's 3rd pass the >= 3 rule fires and routes to `end`.
	if got := countCompletedByStepID(states, "implement"); got != 3 {
		t.Errorf("implement completed = %d, want 3", got)
	}
	if got := countCompletedByStepID(states, "verify"); got != 3 {
		t.Errorf("verify completed = %d, want 3", got)
	}
	if got := countCompletedByStepID(states, "end"); got != 1 {
		t.Errorf("end completed = %d, want 1", got)
	}

	// Confirm iteration bump: the last verify state should have iteration=3,
	// not the pre-fix value of 1.
	finalVerify := findLatestByStepID(states, "verify")
	if finalVerify == nil {
		t.Fatalf("no verify state persisted")
	}
	if finalVerify.Iteration != 3 {
		t.Errorf("final verify iteration = %d, want 3 (cycle re-entry must bump target iteration)", finalVerify.Iteration)
	}
	finalImpl := findLatestByStepID(states, "implement")
	if finalImpl == nil {
		t.Fatalf("no implement state persisted")
	}
	if finalImpl.Iteration != 3 {
		t.Errorf("final implement iteration = %d, want 3", finalImpl.Iteration)
	}
}

func TestSelfLoop_PostponeStoresIteration(t *testing.T) {
	testFlow := Flow{
		ID:   "test-postpone-stores-iter",
		Name: "Postpone Stores Iter",
		Spec: FlowSpec{
			Steps: []Step{
				{
					ID:     "loop",
					Prompt: "iter ${step.iteration}",
					Output: &StepOutput{Schema: map[string]any{"type": "object"}},
					Rules: []Rule{
						{If: "${args.blocked} == true", Then: "loop", Postpone: true},
						{If: "${args.blocked} != true", Then: "end"},
					},
				},
				{ID: "end", Prompt: "done"},
			},
		},
	}
	registerTestFlow(t, testFlow)

	agent := &stubAgent{
		Broker: pubsub.NewBroker[agentpkg.AgentEvent](),
		responses: []agentpkg.AgentEvent{
			loopRespond(`{"blocked":true}`),
		},
	}
	q := &stubQuerier{}
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", testFlow.ID, map[string]any{}, true)
	if err != nil {
		t.Fatalf("first Run() error: %v", err)
	}
	firstStates := drainFlow(t, agentEvents, flowStates)

	// Latest loop state should be postponed at iteration 1.
	final := findLatestByStepID(firstStates, "loop")
	if final == nil {
		t.Fatalf("expected loop state, got: %+v", firstStates)
	}
	if final.Status != FlowStatusPostponed {
		t.Errorf("final loop status = %q, want %q", final.Status, FlowStatusPostponed)
	}
	if final.Iteration != 1 {
		t.Errorf("final loop iteration = %d, want 1", final.Iteration)
	}

	// Inspect the persisted row in the stub to confirm iteration survives.
	var loopRow *struct {
		status string
		iter   int64
	}
	for _, fs := range q.snapshotFlowStates() {
		if fs.StepID == "loop" {
			loopRow = &struct {
				status string
				iter   int64
			}{fs.Status, fs.Iteration}
		}
	}
	if loopRow == nil {
		t.Fatal("loop row not persisted")
	}
	if loopRow.status != string(FlowStatusPostponed) {
		t.Errorf("persisted status = %q, want %q", loopRow.status, FlowStatusPostponed)
	}
	if loopRow.iter != 1 {
		t.Errorf("persisted iteration = %d, want 1", loopRow.iter)
	}
}
