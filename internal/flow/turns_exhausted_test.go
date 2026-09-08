package flow

import (
	"context"
	"errors"
	"testing"

	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// exhaustedDocEvent is a run the agent runtime cut off at its turn budget: the
// forced max-turns wrap-up produced a real struct_output document, but the work
// behind it is unfinished. This is the exact shape job c1f9c6506caed6cd ended
// with (GENAI-296).
func exhaustedDocEvent(content string) agentpkg.AgentEvent {
	return agentpkg.AgentEvent{
		Type:           agentpkg.AgentEventTypeResponse,
		Message:        message.Message{Role: message.Assistant},
		StructOutput:   &message.ToolResult{Name: "struct_output", Content: content},
		TurnsExhausted: true,
	}
}

// exhaustionStep is a working step whose fallback opts into treating turn
// exhaustion as a failure.
func exhaustionStep(id string, retry int, to string) Step {
	return Step{
		ID:       id,
		Prompt:   "implement the plan",
		MaxTurns: 200,
		Fallback: &Fallback{Retry: retry, To: to, OnTurnsExhausted: OnTurnsExhaustedFail},
	}
}

// countRuns counts the stub agent's calls for ONE step's prompt. The stub
// serves every step in the flow, so a bare callCount() would fold the fallback
// step's own run into the number of attempts under test.
func countRuns(ag *stubAgent, promptSubstr string) int {
	n := 0
	for _, p := range ag.snapshotPrompts() {
		if containsSubstring(p, promptSubstr) {
			n++
		}
	}
	return n
}

// stateByStepStatus returns the FIRST state a step published with the given
// status. Used to read a step's inbound args from its `running` transition,
// which is emitted on entry and so cannot have been influenced by the step's
// own run — the only way to distinguish inherited args from self-merged ones.
func stateByStepStatus(states []*FlowState, stepID string, status FlowStatus) *FlowState {
	for _, s := range states {
		if s.StepID == stepID && s.Status == status {
			return s
		}
	}
	return nil
}

func terminalState(states []*FlowState, stepID string) *FlowState {
	var found *FlowState
	for _, s := range states {
		if s.StepID != stepID {
			continue
		}
		if s.Status == FlowStatusCompleted || s.Status == FlowStatusFailed {
			found = s
		}
	}
	return found
}

func runExhaustionFlow(t *testing.T, flowID string, steps []Step, responses []agentpkg.AgentEvent) ([]*FlowState, *stubAgent) {
	t.Helper()
	registerTestFlow(t, Flow{ID: flowID, Name: flowID, Spec: FlowSpec{Steps: steps}})
	ag := &stubAgent{Broker: pubsub.NewBroker[agentpkg.AgentEvent](), responses: responses}
	svc := NewService(&stubSessions{}, &stubMessages{}, &stubQuerier{}, &stubPermissions{},
		&stubAgentFactory{agent: ag})
	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", flowID, map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	return drainFlow(t, agentEvents, flowStates), ag
}

// A step that does NOT opt in keeps the historical behaviour: the wrap-up
// document completes the step. This is the compatibility guarantee — every
// existing flow keeps running exactly as before.
func TestTurnsExhausted_AcceptedWithoutOptIn(t *testing.T) {
	steps := []Step{{ID: "implement", Prompt: "go", MaxTurns: 200,
		Fallback: &Fallback{Retry: 1, To: "salvage"}},
		{ID: "salvage", Prompt: "salvage"}}
	states, ag := runExhaustionFlow(t, "exh-accept", steps,
		[]agentpkg.AgentEvent{exhaustedDocEvent(`{"summary":"cut off"}`)})

	got := terminalState(states, "implement")
	if got == nil || got.Status != FlowStatusCompleted {
		t.Fatalf("without the opt-in an exhausted run must still complete the step, got %+v", got)
	}
	if n := ag.callCount(); n != 1 {
		t.Errorf("agent calls = %d, want 1 — no opt-in means no retry", n)
	}
	if terminalState(states, "salvage") != nil {
		t.Error("salvage must not run: the step completed")
	}
}

// The load-bearing fix: one retry re-enters the SAME session with a fresh turn
// budget, so an agent that ran out of turns mid-task gets to finish (push the
// branch, open the MR) instead of losing the working tree with the pod.
func TestTurnsExhausted_RetryRescuesTheStep(t *testing.T) {
	steps := []Step{exhaustionStep("implement", 1, "salvage"), {ID: "salvage", Prompt: "salvage"}}
	states, ag := runExhaustionFlow(t, "exh-retry", steps, []agentpkg.AgentEvent{
		exhaustedDocEvent(`{"summary":"committed locally, not pushed"}`),
		loopRespond(`{"summary":"pushed and MR opened","gitlab_links":["mr/1"]}`),
	})

	got := terminalState(states, "implement")
	if got == nil || got.Status != FlowStatusCompleted {
		t.Fatalf("the retry should have rescued the step, got %+v", got)
	}
	if n := ag.callCount(); n != 2 {
		t.Fatalf("agent calls = %d, want 2 (exhausted run + one retry with a fresh budget)", n)
	}
	if !containsSubstring(got.Output, "pushed and MR opened") {
		t.Errorf("step output should be the retry's document, got %q", got.Output)
	}
	if terminalState(states, "salvage") != nil {
		t.Error("salvage must not run once the retry succeeded")
	}

	// The whole premise of retrying (rather than routing straight to salvage)
	// is that the retry lands on the SAME session — same pod, same working
	// tree — with a FULL turn budget. Both were previously discarded by the
	// stub, so the claim was asserted in tasks.md and nowhere in the tests.
	sessions := ag.snapshotSessionIDs()
	if len(sessions) != 2 || sessions[0] != sessions[1] {
		t.Errorf("retry must re-enter the same session, got %#v", sessions)
	}
	budgets := ag.snapshotMaxTurns()
	if len(budgets) != 2 || budgets[0] != 200 || budgets[1] != 200 {
		t.Errorf("each attempt must get the step's full turn budget, got %#v", budgets)
	}

	// And it must arrive framed as a continuation. Re-sending the original
	// prompt verbatim into a session whose tail is "you hit the turn limit,
	// wrap up now" + the wrap-up document invites the agent to either redo the
	// task or simply restate that document — the latter returns a
	// non-exhausted result and completes the step carrying the very
	// unfinished work this opt-in exists to catch.
	prompts := ag.snapshotPrompts()
	if len(prompts) != 2 {
		t.Fatalf("want 2 prompts, got %d", len(prompts))
	}
	if prompts[0] != "implement the plan" {
		t.Errorf("first attempt must send the step prompt unchanged, got %q", prompts[0])
	}
	if !containsSubstring(prompts[1], "CONTINUE") || !containsSubstring(prompts[1], "ran out of tool turns") {
		t.Errorf("the retry must be framed as a continuation, got %q", prompts[1])
	}
	if !containsSubstring(prompts[1], "implement the plan") {
		t.Errorf("the retry must still carry the original task for its goal and args, got %q", prompts[1])
	}
}

// A step whose FIRST attempt exhausted and whose SECOND died on an unrelated
// error must still be reported as a turn exhaustion — and must still hand the
// cut-off run's document to the salvage step. lastErr alone cannot carry
// either: attempt 2 overwrites it, taking the only copy of the report with it.
func TestTurnsExhausted_SurvivesALaterUnrelatedError(t *testing.T) {
	steps := []Step{exhaustionStep("implement", 1, "salvage"), {ID: "salvage", Prompt: "salvage"}}
	states, _ := runExhaustionFlow(t, "exh-later-err", steps, []agentpkg.AgentEvent{
		exhaustedDocEvent(`{"summary":"3 commits, unpushed","branch":"feat-CD-4956"}`),
		{Type: agentpkg.AgentEventTypeError, Error: errors.New("session is currently processing another request")},
	})

	got := terminalState(states, "implement")
	if got == nil || got.Status != FlowStatusFailed {
		t.Fatalf("want failed, got %+v", got)
	}
	if !containsSubstring(got.Output, "was cut off after exhausting") {
		t.Errorf("the exhaustion must outrank the final attempt's error, got %q", got.Output)
	}
	salvage := stateByStepStatus(states, "salvage", FlowStatusRunning)
	if salvage == nil {
		t.Fatal("salvage step never ran")
	}
	if got, ok := salvage.Args["branch"].(string); !ok || got != "feat-CD-4956" {
		t.Errorf("salvage lost the cut-off run's document to the later error: %#v", salvage.Args)
	}
}

// A schema-bearing step must NOT get the last-ditch forced wrap-up turn once it
// has run out of turns: forcing needs a turn to spend, and — worse — a forced
// wrap-up that succeeds sets lastErr = nil, which would complete the step and
// undo the opt-in entirely. Pins the errTurnsExhausted / `exhausted` guard.
func TestTurnsExhausted_NoForcedWrapUpOnExhaustedFailure(t *testing.T) {
	steps := []Step{{
		ID:       "implement",
		Prompt:   "implement the plan",
		MaxTurns: 200,
		Output:   &StepOutput{Schema: map[string]any{"type": "object"}},
		Fallback: &Fallback{Retry: 0, To: "salvage", OnTurnsExhausted: OnTurnsExhaustedFail},
	}, {ID: "salvage", Prompt: "salvage"}}
	states, ag := runExhaustionFlow(t, "exh-noforce", steps, []agentpkg.AgentEvent{
		exhaustedDocEvent(`{"summary":"cut off"}`),
		loopRespond(`{"salvaged":true}`),
	})

	got := terminalState(states, "implement")
	if got == nil || got.Status != FlowStatusFailed {
		t.Fatalf("want failed, got %+v", got)
	}
	if n := countRuns(ag, "implement the plan"); n != 1 {
		t.Errorf("implement runs = %d, want 1 — no forced wrap-up turn may be issued", n)
	}
	for i, opts := range ag.snapshotRunOpts() {
		if opts.ForceStructOutput {
			t.Errorf("call %d forced struct_output on a turn-exhausted step", i)
		}
	}
}

// Exhaustion must never park a resume_after step. Parking resumes in a fresh
// workspace, and this is by definition a step that left a half-finished tree
// behind — the resume would find it gone, which is the loss the feature exists
// to prevent. The trap is that the opt-in's extra attempts are themselves new
// chances to draw a transient provider error, so classifying on the FINAL
// error alone reopens the door the design believed it had shut.
func TestTurnsExhausted_NeverParksAResumeAfterStep(t *testing.T) {
	resume := "30m"
	steps := []Step{{
		ID:          "implement",
		Prompt:      "implement the plan",
		MaxTurns:    200,
		ResumeAfter: &resume,
		Fallback:    &Fallback{Retry: 1, To: "salvage", OnTurnsExhausted: OnTurnsExhaustedFail},
	}, {ID: "salvage", Prompt: "salvage"}}
	states, _ := runExhaustionFlow(t, "exh-nopark", steps, []agentpkg.AgentEvent{
		exhaustedDocEvent(`{"summary":"unpushed work on the pod"}`),
		{Type: agentpkg.AgentEventTypeError, Error: errors.New("rate limit exceeded, please retry")},
	})

	if parked := stateByStepStatus(states, "implement", FlowStatusPostponed); parked != nil {
		t.Fatalf("a turn-exhausted step must not park for timed resume: %+v", parked)
	}
	got := terminalState(states, "implement")
	if got == nil || got.Status != FlowStatusFailed {
		t.Fatalf("want failed, got %+v", got)
	}
	if stateByStepStatus(states, "salvage", FlowStatusRunning) == nil {
		t.Error("the step must route to its salvage step, which runs on this pod")
	}
}

// The shape with prose but no usable document: a provider that ignored the
// forced tool_choice, or an end_turn text reply. The run's only account of
// what it did is that prose, and it must not be dropped — the failed row's
// output is what the salvage step reads as its previous-step output.
func TestTurnsExhausted_CarriesProseWhenNoDocument(t *testing.T) {
	steps := []Step{{
		ID:       "implement",
		Prompt:   "implement the plan",
		MaxTurns: 200,
		Output:   &StepOutput{Schema: map[string]any{"type": "object"}},
		Fallback: &Fallback{Retry: 0, To: "salvage", OnTurnsExhausted: OnTurnsExhaustedFail},
	}, {ID: "salvage", Prompt: "salvage"}}
	prose := agentpkg.AgentEvent{
		Type: agentpkg.AgentEventTypeResponse,
		Message: message.Message{Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "3 commits on feat-CD-4956; could not push, credentials missing."},
		}},
		TurnsExhausted: true,
	}
	states, _ := runExhaustionFlow(t, "exh-prose", steps,
		[]agentpkg.AgentEvent{prose, loopRespond(`{"salvaged":true}`)})

	got := terminalState(states, "implement")
	if got == nil || got.Status != FlowStatusFailed {
		t.Fatalf("want failed, got %+v", got)
	}
	if !containsSubstring(got.Output, "feat-CD-4956") {
		t.Errorf("the cut-off run's prose must survive into the failure, got %q", got.Output)
	}
}

