package flow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/opencode-ai/opencode/internal/db"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/question"
)

// ---- fixtures --------------------------------------------------------------

func schemaStep(id string, required ...string) Step {
	req := make([]any, 0, len(required))
	props := map[string]any{}
	for _, r := range required {
		req = append(req, r)
		props[r] = map[string]any{"type": "string"}
	}
	return Step{
		ID:     id,
		Prompt: "do " + id,
		Output: &StepOutput{Schema: map[string]any{"type": "object", "required": req, "properties": props}},
	}
}

func reviewStep(id string, required ...string) Step {
	st := schemaStep(id, required...)
	st.Interactive = true
	st.Interaction = &StepInteraction{Target: "${args.reviewer}"}
	return st
}

// startAtSteps is a four-step chain: two plain steps, then two interactive
// review steps — the shape of a run that resumes a reviewed setup at step 3.
func startAtSteps() []Step {
	a := schemaStep("gather", "facts")
	a.Rules = []Rule{{If: "${args.facts} =~ /./", Then: "draft"}}
	b := schemaStep("draft", "draft")
	b.Rules = []Rule{{Then: "review"}}
	c := reviewStep("review", "approved")
	c.Prompt = "review ${args.draft} built from ${args.facts}"
	c.Rules = []Rule{{Then: "confirm"}}
	d := reviewStep("confirm", "confirmed")
	return []Step{a, b, c, d}
}

func continuePrompt() question.Prompt {
	return question.Prompt{
		Question: "Does this look right?",
		Options:  []question.Option{{Label: "Continue"}, {Label: "Change something"}},
	}
}

// questionFactory hands out the scripted agent and a live question service.
type questionFactory struct {
	scriptedAgentFactory
	questions question.Service
}

func (f *questionFactory) QuestionService() question.Service { return f.questions }

// answerer replies to every question asked on the service with the next
// scripted answer, and records what was asked.
type answerer struct {
	mu      sync.Mutex
	asked   []question.Request
	answers [][][]string
}

func (a *answerer) run(ctx context.Context, svc question.Service) {
	sub := svc.Subscribe(ctx)
	go func() {
		for ev := range sub {
			if ev.Type != pubsub.CreatedEvent {
				continue
			}
			a.mu.Lock()
			idx := len(a.asked)
			a.asked = append(a.asked, ev.Payload)
			var reply [][]string
			if idx < len(a.answers) {
				reply = a.answers[idx]
			}
			a.mu.Unlock()
			if reply == nil {
				_ = svc.Reject(ev.Payload.ID)
				continue
			}
			_ = svc.Reply(ev.Payload.ID, reply)
		}
	}()
}

func (a *answerer) snapshot() []question.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]question.Request(nil), a.asked...)
}

type startAtRun struct {
	agent   *stubAgent
	asked   []question.Request
	states  []*FlowState
	querier *stubQuerier
	err     error
}

func runStartAt(t *testing.T, flowID string, steps []Step, querier *stubQuerier, opts RunOptions, responses []agentpkg.AgentEvent, answers [][][]string) startAtRun {
	t.Helper()
	registerTestFlow(t, Flow{ID: flowID, Name: flowID, Spec: FlowSpec{Steps: steps}})

	base := &stubAgent{Broker: pubsub.NewBroker[agentpkg.AgentEvent](), responses: responses}
	qs := question.NewService()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ans := &answerer{answers: answers}
	ans.run(ctx, qs)

	factory := &questionFactory{scriptedAgentFactory: scriptedAgentFactory{agent: &scriptedAgent{stubAgent: base}}, questions: qs}
	svc := NewService(&stubSessions{}, &stubMessages{}, querier, newInteractivePermissions(), factory)
	svc.(*service).SetInteractiveHook(newRecordingHook())

	agentEvents, flowStates, err := svc.RunWithOptions(context.Background(), "prefix", flowID, copyArgs(reviewerArgs), opts)
	if err != nil {
		return startAtRun{agent: base, querier: querier, err: err}
	}
	states := drainFlow(t, agentEvents, flowStates)
	return startAtRun{agent: base, asked: ans.snapshot(), states: states, querier: querier}
}

func completedOutput(states []*FlowState, stepID string) (*FlowState, bool) {
	for _, s := range states {
		if s.StepID == stepID && s.Status == FlowStatusCompleted {
			return s, true
		}
	}
	return nil, false
}

