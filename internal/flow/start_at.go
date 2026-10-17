package flow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/question"
)

// ErrInvalidStartOptions wraps every refusal of RunOptions.StartAt,
// RunOptions.DoneSteps or RunOptions.PresentSteps. The run is refused before
// any flow_states row is written, so a bad request leaves no trace behind.
var ErrInvalidStartOptions = errors.New("invalid flow start options")

// PresentedStep is a caller-supplied output an interactive step shows for
// review before any model call. See RunOptions.PresentSteps and the
// flow-start-at-step spec.
type PresentedStep struct {
	// Output is the step's output, in the shape a DoneSteps entry takes: a
	// JSON object for a step that declares output.schema, text otherwise.
	Output any `json:"output"`
	// Question is asked of the step's bound peers in place of the agent's
	// first turn.
	Question question.Prompt `json:"question"`
	// Accept is the option label that accepts Output as the step's result.
	// Any other answer hands the step to its agent as usual.
	Accept string `json:"accept"`
}

// suppliedOutput is a caller-supplied step output normalised to exactly what
// runStep would have persisted had the step's agent produced it.
type suppliedOutput struct {
	text     string         // the persisted output column
	isStruct bool           // persisted IsStructOutput
	data     map[string]any // decoded object, merged into args when isStruct
}

// normalizeSuppliedOutput checks a supplied output against the step it is for.
// A step that declares output.schema takes a JSON object, completed as its
// struct_output would complete it (schema defaults filled, every required field
// present) and stored compacted, as runStep stores a struct_output. A step
// without a schema takes text, the only output its agent can produce.
func normalizeSuppliedOutput(step Step, raw any) (suppliedOutput, error) {
	hasSchema := step.Output != nil && step.Output.Schema != nil
	switch v := raw.(type) {
	case map[string]any:
		if !hasSchema {
			return suppliedOutput{}, fmt.Errorf("step %q declares no output.schema; its supplied output must be a string", step.ID)
		}
		data, missing := tools.CompleteStructOutput(step.Output.Schema, v)
		if len(missing) > 0 {
			return suppliedOutput{}, fmt.Errorf("step %q: supplied output is missing required field(s): %s",
				step.ID, strings.Join(missing, ", "))
		}
		encoded, err := json.Marshal(data)
		if err != nil {
			return suppliedOutput{}, fmt.Errorf("step %q: encoding supplied output: %w", step.ID, err)
		}
		return suppliedOutput{text: string(encoded), isStruct: true, data: data}, nil
	case string:
		if hasSchema {
			return suppliedOutput{}, fmt.Errorf("step %q declares output.schema; its supplied output must be a JSON object", step.ID)
		}
		return suppliedOutput{text: v}, nil
	default:
		return suppliedOutput{}, fmt.Errorf("step %q: supplied output must be a JSON object or a string, got %T", step.ID, raw)
	}
}

// presentation is a validated PresentedStep.
type presentation struct {
	output   suppliedOutput
	question question.Prompt
	accept   string
}

// accepted reports whether the answer to the presented question is exactly
// the accept option. A multi-select answer that also picks another option is
// not an acceptance: the person asked for something besides the output.
func (p presentation) accepted(answers [][]string) bool {
	return len(answers) > 0 && len(answers[0]) == 1 && answers[0][0] == p.accept
}

// carriedStep is one step a start-at run records as done without running it.
type carriedStep struct {
	step Step
	args map[string]any // the args the step is recorded as having run with
	out  suppliedOutput
}

// startPlan is the validated form of a run's start options.
type startPlan struct {
	startAt string
	// carried lists the DoneSteps in the order the flow reaches them.
	carried []carriedStep
	// args are the start step's args: the caller's args with every carried
	// output on its path merged in, as runStep would have merged them.
	args map[string]any
	// from is the carried step whose rules route to the start step; nil when
	// the start step is the flow's first step.
	from *carriedStep
	// presented holds the PresentSteps, keyed by step id.
	presented map[string]presentation
}

func (p *startPlan) startsAtStep() bool { return p != nil && p.startAt != "" }