// Retries spent and still out of turns: the step fails and routes to
// fallback.to. This is the safety net that never fired for CD-4956.
func TestTurnsExhausted_RoutesToFallbackWhenBudgetSpent(t *testing.T) {
	steps := []Step{exhaustionStep("implement", 1, "salvage"), {ID: "salvage", Prompt: "salvage"}}
	states, ag := runExhaustionFlow(t, "exh-route", steps, []agentpkg.AgentEvent{
		exhaustedDocEvent(`{"summary":"still cut off"}`),
		exhaustedDocEvent(`{"summary":"still cut off"}`),
	})

	got := terminalState(states, "implement")
	if got == nil || got.Status != FlowStatusFailed {
		t.Fatalf("an exhausted step with no retries left must fail, got %+v", got)
	}
	if n := countRuns(ag, "implement the plan"); n != 2 {
		t.Errorf("implement runs = %d, want 2 — the retry budget must be spent, not exceeded", n)
	}
	if !containsSubstring(got.Output, "exhausting its 200-turn budget") {
		t.Errorf("failure should name the budget it blew, got %q", got.Output)
	}
	if !containsSubstring(got.Output, "on all 2 attempts") {
		t.Errorf("failure should record the attempts spent, got %q", got.Output)
	}
	if terminalState(states, "salvage") == nil {
		t.Fatal("salvage step never ran — the fallback route is the whole point")
	}
}