// ---- planStart -------------------------------------------------------------

func TestPlanStart_Refusals(t *testing.T) {
	f := &Flow{ID: "plan", Spec: FlowSpec{Steps: startAtSteps()}}
	done := map[string]any{
		"gather": map[string]any{"facts": "f1"},
		"draft":  map[string]any{"draft": "d1"},
	}
	present := func(ps PresentedStep) map[string]PresentedStep { return map[string]PresentedStep{"confirm": ps} }
	okPresent := PresentedStep{Output: map[string]any{"confirmed": true}, Question: continuePrompt(), Accept: "Continue"}

	tests := []struct {
		name string
		opts RunOptions
		want string
	}{
		{"unknown start step", RunOptions{StartAt: "nope", DoneSteps: done}, `unknown step "nope"`},
		{"done steps without start", RunOptions{DoneSteps: done}, "doneSteps needs startAt"},
		{"start with recover running", RunOptions{StartAt: "review", DoneSteps: done, RecoverRunning: true}, "recoverRunning"},
		{"missing earlier step", RunOptions{StartAt: "review", DoneSteps: map[string]any{"gather": done["gather"]}}, `step "draft" runs before "review"`},
		{"start step also done", RunOptions{StartAt: "draft", DoneSteps: done}, `cannot also be in doneSteps`},
		{"done step off the path", RunOptions{StartAt: "draft", DoneSteps: map[string]any{"gather": done["gather"], "review": map[string]any{"approved": "y"}}}, `"review" is not on the path`},
		{"missing required field", RunOptions{StartAt: "review", DoneSteps: map[string]any{"gather": map[string]any{"facts": "f"}, "draft": map[string]any{}}}, "missing required field(s): draft"},
		{"text for a schema step", RunOptions{StartAt: "review", DoneSteps: map[string]any{"gather": "facts as text", "draft": done["draft"]}}, "must be a JSON object"},
		{"output routes nowhere", RunOptions{StartAt: "review", DoneSteps: map[string]any{"gather": map[string]any{"facts": ""}, "draft": done["draft"]}}, `never route to "review"`},
		{"present a non-interactive step", RunOptions{PresentSteps: map[string]PresentedStep{"draft": {Output: map[string]any{"draft": "d"}, Question: continuePrompt(), Accept: "Continue"}}}, "not interactive"},
		{"accept is not an option", RunOptions{PresentSteps: present(PresentedStep{Output: okPresent.Output, Question: continuePrompt(), Accept: "Yes"})}, "accept must name"},
		{"question text missing", RunOptions{PresentSteps: present(PresentedStep{Output: okPresent.Output, Question: question.Prompt{Options: continuePrompt().Options}, Accept: "Continue"})}, "question text is required"},
		{"step both done and presented", RunOptions{StartAt: "confirm", DoneSteps: map[string]any{"gather": done["gather"], "draft": done["draft"], "review": map[string]any{"approved": "y"}, "confirm": map[string]any{"confirmed": "y"}}, PresentSteps: present(okPresent)}, "both doneSteps and presentSteps"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := planStart(f, map[string]any{}, tt.opts)
			if err == nil {
				t.Fatalf("planStart() = nil error, want %q", tt.want)
			}
			if !errors.Is(err, ErrInvalidStartOptions) {
				t.Errorf("error %v does not wrap ErrInvalidStartOptions", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestPlanStart_MergesDoneOutputsInFlowOrder(t *testing.T) {
	f := &Flow{ID: "plan", Spec: FlowSpec{Steps: startAtSteps()}}
	plan, err := planStart(f, map[string]any{"seed": "s"}, RunOptions{
		StartAt: "review",
		DoneSteps: map[string]any{
			"draft":  map[string]any{"draft": "d1"},
			"gather": map[string]any{"facts": "f1"},
		},
	})
	if err != nil {
		t.Fatalf("planStart() error: %v", err)
	}
	if len(plan.carried) != 2 || plan.carried[0].step.ID != "gather" || plan.carried[1].step.ID != "draft" {
		t.Fatalf("carried order = %+v, want gather then draft", plan.carried)
	}
	if _, leaked := plan.carried[0].args["facts"]; leaked {
		t.Errorf("gather's recorded args must be its input args, got %v", plan.carried[0].args)
	}
	if plan.carried[1].args["facts"] != "f1" {
		t.Errorf("draft's recorded args must carry gather's output, got %v", plan.carried[1].args)
	}
	for k, want := range map[string]any{"seed": "s", "facts": "f1", "draft": "d1"} {
		if plan.args[k] != want {
			t.Errorf("start args[%q] = %v, want %v", k, plan.args[k], want)
		}
	}
	if plan.from == nil || plan.from.step.ID != "draft" {
		t.Errorf("start step's predecessor = %+v, want draft", plan.from)
	}
}

func TestPlanStart_NoOptionsIsNoPlan(t *testing.T) {
	f := &Flow{ID: "plan", Spec: FlowSpec{Steps: startAtSteps()}}
	plan, err := planStart(f, nil, RunOptions{Fresh: true})
	if err != nil || plan != nil {
		t.Fatalf("planStart() = %v, %v; want nil, nil", plan, err)
	}
}

// ---- runs ------------------------------------------------------------------

// TestStartAt_ParksOnTheStartStepAndPresentsALaterOne is the end-to-end shape:
// a run started at step 3 with outputs for steps 1-2 records them as done with
// no agent call, runs step 3 with their outputs in its args, then shows step
// 4's supplied output for review and, on Continue, completes it with no model
// call.
func TestStartAt_ParksOnTheStartStepAndPresentsALaterOne(t *testing.T) {
	querier := &stubQuerier{}
	run := runStartAt(t, "start-at-e2e", startAtSteps(), querier, RunOptions{
		StartAt: "review",
		DoneSteps: map[string]any{
			"gather": map[string]any{"facts": "f1"},
			"draft":  map[string]any{"draft": "d1"},
		},
		PresentSteps: map[string]PresentedStep{
			"confirm": {Output: map[string]any{"confirmed": "yes"}, Question: continuePrompt(), Accept: "Continue"},
		},
	}, []agentpkg.AgentEvent{structEvent(`{"approved":"yes"}`)}, [][][]string{{{"Continue"}}})
	if run.err != nil {
		t.Fatalf("RunWithOptions() error: %v", run.err)
	}

	if got := run.agent.callCount(); got != 1 {
		t.Fatalf("agent calls = %d, want 1 (the start step only)", got)
	}
	prompt := run.agent.snapshotPrompts()[0]
	if !strings.Contains(prompt, "review d1 built from f1") {
		t.Errorf("start step prompt = %q, want the done steps' outputs substituted", prompt)
	}

	for step, want := range map[string]string{"gather": `{"facts":"f1"}`, "draft": `{"draft":"d1"}`, "confirm": `{"confirmed":"yes"}`} {
		st, ok := completedOutput(run.states, step)
		if !ok {
			t.Fatalf("step %q never published completed", step)
		}
		if st.Output != want || !st.IsStructOutput {
			t.Errorf("step %q output = %q (struct=%v), want %q as struct output", step, st.Output, st.IsStructOutput, want)
		}
	}
	for _, s := range run.states {
		if (s.StepID == "gather" || s.StepID == "draft") && s.Status != FlowStatusCompleted {
			t.Errorf("done step %q published %q; a done step must only ever be completed", s.StepID, s.Status)
		}
	}

	asked := run.asked
	if len(asked) != 1 || asked[0].Questions[0].Question != "Does this look right?" {
		t.Fatalf("questions asked = %+v, want the presented question once", asked)
	}
	if asked[0].SessionID != "prefix-start-at-e2e-confirm" {
		t.Errorf("presented question asked on session %q, want the step's own", asked[0].SessionID)
	}

	rows := map[string]db.FlowState{}
	for _, r := range querier.snapshotFlowStates() {
		rows[r.StepID] = r
	}
	for _, step := range []string{"gather", "draft", "review", "confirm"} {
		if rows[step].Status != string(FlowStatusCompleted) {
			t.Errorf("row %q status = %q, want completed", step, rows[step].Status)
		}
	}
	if rows["draft"].RootSessionID != "prefix-start-at-e2e-gather" {
		t.Errorf("done row root session = %q, want the flow root", rows["draft"].RootSessionID)
	}
}

// TestStartAt_PresentedOutputNotAccepted runs the agent with what was shown
// and what the person answered.
func TestStartAt_PresentedOutputNotAccepted(t *testing.T) {
	steps := []Step{reviewStep("confirm", "confirmed")}
	run := runStartAt(t, "present-revise", steps, &stubQuerier{}, RunOptions{
		PresentSteps: map[string]PresentedStep{
			"confirm": {Output: map[string]any{"confirmed": "draft-1"}, Question: continuePrompt(), Accept: "Continue"},
		},
	}, []agentpkg.AgentEvent{structEvent(`{"confirmed":"draft-2"}`)}, [][][]string{{{"Rename the second reason"}}})
	if run.err != nil {
		t.Fatalf("RunWithOptions() error: %v", run.err)
	}
	if got := run.agent.callCount(); got != 1 {
		t.Fatalf("agent calls = %d, want 1", got)
	}
	prompt := run.agent.snapshotPrompts()[0]
	for _, want := range []string{`{"confirmed":"draft-1"}`, "Does this look right?", "Rename the second reason", "do confirm"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("revision prompt missing %q:\n%s", want, prompt)
		}
	}
	st, ok := completedOutput(run.states, "confirm")
	if !ok || st.Output != `{"confirmed":"draft-2"}` {
		t.Fatalf("confirm completed = %+v, want the agent's own output", st)
	}
}

// TestStartAt_PresentedQuestionDismissed treats a dismissal like any other
// non-accepting answer: the agent runs.
func TestStartAt_PresentedQuestionDismissed(t *testing.T) {
	steps := []Step{reviewStep("confirm", "confirmed")}
	run := runStartAt(t, "present-dismissed", steps, &stubQuerier{}, RunOptions{
		PresentSteps: map[string]PresentedStep{
			"confirm": {Output: map[string]any{"confirmed": "draft-1"}, Question: continuePrompt(), Accept: "Continue"},
		},
	}, []agentpkg.AgentEvent{structEvent(`{"confirmed":"draft-2"}`)}, nil)
	if run.err != nil {
		t.Fatalf("RunWithOptions() error: %v", run.err)
	}
	if got := run.agent.callCount(); got != 1 {
		t.Fatalf("agent calls = %d, want 1", got)
	}
	if prompt := run.agent.snapshotPrompts()[0]; !strings.Contains(prompt, "dismissed the question") {
		t.Errorf("revision prompt does not say the question was dismissed:\n%s", prompt)
	}
}

// TestStartAt_PresentationIsTakenOnce: a self-looping presented step shows the
// supplied output on its first entry only.
func TestStartAt_PresentationIsTakenOnce(t *testing.T) {
	st := reviewStep("confirm", "confirmed")
	st.Rules = []Rule{{If: "${step.iteration} == 1", Then: "confirm"}}
	run := runStartAt(t, "present-once", []Step{st}, &stubQuerier{}, RunOptions{
		PresentSteps: map[string]PresentedStep{
			"confirm": {Output: map[string]any{"confirmed": "yes"}, Question: continuePrompt(), Accept: "Continue"},
		},
	}, []agentpkg.AgentEvent{structEvent(`{"confirmed":"again"}`)}, [][][]string{{{"Continue"}}})
	if run.err != nil {
		t.Fatalf("RunWithOptions() error: %v", run.err)
	}
	if got := run.agent.callCount(); got != 1 {
		t.Fatalf("agent calls = %d, want 1 (iteration 2 only)", got)
	}
	if len(run.asked) != 1 {
		t.Fatalf("presented question asked %d times, want once", len(run.asked))
	}
	if got := countCompletedByStepID(run.states, "confirm"); got != 2 {
		t.Errorf("confirm completed %d times, want 2", got)
	}
}

// TestStartAt_RefusedOverResumableWork: a start-at run must not land on top of
// work a resume would continue; fresh discards that work and lets it start.
func TestStartAt_RefusedOverResumableWork(t *testing.T) {
	opts := RunOptions{
		StartAt: "review",
		DoneSteps: map[string]any{
			"gather": map[string]any{"facts": "f1"},
			"draft":  map[string]any{"draft": "d1"},
		},
	}
	parked := func() *stubQuerier {
		return &stubQuerier{flowStates: []db.FlowState{{
			SessionID: "prefix-start-at-resumable-draft", RootSessionID: "prefix-start-at-resumable-gather",
			FlowID: "start-at-resumable", StepID: "draft", Status: string(FlowStatusPostponed), Iteration: 1,
		}}}
	}

	run := runStartAt(t, "start-at-resumable", startAtSteps(), parked(), opts, nil, nil)
	if !errors.Is(run.err, ErrInvalidStartOptions) || !strings.Contains(run.err.Error(), "work to resume") {
		t.Fatalf("RunWithOptions() error = %v, want a refusal over resumable work", run.err)
	}
	if got := len(run.querier.snapshotFlowStates()); got != 1 {
		t.Errorf("refused run wrote rows: %d rows, want the 1 it found", got)
	}

	opts.Fresh = true
	run = runStartAt(t, "start-at-resumable", startAtSteps(), parked(), opts,
		[]agentpkg.AgentEvent{structEvent(`{"approved":"yes"}`), structEvent(`{"confirmed":"yes"}`)}, [][][]string{})
	if run.err != nil {
		t.Fatalf("fresh RunWithOptions() error: %v", run.err)
	}
	if got := run.agent.callCount(); got != 2 {
		t.Errorf("agent calls = %d, want 2 (review, confirm)", got)
	}
}

// TestStartAt_LaterResumeWalksPastDoneSteps: the done rows a start-at run
// records are what a later invocation resumes over. The start step parks; the
// next plain invocation resumes it and never runs the done steps.
func TestStartAt_LaterResumeWalksPastDoneSteps(t *testing.T) {
	steps := startAtSteps()
	steps[2].Rules = []Rule{
		{If: "${args.approved} != yes", Then: "review", Postpone: true},
		{If: "${args.approved} == yes", Then: "confirm"},
	}
	querier := &stubQuerier{}
	run := runStartAt(t, "start-at-resume", steps, querier, RunOptions{
		StartAt: "review",
		DoneSteps: map[string]any{
			"gather": map[string]any{"facts": "f1"},
			"draft":  map[string]any{"draft": "d1"},
		},
	}, []agentpkg.AgentEvent{structEvent(`{"approved":"later"}`)}, nil)
	if run.err != nil {
		t.Fatalf("first run error: %v", run.err)
	}
	if latest := findLatestByStepID(run.states, "review"); latest == nil || latest.Status != FlowStatusPostponed {
		t.Fatalf("review latest = %+v, want postponed", latest)
	}

	resumed := runStartAt(t, "start-at-resume", steps, querier, RunOptions{},
		[]agentpkg.AgentEvent{structEvent(`{"approved":"yes"}`), structEvent(`{"confirmed":"yes"}`)}, nil)
	if resumed.err != nil {
		t.Fatalf("resume error: %v", resumed.err)
	}
	prompts := resumed.agent.snapshotPrompts()
	if len(prompts) != 2 {
		t.Fatalf("resume agent calls = %d (%q), want 2 (review, confirm)", len(prompts), prompts)
	}
	if !strings.Contains(prompts[0], "review d1 built from f1") {
		t.Errorf("resumed review prompt = %q, want the done outputs in its args", prompts[0])
	}
	if _, ok := completedOutput(resumed.states, "confirm"); !ok {
		t.Error("resumed run never completed confirm")
	}
}

// TestStartAt_PresentNeedsQuestionService: a presentation with nobody to ask
// is refused before anything runs.
func TestStartAt_PresentNeedsQuestionService(t *testing.T) {
	registerTestFlow(t, Flow{ID: "present-noq", Spec: FlowSpec{Steps: []Step{reviewStep("confirm", "confirmed")}}})
	svc := NewService(&stubSessions{}, &stubMessages{}, &stubQuerier{}, newInteractivePermissions(), &stubAgentFactory{})
	_, _, err := svc.RunWithOptions(context.Background(), "prefix", "present-noq", copyArgs(reviewerArgs), RunOptions{
		PresentSteps: map[string]PresentedStep{
			"confirm": {Output: map[string]any{"confirmed": "yes"}, Question: continuePrompt(), Accept: "Continue"},
		},
	})
	if !errors.Is(err, ErrInvalidStartOptions) || !strings.Contains(err.Error(), "question service") {
		t.Fatalf("RunWithOptions() error = %v, want the missing question service refusal", err)
	}
}

func TestNormalizeSuppliedOutput_TextForSchemalessStep(t *testing.T) {
	out, err := normalizeSuppliedOutput(Step{ID: "notes"}, "free text")
	if err != nil || out.isStruct || out.text != "free text" {
		t.Fatalf("normalizeSuppliedOutput() = %+v, %v; want text output", out, err)
	}
	if _, err := normalizeSuppliedOutput(Step{ID: "notes"}, 42); err == nil {
		t.Fatal("a number must be refused")
	}
}