// planStart validates opts' start options against f and args. It is pure: it
// reads nothing and writes nothing, so a refusal leaves no state behind.
//
// The walk mirrors the resume planner (collectResumableSteps) on purpose. A
// later invocation that resumes this run walks from the first step over the
// rows recorded here, using each step's rules on the recorded output, so the
// supplied outputs must route to the start step exactly as this walk checks.
// Anything the walk would not reach is refused rather than recorded, because a
// stray completed row would be skipped by a later resume as work already done.
func planStart(f *Flow, args map[string]any, opts RunOptions) (*startPlan, error) {
	if opts.StartAt == "" && len(opts.DoneSteps) == 0 && len(opts.PresentSteps) == 0 {
		return nil, nil
	}
	refuse := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidStartOptions, fmt.Sprintf(format, a...))
	}
	if opts.StartAt == "" && len(opts.DoneSteps) > 0 {
		return nil, refuse("doneSteps needs startAt")
	}
	if opts.StartAt != "" && opts.RecoverRunning {
		return nil, refuse("startAt cannot be combined with recoverRunning")
	}

	plan := &startPlan{startAt: opts.StartAt, presented: map[string]presentation{}}

	for id, ps := range opts.PresentSteps {
		step := findStep(f.Spec.Steps, id)
		if step == nil {
			return nil, refuse("presentSteps names unknown step %q", id)
		}
		if !step.Interactive {
			return nil, refuse("presentSteps names step %q, which is not interactive", id)
		}
		if _, done := opts.DoneSteps[id]; done {
			return nil, refuse("step %q is in both doneSteps and presentSteps", id)
		}
		out, err := normalizeSuppliedOutput(*step, ps.Output)
		if err != nil {
			return nil, refuse("presentSteps: %v", err)
		}
		if strings.TrimSpace(ps.Question.Question) == "" {
			return nil, refuse("presentSteps[%q]: question text is required", id)
		}
		acceptFound := false
		for _, o := range ps.Question.Options {
			if o.Label == ps.Accept {
				acceptFound = true
				break
			}
		}
		if ps.Accept == "" || !acceptFound {
			return nil, refuse("presentSteps[%q]: accept must name one of the question's option labels", id)
		}
		plan.presented[id] = presentation{output: out, question: ps.Question, accept: ps.Accept}
	}

	if opts.StartAt == "" {
		return plan, nil
	}
	if findStep(f.Spec.Steps, opts.StartAt) == nil {
		return nil, refuse("startAt names unknown step %q", opts.StartAt)
	}
	if _, done := opts.DoneSteps[opts.StartAt]; done {
		return nil, refuse("startAt step %q cannot also be in doneSteps", opts.StartAt)
	}

	done := make(map[string]suppliedOutput, len(opts.DoneSteps))
	for id, raw := range opts.DoneSteps {
		step := findStep(f.Spec.Steps, id)
		if step == nil {
			return nil, refuse("doneSteps names unknown step %q", id)
		}
		out, err := normalizeSuppliedOutput(*step, raw)
		if err != nil {
			return nil, refuse("doneSteps: %v", err)
		}
		done[id] = out
	}

	visited := map[string]bool{}
	// leads marks the carried steps the walk reached the start step through.
	leads := map[string]bool{}
	found := false
	var walk func(step Step, stepArgs map[string]any, from *carriedStep) (bool, error)
	walk = func(step Step, stepArgs map[string]any, from *carriedStep) (bool, error) {
		if step.ID == opts.StartAt {
			// First arrival wins, as the diamond-convergence guard decides
			// for a real run.
			if !found {
				found = true
				plan.args = copyArgs(stepArgs)
				plan.from = from
			}
			return true, nil
		}
		if visited[step.ID] {
			return leads[step.ID], nil
		}
		visited[step.ID] = true
		out, ok := done[step.ID]
		if !ok {
			return false, refuse("step %q is reached before %q, on its path or on a parallel branch, but has no entry in doneSteps", step.ID, opts.StartAt)
		}
		c := &carriedStep{step: step, args: copyArgs(stepArgs), out: out}
		plan.carried = append(plan.carried, *c)
		after := copyArgs(stepArgs)
		if out.isStruct {
			maps.Copy(after, out.data)
		}
		reaches := false
		for _, next := range resolveNextSteps(step.Rules, f.Spec.Steps, after, map[string]any{"iteration": 1}) {
			if next.postpone {
				return false, refuse("the doneSteps output for %q routes to a postpone of %q instead of on to the next step", step.ID, next.step.ID)
			}
			if next.step.ID == step.ID {
				return false, refuse("the doneSteps output for %q routes back to %q instead of on to the next step", step.ID, step.ID)
			}
			r, err := walk(next.step, after, c)
			if err != nil {
				return false, err
			}
			reaches = reaches || r
		}
		leads[step.ID] = reaches
		return reaches, nil
	}
	// copyArgs(nil) is nil, and the walk merges outputs into what it copies.
	root := copyArgs(args)
	if root == nil {
		root = map[string]any{}
	}
	if _, err := walk(f.Spec.Steps[0], root, nil); err != nil {
		return nil, err
	}
	if !found {
		return nil, refuse("the doneSteps outputs never route to %q", opts.StartAt)
	}
	for id := range done {
		if !visited[id] {
			return nil, refuse("doneSteps entry %q is not on the path to %q", id, opts.StartAt)
		}
	}
	// A done step on a parallel branch that the start step can also route to
	// (a join) would be recorded as completed, and the start step's route to it
	// dropped as a diamond convergence: it would never run after the start.
	afterStart := routeTargets(f.Spec.Steps, opts.StartAt)
	for _, c := range plan.carried {
		if !leads[c.step.ID] && afterStart[c.step.ID] {
			return nil, refuse("doneSteps entry %q is on a branch parallel to %q, which can route to it: recorded as done, it would never run after %q", c.step.ID, opts.StartAt, opts.StartAt)
		}
	}
	return plan, nil
}