// retry: 0 routes immediately, so a step can opt into "exhaustion means run the
// safety net" without paying for a second turn budget first.
func TestTurnsExhausted_ZeroRetryRoutesImmediately(t *testing.T) {
	steps := []Step{exhaustionStep("implement", 0, "salvage"), {ID: "salvage", Prompt: "salvage"}}
	states, ag := runExhaustionFlow(t, "exh-zero", steps,
		[]agentpkg.AgentEvent{exhaustedDocEvent(`{"summary":"cut off"}`)})

	if got := terminalState(states, "implement"); got == nil || got.Status != FlowStatusFailed {
		t.Fatalf("want failed, got %+v", got)
	}
	if n := countRuns(ag, "implement the plan"); n != 1 {
		t.Errorf("implement runs = %d, want 1 — retry: 0 means no extra attempt", n)
	}
	if got := terminalState(states, "salvage"); got == nil {
		t.Fatal("salvage step never ran")
	}
}

// The fallback step must inherit what the cut-off run managed to report, so a
// salvage step knows which repos and links are already in play instead of
// running blind on the inbound args.
//
// Two things make this test non-vacuous, and both are easy to get wrong:
//
//  1. salvage gets its OWN response. stubAgent clamps its response index to
//     the last scripted event, so with a single-entry script salvage would
//     replay the exhausted document and merge it into its own args through the
//     ordinary completion path — the assertion would then pass with the
//     production merge deleted outright (it did).
//  2. the assertion reads salvage's RUNNING state, published on entry before
//     its own run can contribute anything. A terminal-state assertion cannot
//     tell inherited args from args the step merged for itself.
func TestTurnsExhausted_FallbackInheritsWrapUpDocument(t *testing.T) {
	steps := []Step{exhaustionStep("implement", 0, "salvage"), {ID: "salvage", Prompt: "salvage"}}
	states, _ := runExhaustionFlow(t, "exh-args", steps, []agentpkg.AgentEvent{
		exhaustedDocEvent(`{"summary":"3 commits on feat-CD-4956, unpushed","gitlab_links":["mr/7"]}`),
		loopRespond(`{"salvaged":true}`),
	})

	salvage := stateByStepStatus(states, "salvage", FlowStatusRunning)
	if salvage == nil {
		t.Fatal("salvage step never ran")
	}
	if got, ok := salvage.Args["summary"].(string); !ok || !containsSubstring(got, "unpushed") {
		t.Errorf("salvage did not inherit summary from the cut-off run: %#v", salvage.Args["summary"])
	}
	links, ok := salvage.Args["gitlab_links"].([]any)
	if !ok || len(links) != 1 || links[0] != "mr/7" {
		t.Errorf("salvage did not inherit gitlab_links: %#v", salvage.Args["gitlab_links"])
	}

	// The failed step's own published args must NOT carry the merge: that map
	// was handed to the channel and the broker before the merge point, so
	// writing to it is an unsynchronised mutation of a published value.
	impl := stateByStepStatus(states, "implement", FlowStatusFailed)
	if impl == nil {
		t.Fatal("implement never reached failed")
	}
	if _, leaked := impl.Args["summary"]; leaked {
		t.Errorf("the failed step's published args were mutated after publication: %#v", impl.Args)
	}
}

