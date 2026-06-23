package flow

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
)

// TestHasResumableWork exercises the pure helper directly — exhaustive
// coverage at the function level catches wiring regressions even when
// the integration assertions in TestRunRetrigger_Gating succeed for
// the wrong reason. The helper folds two concerns into one boolean:
// (a) is any row in an in-progress status, and (b) for completed rows,
// do their rules still produce a pending target (self-route or
// forward target not yet terminal). Both branches are covered below.
func TestHasResumableWork(t *testing.T) {
	// Fixture flows are used as the FlowSpec argument. The "linear"
	// flow has a forward DAG with no self-routing; the "selfloop"
	// flow has a step whose rules unconditionally self-route. Each
	// test row picks one based on what it's exercising.
	linearFlow := &Flow{
		ID: "linear",
		Spec: FlowSpec{
			Steps: []Step{
				{ID: "a", Rules: []Rule{{Then: "b"}}},
				{ID: "b"},
			},
		},
	}
	selfloopFlow := &Flow{
		ID: "selfloop",
		Spec: FlowSpec{
			Steps: []Step{
				{ID: "loop", Rules: []Rule{{Then: "loop"}}},
			},
		},
	}
	completedRow := func(stepID string, iteration int) db.FlowState {
		return db.FlowState{
			StepID:    stepID,
			Status:    string(FlowStatusCompleted),
			Args:      sql.NullString{String: `{}`, Valid: true},
			Iteration: int64(iteration),
		}
	}
	statusRow := func(stepID string, status FlowStatus) db.FlowState {
		return db.FlowState{StepID: stepID, Status: string(status), Args: sql.NullString{String: `{}`, Valid: true}, Iteration: 1}
	}

	cases := []struct {
		name            string
		flow            *Flow
		states          []db.FlowState
		resumeOnFailure bool
		wantResumable   bool
	}{
		// In-progress statuses — short-circuit before rule walk.
		{name: "empty_state", flow: linearFlow, states: nil, wantResumable: false},
		{name: "running_short_circuits", flow: linearFlow, states: []db.FlowState{statusRow("b", FlowStatusRunning)}, wantResumable: true},
		{name: "postponed_short_circuits", flow: linearFlow, states: []db.FlowState{statusRow("b", FlowStatusPostponed)}, wantResumable: true},
		{name: "waiting_for_input_short_circuits", flow: linearFlow, states: []db.FlowState{statusRow("b", FlowStatusWaitingForInput)}, wantResumable: true},
		{name: "failed_no_opt_in", flow: linearFlow, states: []db.FlowState{completedRow("a", 1), statusRow("b", FlowStatusFailed)}, resumeOnFailure: false, wantResumable: false},
		{name: "failed_with_opt_in", flow: linearFlow, states: []db.FlowState{completedRow("a", 1), statusRow("b", FlowStatusFailed)}, resumeOnFailure: true, wantResumable: true},

		// Linear DAG — all steps completed, no pending forward work.
		{name: "linear_all_completed", flow: linearFlow, states: []db.FlowState{completedRow("a", 1), completedRow("b", 1)}, wantResumable: false},
		// Linear DAG — step a completed but b never ran (no row): pending forward work.
		{name: "linear_b_never_ran", flow: linearFlow, states: []db.FlowState{completedRow("a", 1)}, wantResumable: true},

		// Self-loop — completed iter 1 with unconditional self-route still has a self-route pending.
		{name: "selfloop_completed_self_route_pending", flow: selfloopFlow, states: []db.FlowState{completedRow("loop", 1)}, wantResumable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hasResumableWork(tc.states, tc.flow, tc.resumeOnFailure)
			if got != tc.wantResumable {
				t.Errorf("hasResumableWork(%v, resumeOnFailure=%v) = %v, want %v",
					statusNames(tc.states), tc.resumeOnFailure, got, tc.wantResumable)
			}
		})
	}
}

// TestHasResumableWork_SelfLoopTerminatedByRule verifies a self-loop
// that terminated by rule evaluation (predicate flipped false) is
// considered terminal — no resume. This is the right behavior for a
// re-trigger after a self-loop completed normally.
func TestHasResumableWork_SelfLoopTerminatedByRule(t *testing.T) {
	flow := &Flow{
		ID: "terminating-selfloop",
		Spec: FlowSpec{
			Steps: []Step{
				{
					ID: "loop",
					Rules: []Rule{
						{If: "${step.iteration} != 3", Then: "loop"},
					},
				},
			},
		},
	}
	// Iteration 3 completed; predicate `!= 3` is false at iter=3.
	row := db.FlowState{
		StepID:    "loop",
		Status:    string(FlowStatusCompleted),
		Args:      sql.NullString{String: `{}`, Valid: true},
		Iteration: 3,
	}
	if hasResumableWork([]db.FlowState{row}, flow, false) {
		t.Error("self-loop that already evaluated to no-route at iter 3 should not be resumable; want restart on re-trigger")
	}
}