// routeTargets returns every step the step stepID can route to through any
// chain of rules or fallbacks, whatever their conditions.
func routeTargets(steps []Step, stepID string) map[string]bool {
	seen := map[string]bool{}
	queue := []string{stepID}
	for len(queue) > 0 {
		step := findStep(steps, queue[0])
		queue = queue[1:]
		if step == nil {
			continue
		}
		targets := make([]string, 0, len(step.Rules)+1)
		for _, r := range step.Rules {
			targets = append(targets, r.Then)
		}
		if step.Fallback != nil && step.Fallback.To != "" {
			targets = append(targets, step.Fallback.To)
		}
		for _, id := range targets {
			if id != "" && !seen[id] {
				seen[id] = true
				queue = append(queue, id)
			}
		}
	}
	return seen
}

// recordCarriedSteps persists each carried step as a completed flow_states row
// and publishes its completed state, exactly as the resume planner publishes a
// completed step it skips. It returns the start step's work. Each carried step
// gets its own session, because a flow_states row references one: a new, empty
// one, or the one an earlier run under the same prefix left (fresh deletes it).
func (s *service) recordCarriedSteps(
	ctx context.Context,
	f *Flow,
	plan *startPlan,
	sessionPrefix, sessionFlowID, rootSessionID string,
	startedSteps *sync.Map,
	flowStates chan<- *FlowState,
) ([]stepWork, error) {
	var from *FlowState
	for _, c := range plan.carried {
		sessionID := fmt.Sprintf("%s-%s-%s", sessionPrefix, sessionFlowID, c.step.ID)
		// No predecessor is passed: a carried step has no transcript to
		// fork from, and nothing is added to its session.
		if _, err := s.resolveSession(ctx, c.step, sessionID, rootSessionID, nil); err != nil {
			return nil, fmt.Errorf("recording done step %q: %w", c.step.ID, err)
		}
		argsJSON, _ := marshalPersistedArgs(c.args)
		output := sql.NullString{String: c.out.text, Valid: c.out.text != ""}
		var updatedAt int64
		if _, getErr := s.querier.GetFlowState(ctx, sessionID); getErr == nil {
			row, err := s.querier.UpdateFlowState(ctx, db.UpdateFlowStateParams{
				Status:         string(FlowStatusCompleted),
				Args:           sql.NullString{String: string(argsJSON), Valid: true},
				Output:         output,
				IsStructOutput: c.out.isStruct,
				Iteration:      1,
				SessionID:      sessionID,
			})
			if err != nil {
				return nil, fmt.Errorf("recording done step %q: %w", c.step.ID, err)
			}
			updatedAt = row.UpdatedAt
		} else {
			row, err := s.querier.CreateFlowState(ctx, db.CreateFlowStateParams{
				SessionID:      sessionID,
				RootSessionID:  rootSessionID,
				FlowID:         f.ID,
				StepID:         c.step.ID,
				Status:         string(FlowStatusCompleted),
				Args:           sql.NullString{String: string(argsJSON), Valid: true},
				Output:         output,
				IsStructOutput: c.out.isStruct,
				Iteration:      1,
			})
			if err != nil {
				return nil, fmt.Errorf("recording done step %q: %w", c.step.ID, err)
			}
			updatedAt = row.UpdatedAt
		}
		if updatedAt == 0 {
			updatedAt = time.Now().Unix()
		}
		state := &FlowState{
			SessionID:      sessionID,
			RootSessionID:  rootSessionID,
			FlowID:         f.ID,
			StepID:         c.step.ID,
			Status:         FlowStatusCompleted,
			Args:           c.args,
			Output:         c.out.text,
			IsStructOutput: c.out.isStruct,
			Iteration:      1,
			UpdatedAt:      updatedAt,
		}
		startedSteps.Store(c.step.ID, true)
		flowStates <- state
		s.Publish(pubsub.UpdatedEvent, *state)
		if plan.from != nil && plan.from.step.ID == c.step.ID {
			from = state
		}
	}
	start := findStep(f.Spec.Steps, plan.startAt)
	logging.Info("Starting flow at a named step",
		"flow", f.ID, "start_at", plan.startAt, "done_steps", len(plan.carried))
	return []stepWork{{step: *start, args: copyArgs(plan.args), prevStep: from, iteration: 1}}, nil
}