// An exhausted run that produced NO document keeps the richer
// missingStructOutputError path (it carries the agent's last prose), rather
// than being reclassified.
func TestTurnsExhausted_EmptyRunKeepsMissingStructOutputError(t *testing.T) {
	steps := []Step{{
		ID:       "implement",
		Prompt:   "go",
		MaxTurns: 200,
		Output:   &StepOutput{Schema: map[string]any{"type": "object"}},
		Fallback: &Fallback{Retry: 0, To: "salvage", OnTurnsExhausted: OnTurnsExhaustedFail},
	}, {ID: "salvage", Prompt: "salvage"}}
	empty := agentpkg.AgentEvent{
		Type:           agentpkg.AgentEventTypeResponse,
		Message:        message.Message{Role: message.Assistant},
		TurnsExhausted: true,
	}
	states, _ := runExhaustionFlow(t, "exh-empty", steps, []agentpkg.AgentEvent{empty})

	got := terminalState(states, "implement")
	if got == nil || got.Status != FlowStatusFailed {
		t.Fatalf("want failed, got %+v", got)
	}
	if !containsSubstring(got.Output, "expects structured output") {
		t.Errorf("an empty exhausted run should keep the missing-struct_output error, got %q", got.Output)
	}
}

func TestTurnsExhaustedError_Message(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  *turnsExhaustedError
		want string
	}{
		{
			name: "with step budget and one attempt",
			err:  &turnsExhaustedError{StepID: "implement", MaxTurns: 200, Attempts: 1},
			want: `step "implement" was cut off after exhausting its 200-turn budget; its work is incomplete`,
		},
		{
			name: "inherited budget reports no number",
			err:  &turnsExhaustedError{StepID: "implement", Attempts: 1},
			want: `step "implement" was cut off after exhausting its turn budget; its work is incomplete`,
		},
		{
			name: "multiple attempts are recorded",
			err:  &turnsExhaustedError{StepID: "implement", MaxTurns: 200, Attempts: 3},
			want: `step "implement" was cut off after exhausting its 200-turn budget on all 3 attempts; its work is incomplete`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() =\n  %q\nwant\n  %q", got, tt.want)
			}
		})
	}
}