// TestHasResumableWork_MixedStatusCombinations covers status priorities
// the single-row matrix in TestHasResumableWork doesn't reach. The gate
// MUST short-circuit on ANY in-progress row regardless of where it sits
// in the row set, and the `failed`-without-opt-in case MUST defer to
// the rule walk on co-existing completed rows.
func TestHasResumableWork_MixedStatusCombinations(t *testing.T) {
	linearFlow := &Flow{
		ID: "mixed",
		Spec: FlowSpec{
			Steps: []Step{
				{ID: "a", Rules: []Rule{{Then: "b"}}},
				{ID: "b", Rules: []Rule{{Then: "c"}}},
				{ID: "c"},
			},
		},
	}
	row := func(stepID string, status FlowStatus) db.FlowState {
		return db.FlowState{
			StepID:    stepID,
			Status:    string(status),
			Args:      sql.NullString{String: `{}`, Valid: true},
			Iteration: 1,
		}
	}

	cases := []struct {
		name            string
		states          []db.FlowState
		resumeOnFailure bool
		want            bool
	}{
		{
			// Postponed after a failed row: the gate must short-circuit
			// on postponed regardless of the failed row preceding it.
			name:            "failed_then_postponed_no_opt_in",
			states:          []db.FlowState{row("a", FlowStatusFailed), row("b", FlowStatusPostponed)},
			resumeOnFailure: false,
			want:            true,
		},
		{
			// Two running rows — short-circuit on the first encountered.
			name:   "multiple_running",
			states: []db.FlowState{row("a", FlowStatusRunning), row("b", FlowStatusRunning)},
			want:   true,
		},
		{
			// Status-driven check must win even if order would otherwise
			// surface a completed row first.
			name:   "completed_then_waiting_for_input",
			states: []db.FlowState{row("a", FlowStatusCompleted), row("b", FlowStatusWaitingForInput)},
			want:   true,
		},
		{
			// `a` completed → rule points at `b`; `b` failed (terminal
			// without opt-in); `c` never ran. The rule walk on `a` sees
			// terminal[b]=true and continues; no completed row produces
			// a non-terminal forward target, so the predicate is false.
			name:            "linear_failed_midchain_no_opt_in_restarts",
			states:          []db.FlowState{row("a", FlowStatusCompleted), row("b", FlowStatusFailed)},
			resumeOnFailure: false,
			want:            false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hasResumableWork(tc.states, linearFlow, tc.resumeOnFailure)
			if got != tc.want {
				t.Errorf("hasResumableWork = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRunRetrigger_GatePlannerMismatchFallsBackToRestart locks in the
// safety net: a self-loop whose predicate depends on a caller arg that
// flipped between runs produces a gate=true / planner=empty mismatch.
// The runtime MUST fall back to restart-from-step-0 so the re-trigger
// actually does something instead of silently closing channels.
func TestRunRetrigger_GatePlannerMismatchFallsBackToRestart(t *testing.T) {
	prefix := "prefix"
	flowID := "retrigger-gate-mismatch"
	// Single-step self-loop whose continuation depends on `args.continue`.
	// At gate time the row's persisted args have `continue: "yes"` so the
	// rule walk reports a pending self-route. At planner time the caller
	// passes `continue: "no"`, so resolveNextSteps returns nothing.
	f := Flow{
		ID:   flowID,
		Name: flowID,
		Spec: FlowSpec{
			Steps: []Step{
				{
					ID:     "step-a",
					Prompt: "prompt-a",
					Rules:  []Rule{{If: `${args.continue} == yes`, Then: "step-a"}},
				},
			},
		},
	}
	f.Spec.Session = FlowSession{Prefix: prefix}
	registerTestFlow(t, f)

	rootSessionID := fmt.Sprintf("%s-%s-step-a", prefix, flowID)
	now := time.Now().Unix()
	q := &stubQuerier{
		flowStates: []db.FlowState{
			{
				SessionID:     rootSessionID,
				RootSessionID: rootSessionID,
				FlowID:        flowID,
				StepID:        "step-a",
				Status:        string(FlowStatusCompleted),
				Args:          sql.NullString{String: `{"continue":"yes"}`, Valid: true}, // row.Args carries the value that made the prior run loop
				Iteration:     2,
				CreatedAt:     now,
				UpdatedAt:     now,
			},
		},
	}
	sessions := &stubSessions{}
	agent := newStubAgent()
	svc := NewService(sessions, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	agentEvents, flowStates, err := svc.Run(context.Background(), prefix, flowID, map[string]any{"continue": "no"}, false)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	for range flowStates {
	}
	for range agentEvents {
	}

	prompts := agent.snapshotPrompts()
	if len(prompts) == 0 {
		t.Fatalf("gate=resume / planner=empty must fall back to restart, but no agent calls happened")
	}
	if prompts[0] != "prompt-a" {
		t.Errorf("fallback restart should re-run step-a from iter 1; first prompt = %q", prompts[0])
	}
	if len(sessions.deletedTreeIDs) != 0 || len(sessions.deletedIDs) != 0 {
		t.Errorf("fallback restart must not delete sessions; got tree=%v id=%v", sessions.deletedTreeIDs, sessions.deletedIDs)
	}
}

// TestHasResumableWork_StaleStepIDIsIgnored verifies that a flow_states
// row for a step that no longer exists in the flow spec (e.g. the
// author renamed `b` → `c` between runs) does not crash the gate and
// is treated as non-contributing — neither short-circuiting via its
// status (only `completed`/`failed` reach this check) nor producing
// pending forward work via its rule walk (no spec to walk).
func TestHasResumableWork_StaleStepIDIsIgnored(t *testing.T) {
	flow := &Flow{
		ID: "renamed-step",
		Spec: FlowSpec{
			Steps: []Step{
				{ID: "a", Rules: []Rule{{Then: "c"}}},
				{ID: "c"},
			},
		},
	}
	completed := func(stepID string) db.FlowState {
		return db.FlowState{
			StepID:    stepID,
			Status:    string(FlowStatusCompleted),
			Args:      sql.NullString{String: `{}`, Valid: true},
			Iteration: 1,
		}
	}
	// Only `a` and the stale `b` row exist. `a`'s rule points at `c`,
	// which has no row → forward target pending → resumable.
	if !hasResumableWork([]db.FlowState{completed("a"), completed("b")}, flow, false) {
		t.Error("renamed downstream step `c` has no row; gate should report resumable forward work")
	}
	// With `c` also completed, every forward target is terminal and the
	// stale `b` row must not flip the gate true on its own.
	if hasResumableWork([]db.FlowState{completed("a"), completed("b"), completed("c")}, flow, false) {
		t.Error("stale step row `b` should not contribute to resumable work when the live DAG is fully terminal")
	}
}

func statusNames(states []db.FlowState) []string {
	out := make([]string, len(states))
	for i, s := range states {
		out[i] = s.Status
	}
	return out
}

// twoStepFlow returns a 2-step flow `step-a` → `step-b` with an
// unconditional routing rule on `step-a`. No output schemas, so the
// default stubAgent text response is sufficient for both steps.
func twoStepFlow(id string) Flow {
	return Flow{
		ID:   id,
		Name: id,
		Spec: FlowSpec{
			Steps: []Step{
				{
					ID:     "step-a",
					Prompt: "prompt-a",
					Rules:  []Rule{{Then: "step-b"}},
				},
				{
					ID:     "step-b",
					Prompt: "prompt-b",
				},
			},
		},
	}
}

// existingState builds a db.FlowState row for a given step in a given
// status. The session ID follows the rootSessionID derivation that
// Run() uses internally: `<prefix>-<flowID>-<stepID>`, with the flow's
// first step also serving as the root.
func existingState(prefix, flowID, stepID string, status FlowStatus, iteration int) db.FlowState {
	rootStepID := "step-a"
	rootSessionID := fmt.Sprintf("%s-%s-%s", prefix, flowID, rootStepID)
	stepSessionID := fmt.Sprintf("%s-%s-%s", prefix, flowID, stepID)
	return db.FlowState{
		SessionID:     stepSessionID,
		RootSessionID: rootSessionID,
		FlowID:        flowID,
		StepID:        stepID,
		Status:        string(status),
		Args:          sql.NullString{String: `{}`, Valid: true},
		Iteration:     int64(iteration),
		CreatedAt:     time.Now().Unix(),
		UpdatedAt:     time.Now().Unix(),
	}
}

// TestRunRetrigger_Gating walks the (existing-state set × resume_on_failure
// × fresh) matrix and asserts which entry path Run() chose for each row.
// Path-taken signals (in priority order):
//   - sessions.deletedTreeIDs non-empty  → fresh=true reset path
//   - agent prompts empty                → hasRunning early-return (resume
//     via channel replay without
//     spawning new step work)
//   - prompts contain "prompt-a"         → restart from step 0 (agent ran A)
//   - prompts start at "prompt-b"        → resume path skipped A and entered B
func TestRunRetrigger_Gating(t *testing.T) {
	cases := []struct {
		name             string
		existing         func(prefix, flowID string) []db.FlowState
		resumeOnFailure  bool
		fresh            bool
		wantPath         string // "restart" | "resume_skip_a" | "resume_running" | "fresh"
		wantPromptPrefix string // expected prefix of the first agent prompt; "" means no agent call
	}{
		{
			name: "all_completed_restarts_from_step_a",
			existing: func(p, f string) []db.FlowState {
				return []db.FlowState{
					existingState(p, f, "step-a", FlowStatusCompleted, 1),
					existingState(p, f, "step-b", FlowStatusCompleted, 1),
				}
			},
			wantPath:         "restart",
			wantPromptPrefix: "prompt-a",
		},
		{
			name: "trailing_failed_restarts_when_resume_on_failure_false",
			existing: func(p, f string) []db.FlowState {
				return []db.FlowState{
					existingState(p, f, "step-a", FlowStatusCompleted, 1),
					existingState(p, f, "step-b", FlowStatusFailed, 1),
				}
			},
			wantPath:         "restart",
			wantPromptPrefix: "prompt-a",
		},
		{
			name: "trailing_failed_resumes_when_resume_on_failure_true",
			existing: func(p, f string) []db.FlowState {
				return []db.FlowState{
					existingState(p, f, "step-a", FlowStatusCompleted, 1),
					existingState(p, f, "step-b", FlowStatusFailed, 1),
				}
			},
			resumeOnFailure:  true,
			wantPath:         "resume_skip_a",
			wantPromptPrefix: "prompt-b",
		},
		{
			name: "trailing_running_returns_existing_states_no_agent_calls",
			existing: func(p, f string) []db.FlowState {
				return []db.FlowState{
					existingState(p, f, "step-a", FlowStatusCompleted, 1),
					existingState(p, f, "step-b", FlowStatusRunning, 1),
				}
			},
			wantPath:         "resume_running",
			wantPromptPrefix: "",
		},
		{
			name: "trailing_postponed_resumes_into_step_b",
			existing: func(p, f string) []db.FlowState {
				return []db.FlowState{
					existingState(p, f, "step-a", FlowStatusCompleted, 1),
					existingState(p, f, "step-b", FlowStatusPostponed, 1),
				}
			},
			wantPath:         "resume_skip_a",
			wantPromptPrefix: "prompt-b",
		},
		{
			name: "trailing_waiting_for_input_resumes_into_step_b",
			existing: func(p, f string) []db.FlowState {
				return []db.FlowState{
					existingState(p, f, "step-a", FlowStatusCompleted, 1),
					existingState(p, f, "step-b", FlowStatusWaitingForInput, 1),
				}
			},
			wantPath:         "resume_skip_a",
			wantPromptPrefix: "prompt-b",
		},
		{
			name: "fresh_true_wipes_everything_and_restarts",
			existing: func(p, f string) []db.FlowState {
				return []db.FlowState{
					existingState(p, f, "step-a", FlowStatusCompleted, 1),
					existingState(p, f, "step-b", FlowStatusCompleted, 1),
				}
			},
			fresh:            true,
			wantPath:         "fresh",
			wantPromptPrefix: "prompt-a",
		},
		{
			name:             "no_prior_state_starts_at_step_a",
			existing:         func(_, _ string) []db.FlowState { return nil },
			wantPath:         "restart",
			wantPromptPrefix: "prompt-a",
		},
	}

	for i, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Each subtest gets its own flow ID so registerTestFlow's
			// cleanup doesn't race with sibling subtests if t.Parallel()
			// is ever added. Subtest-scoped state otherwise.
			flowID := fmt.Sprintf("retrigger-case-%02d", i)
			prefix := "prefix"

			f := twoStepFlow(flowID)
			f.Spec.Session = FlowSession{
				Prefix:          prefix,
				ResumeOnFailure: tc.resumeOnFailure,
			}
			registerTestFlow(t, f)

			q := &stubQuerier{flowStates: tc.existing(prefix, flowID)}
			sessions := &stubSessions{}
			agent := newStubAgent()
			svc := NewService(sessions, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

			agentEvents, flowStates, err := svc.Run(context.Background(), prefix, flowID, map[string]any{}, tc.fresh)
			if err != nil {
				t.Fatalf("Run() error: %v", err)
			}
			// Drain channels so the run completes deterministically.
			for range flowStates {
			}
			for range agentEvents {
			}

			prompts := agent.snapshotPrompts()

			switch tc.wantPath {
			case "fresh":
				if len(q.deletedFlowRootSessions) == 0 {
					t.Errorf("expected DeleteFlowStatesByRootSession on fresh=true, got none")
				}
				if len(sessions.deletedTreeIDs) == 0 {
					t.Errorf("expected sessions.DeleteTree on fresh=true, got none")
				}
				if len(prompts) == 0 || prompts[0] != tc.wantPromptPrefix {
					t.Errorf("fresh path: prompts[0] = %q, want %q (full: %v)", firstOrEmpty(prompts), tc.wantPromptPrefix, prompts)
				}
			case "restart":
				if len(q.deletedFlowRootSessions) != 0 {
					t.Errorf("restart path must not delete flow_states; got %v", q.deletedFlowRootSessions)
				}
				if len(sessions.deletedTreeIDs) != 0 {
					t.Errorf("restart path must not DeleteTree sessions; got %v", sessions.deletedTreeIDs)
				}
				if len(sessions.deletedIDs) != 0 {
					t.Errorf("restart path must not Delete sessions; got %v", sessions.deletedIDs)
				}
				if len(prompts) == 0 || prompts[0] != tc.wantPromptPrefix {
					t.Errorf("restart path: prompts[0] = %q, want %q (full: %v)", firstOrEmpty(prompts), tc.wantPromptPrefix, prompts)
				}
			case "resume_skip_a":
				if len(q.deletedFlowRootSessions) != 0 {
					t.Errorf("resume path must not delete flow_states; got %v", q.deletedFlowRootSessions)
				}
				if len(sessions.deletedTreeIDs) != 0 {
					t.Errorf("resume path must not DeleteTree sessions; got %v", sessions.deletedTreeIDs)
				}
				if len(prompts) == 0 {
					t.Fatalf("resume_skip_a: expected at least one agent prompt, got none")
				}
				if prompts[0] != tc.wantPromptPrefix {
					t.Errorf("resume_skip_a: first prompt = %q, want %q (full: %v)", prompts[0], tc.wantPromptPrefix, prompts)
				}
				// Step A's agent must NOT have been called — its row was
				// already `completed` and was routed via the skip path.
				for _, p := range prompts {
					if p == "prompt-a" {
						t.Errorf("resume_skip_a: step-a's agent should NOT have run, but prompt-a appeared in %v", prompts)
					}
				}
			case "resume_running":
				if len(prompts) != 0 {
					t.Errorf("hasRunning early-return must not call agent; got prompts %v", prompts)
				}
				if len(q.createdFlowStates) != 0 {
					t.Errorf("hasRunning early-return must not create new flow_states; got %d", len(q.createdFlowStates))
				}
				if len(sessions.deletedTreeIDs) != 0 || len(sessions.deletedIDs) != 0 {
					t.Errorf("hasRunning early-return must not delete sessions; got tree=%v id=%v", sessions.deletedTreeIDs, sessions.deletedIDs)
				}
			default:
				t.Fatalf("unknown wantPath %q", tc.wantPath)
			}
		})
	}
}

func firstOrEmpty(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// TestRunRetrigger_PreservesSessionsOnRestart locks in the D4 contract:
// when Run takes the restart-from-step-0 path (every existing row terminal,
// fresh=false), per-step sessions are NOT deleted. The agent invoked at
// step 0 will see whatever messages already exist in that session — the
// "cumulative LLM context" that stable session.prefix is meant to provide.
func TestRunRetrigger_PreservesSessionsOnRestart(t *testing.T) {
	prefix := "prefix"
	flowID := "retrigger-preserve-sessions"
	f := twoStepFlow(flowID)
	f.Spec.Session = FlowSession{Prefix: prefix}
	registerTestFlow(t, f)

	q := &stubQuerier{
		flowStates: []db.FlowState{
			existingState(prefix, flowID, "step-a", FlowStatusCompleted, 1),
			existingState(prefix, flowID, "step-b", FlowStatusCompleted, 1),
		},
	}
	sessions := &stubSessions{}
	agent := newStubAgent()
	svc := NewService(sessions, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	agentEvents, flowStates, err := svc.Run(context.Background(), prefix, flowID, map[string]any{}, false)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	for range flowStates {
	}
	for range agentEvents {
	}

	if len(sessions.deletedTreeIDs) != 0 {
		t.Errorf("restart-on-retrigger must not DeleteTree any session; got %v", sessions.deletedTreeIDs)
	}
	if len(sessions.deletedIDs) != 0 {
		t.Errorf("restart-on-retrigger must not Delete any session; got %v", sessions.deletedIDs)
	}
	if len(q.deletedFlowRootSessions) != 0 {
		t.Errorf("restart-on-retrigger must not call DeleteFlowStatesByRootSession; got %v", q.deletedFlowRootSessions)
	}

	// Sanity: the restart actually ran from step 0 (agent saw both
	// prompts). runStep prepends "Previous step (X) output:\n…" to the
	// next prompt when the prior step emitted non-struct text output,
	// so check by suffix rather than equality.
	prompts := agent.snapshotPrompts()
	if len(prompts) < 2 || prompts[0] != "prompt-a" || !strings.HasSuffix(prompts[1], "prompt-b") {
		t.Errorf("restart-on-retrigger should re-run both steps from step 0; prompts=%v", prompts)
	}
}

// TestRunRetrigger_WakesPostponedStep locks in the D3 contract: a
// re-trigger of a flow whose latest step is `postponed` resumes that
// step in place, with iteration preserved. The re-trigger IS the wake
// signal — the runtime must not treat postponed as terminal and restart
// from step 0.
func TestRunRetrigger_WakesPostponedStep(t *testing.T) {
	prefix := "prefix"
	flowID := "retrigger-wake-postponed"
	f := twoStepFlow(flowID)
	f.Spec.Session = FlowSession{Prefix: prefix}
	registerTestFlow(t, f)

	const postponedIteration = 3
	q := &stubQuerier{
		flowStates: []db.FlowState{
			existingState(prefix, flowID, "step-a", FlowStatusCompleted, 1),
			existingState(prefix, flowID, "step-b", FlowStatusPostponed, postponedIteration),
		},
	}
	sessions := &stubSessions{}
	agent := newStubAgent()
	svc := NewService(sessions, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	agentEvents, flowStates, err := svc.Run(context.Background(), prefix, flowID, map[string]any{}, false)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	collected := []*FlowState{}
	for s := range flowStates {
		collected = append(collected, s)
	}
	for range agentEvents {
	}

	prompts := agent.snapshotPrompts()
	if len(prompts) == 0 {
		t.Fatalf("postponed wake: expected the postponed step's agent to run, got no prompts")
	}
	if prompts[0] != "prompt-b" {
		t.Errorf("postponed wake: first prompt = %q, want %q (step-a should be skipped via cached completed output)", prompts[0], "prompt-b")
	}
	for _, p := range prompts {
		if p == "prompt-a" {
			t.Errorf("postponed wake: step-a should not have re-run; prompts=%v", prompts)
		}
	}

	// The runStep's initial UpdateFlowState should have carried the
	// postponed row's iteration counter (3) forward, not reset to 1.
	// Inspect the final flow_states row for step-b.
	finalB := findStateByStepID(q.snapshotFlowStates(), "step-b")
	if finalB == nil {
		t.Fatalf("postponed wake: no flow_state row found for step-b after run")
	}
	if finalB.Iteration != postponedIteration {
		t.Errorf("postponed wake: step-b final iteration = %d, want %d", finalB.Iteration, postponedIteration)
	}

	// Sanity: at least one of the emitted FlowStates should reference the
	// resumed step-b. The channel-replay shape varies (runStep emits
	// running then completed), so we just check the StepID appears.
	if !anyStateForStep(collected, "step-b") {
		t.Errorf("postponed wake: flowStates channel saw no state for step-b; got %d states", len(collected))
	}
}

func findStateByStepID(states []db.FlowState, stepID string) *db.FlowState {
	for i := range states {
		if states[i].StepID == stepID {
			return &states[i]
		}
	}
	return nil
}

func anyStateForStep(states []*FlowState, stepID string) bool {
	for _, s := range states {
		if s.StepID == stepID {
			return true
		}
	}
	return false
}