// presentationsKey carries a run's PresentSteps on the run context, so
// runStep can find them without widening its signature.
type presentationsKey struct{}

// presentationSet holds a run's presentations. Each one is taken at most
// once: the step's first entry in the run presents it, and any later entry (a
// self-loop, a cycle) runs the agent as usual.
type presentationSet struct {
	mu sync.Mutex
	m  map[string]presentation
}

func withPresentations(ctx context.Context, m map[string]presentation) context.Context {
	if len(m) == 0 {
		return ctx
	}
	cp := make(map[string]presentation, len(m))
	maps.Copy(cp, m)
	return context.WithValue(ctx, presentationsKey{}, &presentationSet{m: cp})
}

func takePresentation(ctx context.Context, stepID string) (presentation, bool) {
	set, _ := ctx.Value(presentationsKey{}).(*presentationSet)
	if set == nil {
		return presentation{}, false
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	p, ok := set.m[stepID]
	if ok {
		delete(set.m, stepID)
	}
	return p, ok
}

// presentedRevisionPrompt is the prompt an agent gets when the person did not
// accept a presented output: the output, the question they were asked about it
// and their answer, then the step's own prompt. The engine posts only the
// question, so the prompt does not claim the person saw the output itself. The
// agent takes the step on from there as usual.
func presentedRevisionPrompt(p presentation, answers [][]string, dismissed bool, prompt string) string {
	answer := "(no answer)"
	switch {
	case dismissed:
		answer = "(they dismissed the question)"
	case len(answers) > 0 && len(answers[0]) > 0:
		answer = strings.Join(answers[0], ", ")
	}
	return fmt.Sprintf(
		"Before this turn, the output below was supplied for this step without running it:\n"+
			"<presented_output>\n%s\n</presented_output>\n"+
			"The person was asked %q about it and answered: %s\n"+
			"Take the step on from their answer.\n\n%s",
		p.output.text, p.question.Question, answer, prompt)
}

// presentedResult is the agent event an accepted presentation stands in for:
// the supplied output as a struct_output, or as the turn's text when the step
// has no output schema. runStep's completion path then treats it exactly as
// it treats an agent's result.
func presentedResult(p presentation) agentpkg.AgentEvent {
	ev := agentpkg.AgentEvent{
		Type:    agentpkg.AgentEventTypeResponse,
		Message: message.Message{Role: message.Assistant},
		Done:    true,
	}
	if p.output.isStruct {
		ev.StructOutput = &message.ToolResult{Name: "struct_output", Content: p.output.text}
	} else {
		ev.Message.Parts = []message.ContentPart{message.TextContent{Text: p.output.text}}
	}
	return ev
}