// errTurnsExhausted must recognise both shapes, since both mean "no turn budget
// left to spend" — the premise of the re-prompt and forced-wrap-up exemptions.
func TestErrTurnsExhausted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"turnsExhaustedError", &turnsExhaustedError{StepID: "s"}, true},
		{"wrapped turnsExhaustedError", errors.Join(errors.New("ctx"), &turnsExhaustedError{StepID: "s"}), true},
		{"missing struct output on exhausted run", &missingStructOutputError{StepID: "s", TurnsExhausted: true}, true},
		{"missing struct output within budget", &missingStructOutputError{StepID: "s"}, false},
		{"unrelated", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errTurnsExhausted(tt.err); got != tt.want {
				t.Errorf("errTurnsExhausted() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Turn exhaustion is not a provider blip: it must never be classified transient,
// or a resume_after step would park and auto-resume in a FRESH workspace, which
// is precisely where the half-finished working tree does not exist.
func TestTurnsExhausted_IsNotATransientProviderError(t *testing.T) {
	t.Parallel()
	err := &turnsExhaustedError{StepID: "implement", MaxTurns: 200, Attempts: 2}
	if isTransientProviderError(err) {
		t.Errorf("turn exhaustion must not be transient — parking would resume in a fresh workspace: %q", err.Error())
	}
}

func TestMergeStructOutputIntoArgs(t *testing.T) {
	t.Parallel()
	t.Run("merges top-level fields over existing args", func(t *testing.T) {
		args := map[string]any{"jira_issue_id": "CD-4956", "summary": "old"}
		mergeStructOutputIntoArgs(args, `{"summary":"new","blockers":[]}`)
		if args["summary"] != "new" {
			t.Errorf("summary = %v, want new", args["summary"])
		}
		if args["jira_issue_id"] != "CD-4956" {
			t.Errorf("inbound args must survive: %v", args["jira_issue_id"])
		}
		if _, ok := args["blockers"]; !ok {
			t.Error("blockers not merged")
		}
	})
	t.Run("leaves args untouched on unusable input", func(t *testing.T) {
		for _, doc := range []string{"", "not json", `["a"]`, `null`} {
			args := map[string]any{"k": "v"}
			mergeStructOutputIntoArgs(args, doc)
			if len(args) != 1 || args["k"] != "v" {
				t.Errorf("doc %q mutated args: %#v", doc, args)
			}
		}
	})
	t.Run("nil args is a no-op", func(t *testing.T) {
		mergeStructOutputIntoArgs(nil, `{"a":1}`)
	})
}

func TestValidateFlow_OnTurnsExhausted(t *testing.T) {
	t.Parallel()
	newFlow := func(v string) *Flow {
		return &Flow{ID: "f", Name: "F", Spec: FlowSpec{Steps: []Step{{
			ID:       "implement",
			Prompt:   "go",
			Fallback: &Fallback{Retry: 1, OnTurnsExhausted: v},
		}}}}
	}
	for _, v := range []string{"", OnTurnsExhaustedAccept, OnTurnsExhaustedFail} {
		if err := validateFlow(newFlow(v)); err != nil {
			t.Errorf("on_turns_exhausted %q should be valid, got %v", v, err)
		}
	}
	for _, v := range []string{"retry", "Fail", "true", "nonsense"} {
		err := validateFlow(newFlow(v))
		if !errors.Is(err, ErrInvalidOnTurnsExhausted) {
			t.Errorf("on_turns_exhausted %q should be rejected at load time, got %v", v, err)
		}
	}
}

func TestFailsOnTurnsExhausted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		step Step
		want bool
	}{
		{"no fallback", Step{ID: "s"}, false},
		{"fallback without the key", Step{ID: "s", Fallback: &Fallback{Retry: 2}}, false},
		{"explicit accept", Step{ID: "s", Fallback: &Fallback{OnTurnsExhausted: OnTurnsExhaustedAccept}}, false},
		{"explicit fail", Step{ID: "s", Fallback: &Fallback{OnTurnsExhausted: OnTurnsExhaustedFail}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.step.FailsOnTurnsExhausted(); got != tt.want {
				t.Errorf("FailsOnTurnsExhausted() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTurnsExhaustedOutcome(t *testing.T) {
	t.Parallel()
	step := exhaustionStep("implement", 1, "salvage")

	t.Run("nil when the run was not exhausted", func(t *testing.T) {
		if got := turnsExhaustedOutcome(step, loopRespond(`{"a":1}`), 1); got != nil {
			t.Errorf("want nil, got %v", got)
		}
	})
	t.Run("nil when the step did not opt in", func(t *testing.T) {
		plain := Step{ID: "implement", Fallback: &Fallback{Retry: 1}}
		if got := turnsExhaustedOutcome(plain, exhaustedDocEvent(`{"a":1}`), 1); got != nil {
			t.Errorf("want nil, got %v", got)
		}
	})
	t.Run("carries the wrap-up document", func(t *testing.T) {
		got := turnsExhaustedOutcome(step, exhaustedDocEvent(`{"summary":"cut off"}`), 2)
		if got == nil {
			t.Fatal("want an error, got nil")
		}
		if got.StructOutput != `{"summary":"cut off"}` {
			t.Errorf("StructOutput = %q", got.StructOutput)
		}
		if got.MaxTurns != 200 || got.Attempts != 2 || got.StepID != "implement" {
			t.Errorf("unexpected fields: %+v", got)
		}
	})
}
