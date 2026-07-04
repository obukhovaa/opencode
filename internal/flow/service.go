package flow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/format"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
)

type FlowStatus string

const (
	FlowStatusRunning         FlowStatus = "running"
	FlowStatusCompleted       FlowStatus = "completed"
	FlowStatusFailed          FlowStatus = "failed"
	FlowStatusPostponed       FlowStatus = "postponed"
	FlowStatusWaitingForInput FlowStatus = "waiting_for_input"
)

type FlowState struct {
	SessionID      string
	RootSessionID  string
	FlowID         string
	StepID         string
	Status         FlowStatus
	Args           map[string]any
	Output         string
	IsStructOutput bool
	Iteration      int
	CreatedAt      int64
	UpdatedAt      int64
	// WaitingTarget carries the resolved interaction.target peers when
	// Status == FlowStatusWaitingForInput. It is emitted exactly once per
	// interactive step, immediately after the bridge bind succeeds and
	// before agent.Run begins. Consumers (API runner) translate this into
	// the flow.waiting_for_input SSE event.
	WaitingTarget []bridge.PeerRef
}

// AgentProvider interface removed — use agentpkg.AgentFactory directly.

type Service interface {
	pubsub.Suscriber[FlowState]
	Run(ctx context.Context, sessionPrefix string, flowID string, args map[string]any, fresh bool) (<-chan agentpkg.AgentEvent, <-chan *FlowState, error)
}

type service struct {
	*pubsub.Broker[FlowState]
	sessions    session.Service
	messages    message.Service
	querier     db.QuerierWithTx
	permissions permission.Service
	agents      agentpkg.AgentFactory

	interactiveHook InteractiveHook // nil → uses nopInteractiveHook (fail-fast)
}

// SetInteractiveHook installs the chat-bridge hook used by
// interactive: true steps. cmd/serve.go injects the bridge service's
// implementation; tests and headless flows that never enter interactive
// steps can leave this unset.
func (s *service) SetInteractiveHook(h InteractiveHook) {
	s.interactiveHook = h
}

// interactiveHookOrNop returns the configured hook or a fail-fast stub.
// The flow engine calls this every time an interactive step starts.
func (s *service) interactiveHookOrNop() InteractiveHook {
	if s.interactiveHook != nil {
		return s.interactiveHook
	}
	return nopInteractiveHook{}
}

// SetInteractiveHook on the Service interface (for callers that hold
// only the interface). Forwards to the concrete service. Use this from
// cmd/serve.go where we have a flow.Service interface but need to wire
// the bridge hook.
type InteractiveHookSetter interface {
	SetInteractiveHook(h InteractiveHook)
}

func NewService(
	sessions session.Service,
	messages message.Service,
	querier db.QuerierWithTx,
	permissions permission.Service,
	agents agentpkg.AgentFactory,
) Service {
	return &service{
		Broker:      pubsub.NewBroker[FlowState](),
		sessions:    sessions,
		messages:    messages,
		querier:     querier,
		permissions: permissions,
		agents:      agents,
	}
}

type stepWork struct {
	step      Step
	args      map[string]any
	prevStep  *FlowState
	postpone  bool
	iteration int
}

func (s *service) Run(ctx context.Context, sessionPrefix string, flowID string, args map[string]any, fresh bool) (<-chan agentpkg.AgentEvent, <-chan *FlowState, error) {
	f, err := Get(flowID)
	if err != nil {
		return nil, nil, err
	}
	if f.Disabled {
		return nil, nil, fmt.Errorf("%w: %s", ErrFlowDisabled, flowID)
	}

	if errArgs := validateArgs(args, f.Spec.Args); errArgs != nil {
		return nil, nil, fmt.Errorf("invalid flow args: %w", errArgs)
	}

	if sessionPrefix == "" {
		var prefixErr error
		sessionPrefix, prefixErr = resolveSessionPrefix(f.Spec.Session.Prefix, args)
		if prefixErr != nil {
			return nil, nil, fmt.Errorf("resolving session prefix: %w", prefixErr)
		}
	}
	rootSessionID := fmt.Sprintf("%s-%s-%s", sessionPrefix, flowID, f.Spec.Steps[0].ID)

	existingStates, err := s.querier.ListFlowStatesByRootSession(ctx, rootSessionID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("checking existing flow states: %w", err)
	}

	agentEvents := make(chan agentpkg.AgentEvent, 100)
	flowStates := make(chan *FlowState, 100)

	if fresh {
		if err := s.querier.DeleteFlowStatesByRootSession(ctx, rootSessionID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			logging.Warn("Failed to delete existing flow states", "error", err)
		}
		// Wipe the entire session tree in one shot. The first step is the
		// flow root, so DeleteTree removes every step session that shares
		// its root_session_id (plus FK-cascaded messages, files, recaps).
		if err := s.sessions.DeleteTree(ctx, rootSessionID); err != nil {
			logging.Debug("Could not delete session tree during fresh start", "session_id", rootSessionID, "error", err)
		}
		existingStates = nil
	}

	hasRunning := false
	for _, es := range existingStates {
		if es.Status == string(FlowStatusRunning) {
			hasRunning = true
			break
		}
	}
	if hasRunning {
		go func() {
			defer close(agentEvents)
			defer close(flowStates)
			for _, es := range existingStates {
				state := dbFlowStateToFlowState(es)
				flowStates <- state
				s.Publish(pubsub.UpdatedEvent, *state)
			}
		}()
		return agentEvents, flowStates, nil
	}

	nextSteps := make(chan stepWork, len(f.Spec.Steps))
	var wg sync.WaitGroup
	startedSteps := &sync.Map{}

	// Resume vs restart gate. The discriminating property is whether
	// any existing flow_states row represents work still in flight —
	// crash recovery (`running`), explicit pause (`postponed`,
	// `waiting_for_input`), or opt-in retry-from-failure (`failed` when
	// flow.session.resume_on_failure is true). The gate ALSO walks the
	// rules of completed rows (using their persisted args+iteration) to
	// detect self-loop crash recovery — iter N completed with a
	// self-route but iter N+1's running row never landed. If nothing is
	// in flight and no completed row's rule walk produces a pending
	// target, the prior run terminated cleanly and a re-trigger must
	// re-execute the flow from step 0 against the current world.
	// Per-step sessions are NOT touched here — only `fresh=true` deletes
	// them (handled above). Step sessions persist across the restart so
	// the agent retains cumulative LLM context. See
	// openspec/specs/flow-runtime-resume for the full contract.
	var initialWork []stepWork
	if !fresh && hasResumableWork(existingStates, f, f.Spec.Session.ResumeOnFailure) {
		stateMap := make(map[string]*FlowState, len(existingStates))
		for _, es := range existingStates {
			state := dbFlowStateToFlowState(es)
			stateMap[state.StepID] = state
		}
		visited := make(map[string]bool)
		logging.Info("Resuming flow from previous state",
			"flow", flowID,
			"existing_steps", len(existingStates),
			"resume_on_failure", f.Spec.Session.ResumeOnFailure)
		initialWork = s.collectResumableSteps(ctx, f, f.Spec.Steps[0], copyArgs(args), nil, stateMap, visited, startedSteps, flowStates)
		// Safety net: the gate uses each row's persisted args+iteration,
		// while collectResumableSteps walks rules with the CURRENT caller
		// args. A self-loop whose predicate depends on a caller arg that
		// changed between runs can produce a gate=true / planner=empty
		// mismatch — without this fallback the flow would close its
		// channels with no agent calls, silently no-op'ing the re-trigger.
		// Fall back to restart-from-step-0 to keep the runtime making
		// forward progress instead of returning the cited regression
		// shape ("zero LLM calls on retrigger").
		if len(initialWork) == 0 {
			logging.Warn("Resume planner produced no work; falling back to restart from step 0",
				"flow", flowID,
				"existing_steps", len(existingStates),
				"resume_on_failure", f.Spec.Session.ResumeOnFailure)
			// collectResumableSteps' skip-completed path stored every
			// visited completed step in `startedSteps` so downstream
			// convergence doesn't re-run a cached step. On fallback we
			// want a true restart from step 0, so swap in a fresh map —
			// the scheduler goroutine hasn't started yet and captures
			// `startedSteps` by closure, so reassignment is safe.
			startedSteps = &sync.Map{}
			initialWork = []stepWork{{step: f.Spec.Steps[0], args: copyArgs(args), iteration: 1}}
		}
	} else {
		// `fresh=true` zeroed existingStates above, so a non-empty slice
		// here implies fresh=false: this branch is the re-trigger
		// restart path. First-ever runs (empty slice) skip the log.
		if len(existingStates) > 0 {
			logging.Info("Restarting flow from step 0 (no in-progress state)",
				"flow", flowID,
				"existing_steps", len(existingStates),
				"resume_on_failure", f.Spec.Session.ResumeOnFailure)
		}
		initialWork = []stepWork{{step: f.Spec.Steps[0], args: copyArgs(args), iteration: 1}}
	}

	for _, w := range initialWork {
		stepSessionID := fmt.Sprintf("%s-%s-%s", sessionPrefix, flowID, w.step.ID)
		argsJSON, _ := json.Marshal(w.args)
		if existingFS, getErr := s.querier.GetFlowState(ctx, stepSessionID); getErr == nil {
			output := sql.NullString{}
			isStructOutput := false
			if existingFS.Status == string(FlowStatusPostponed) {
				output = existingFS.Output
				isStructOutput = existingFS.IsStructOutput
			}
			if _, err := s.querier.UpdateFlowState(ctx, db.UpdateFlowStateParams{
				Status:         string(FlowStatusRunning),
				Args:           sql.NullString{String: string(argsJSON), Valid: true},
				Output:         output,
				IsStructOutput: isStructOutput,
				Iteration:      int64(w.iteration),
				SessionID:      stepSessionID,
			}); err != nil {
				logging.Warn("Failed to update initial flow state", "session_id", stepSessionID, "error", err)
			}
		} else {
			if _, err := s.querier.CreateFlowState(ctx, db.CreateFlowStateParams{
				SessionID:      stepSessionID,
				RootSessionID:  rootSessionID,
				FlowID:         f.ID,
				StepID:         w.step.ID,
				Status:         string(FlowStatusRunning),
				Args:           sql.NullString{String: string(argsJSON), Valid: true},
				IsStructOutput: false,
				Iteration:      int64(w.iteration),
			}); err != nil {
				logging.Warn("Failed to create initial flow state", "session_id", stepSessionID, "error", err)
			}
		}
	}

	for _, w := range initialWork {
		wg.Add(1)
		nextSteps <- w
	}

	go func() {
		for work := range nextSteps {
			stepSessionID := fmt.Sprintf("%s-%s-%s", sessionPrefix, flowID, work.step.ID)
			// Diamond-convergence guard: a step scheduled by multiple upstream
			// paths runs at most once per invocation. Self-loops are exempt —
			// they arrive sequentially (only after the prior iteration completes
			// and enqueues the next) so re-entry is safe and intentional.
			isSelfLoop := work.prevStep != nil && work.prevStep.StepID == work.step.ID
			if !isSelfLoop {
				if _, loaded := startedSteps.LoadOrStore(work.step.ID, true); loaded && !work.postpone {
					logging.Debug("Step already started, skipping (diamond convergence)", "step", work.step.ID)
					wg.Done() // balance the Add from sender
					continue
				}
			}

			go func(w stepWork, sessID string) {
				defer wg.Done()
				s.runStep(ctx, f, w.step, sessID, rootSessionID, w.args, w.prevStep, &wg, agentEvents, flowStates, nextSteps, w.postpone, w.iteration)
			}(work, stepSessionID)
		}
	}()

	go func() {
		wg.Wait()
		close(nextSteps)
		close(agentEvents)
		close(flowStates)
	}()

	return agentEvents, flowStates, nil
}

func (s *service) runStep(
	ctx context.Context,
	f *Flow,
	step Step,
	sessionID string,
	rootSessionID string,
	args map[string]any,
	prevState *FlowState,
	wg *sync.WaitGroup,
	agentEvents chan<- agentpkg.AgentEvent,
	flowStates chan<- *FlowState,
	nextSteps chan<- stepWork,
	postpone bool,
	iteration int,
) {
	if iteration < 1 {
		iteration = 1
	}
	stepVars := map[string]any{"iteration": iteration}

	agentID := step.Agent
	if agentID == "" {
		agentID = "coder"
	}

	var outputSchema map[string]any
	if step.Output != nil {
		outputSchema = step.Output.Schema
	}
	// Resolve interaction.target peers BEFORE NewAgent so the system
	// prompt's "## Reviewer details" section can include the mention
	// handle + channel + peerId. The actual bridge bind happens later
	// (after session resolution) — see OnInteractiveStepStart below —
	// but the resolver itself is pure and side-effect-free, so running
	// it twice (once here, once in the bind block) is safe. We could
	// hoist the second call out too, but that would tangle the bind
	// error-path with this one; better to keep both calls explicit.
	var boundPeers []bridge.PeerRef
	if step.Interactive {
		peers, resolveErr := resolveInteractionTarget(step.Interaction, args)
		if resolveErr != nil {
			s.handleStepError(ctx, step, sessionID, rootSessionID, f.ID, args, iteration,
				fmt.Errorf("interactive step %q: %w", step.ID, resolveErr),
				wg, agentEvents, flowStates, nextSteps, f)
			return
		}
		boundPeers = peers
	}
	// Pass step.Interactive so the agent's system prompt gets the
	// multi-turn-friendly variant (see prompt.GetAgentPrompt). The
	// in-memory AgentInfo.Interactive + BoundPeers flow through to
	// prompt-shape selection.
	agentSvc, err := s.agents.NewAgent(ctx, agentID, outputSchema, step.ID, step.Interactive, boundPeers)
	if err != nil {
		s.handleStepError(ctx, step, sessionID, rootSessionID, f.ID, args, iteration, err, wg, agentEvents, flowStates, nextSteps, f)
		return
	}

	sess, err := s.resolveSession(ctx, step, sessionID, rootSessionID, prevState)
	if err != nil {
		s.handleStepError(ctx, step, sessionID, rootSessionID, f.ID, args, iteration, fmt.Errorf("resolving session: %w", err), wg, agentEvents, flowStates, nextSteps, f)
		return
	}

	s.permissions.AutoApproveSession(sess.ID)

	prompt := substituteScoped(step.Prompt, args, stepVars)
	// Expand !`cmd` shell markup in flow prompts (after args substitution so args can parameterize commands)
	if strings.Contains(prompt, "!`") {
		cwd := config.WorkingDirectory()
		prompt = format.ExpandShellMarkup(ctx, prompt, cwd)
		logging.Debug("Flow step prompt after shell markup expansion", "step", step.ID, "prompt_length", len(prompt))
	}
	// NOTE: Structured output referenced via template variables if needed
	if prevState != nil && prevState.Output != "" && !prevState.IsStructOutput {
		prompt = fmt.Sprintf("Previous step (%s) output:\n%s\n\n%s", prevState.StepID, prevState.Output, prompt)
	}

	status := FlowStatusRunning
	if prevState != nil && postpone {
		status = FlowStatusPostponed
	}

	argsJSON, _ := json.Marshal(args)
	existingFS, getErr := s.querier.GetFlowState(ctx, sessionID)
	var updatedAt int64
	if getErr == nil {
		output := sql.NullString{}
		isStructOutput := false
		if status == FlowStatusPostponed {
			output = existingFS.Output
			isStructOutput = existingFS.IsStructOutput
		}
		if state, stateErr := s.querier.UpdateFlowState(ctx, db.UpdateFlowStateParams{
			Status:         string(status),
			Args:           sql.NullString{String: string(argsJSON), Valid: true},
			Output:         output,
			IsStructOutput: isStructOutput,
			Iteration:      int64(iteration),
			SessionID:      sessionID,
		}); stateErr == nil {
			updatedAt = state.UpdatedAt
		} else {
			err = stateErr
		}
	} else {
		if state, stateErr := s.querier.CreateFlowState(ctx, db.CreateFlowStateParams{
			SessionID:      sessionID,
			RootSessionID:  rootSessionID,
			FlowID:         f.ID,
			StepID:         step.ID,
			Status:         string(status),
			Args:           sql.NullString{String: string(argsJSON), Valid: true},
			IsStructOutput: false,
			Iteration:      int64(iteration),
		}); stateErr == nil {
			updatedAt = state.CreatedAt
		} else {
			err = stateErr
		}
	}
	if err != nil {
		s.handleStepError(ctx, step, sessionID, rootSessionID, f.ID, args, iteration, fmt.Errorf("persisting flow state: %w", err), wg, agentEvents, flowStates, nextSteps, f)
		return
	}

	runningState := &FlowState{
		SessionID:     sessionID,
		RootSessionID: rootSessionID,
		FlowID:        f.ID,
		StepID:        step.ID,
		Status:        status,
		Args:          args,
		Iteration:     iteration,
		UpdatedAt:     updatedAt,
	}
	if status == FlowStatusPostponed && getErr == nil {
		runningState.Output = existingFS.Output.String
		runningState.IsStructOutput = existingFS.IsStructOutput
	}
	flowStates <- runningState
	s.Publish(pubsub.UpdatedEvent, *runningState)

	if status == FlowStatusPostponed {
		logging.Info("Step postponed for next execution", "step", step.ID)
		return
	}

	// Inject flow context for downstream telemetry (Langfuse trace naming + metadata)
	ctx = context.WithValue(ctx, tools.FlowIDContextKey, f.ID)
	ctx = context.WithValue(ctx, tools.FlowStepIDContextKey, step.ID)
	ctx = context.WithValue(ctx, tools.FlowStepIterationContextKey, iteration)
	ctx = withFlowArgs(ctx, args)

	// DEBUG: trace interactive step path. Remove after diagnosing
	// flow.waiting_for_input non-emission.
	logging.Info("flow: step pre-interactive check",
		"step", step.ID, "interactive", step.Interactive,
		"has_interaction_block", step.Interaction != nil,
		"target", func() string {
			if step.Interaction != nil {
				return step.Interaction.Target
			}
			return ""
		}(),
		"hook_registered", s.interactiveHook != nil,
	)
	// Interactive bind: per the flow-api spec, on entering an interactive
	// step we resolve interaction.target from args and call the bridge's
	// InteractiveHook BEFORE agent.Run. Failure here fails the step fast.
	// The bind is automatically reversed in deferred Unbind below.
	if step.Interactive {
		// boundPeers was already resolved above (before NewAgent) so the
		// system prompt could include the "## Reviewer details" section.
		// Re-using the slice here keeps the bind call wire-compatible
		// without paying the resolve cost twice.
		if err := s.interactiveHookOrNop().OnInteractiveStepStart(ctx, sess.ID, boundPeers); err != nil {
			s.handleStepError(ctx, step, sessionID, rootSessionID, f.ID, args, iteration,
				fmt.Errorf("interactive step %q bind: %w", step.ID, err),
				wg, agentEvents, flowStates, nextSteps, f)
			return
		}
		// Mark the session as interactively bound so the question tool
		// won't auto-approve away the human's chance to answer (see
		// permission.Service.MarkInteractiveSession + question tool's
		// auto-approve short-circuit guard). Cleared in the deferred
		// unbind below.
		s.permissions.MarkInteractiveSession(sess.ID)
		// Defer unbind so any return path (success, error, panic) unwinds
		// the binding. Use a fresh ctx so a cancelled parent doesn't
		// short-circuit the Unbind call.
		defer func() {
			s.permissions.RemoveInteractiveSession(sess.ID)
			unbindCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.interactiveHookOrNop().OnInteractiveStepComplete(unbindCtx, sess.ID); err != nil {
				logging.Warn("interactive step unbind failed", "step", step.ID, "err", err)
			}
		}()
		// Per the flow-api spec, emit the waiting_for_input transition
		// AFTER the bind succeeds and BEFORE agent.Run. The API runner
		// translates this FlowState into the flow.waiting_for_input SSE
		// event. We DO NOT persist this state — it's an in-flight signal
		// only, not a step terminal status.
		waitingState := &FlowState{
			SessionID:     sess.ID,
			RootSessionID: rootSessionID,
			FlowID:        f.ID,
			StepID:        step.ID,
			Status:        FlowStatusWaitingForInput,
			Args:          args,
			Iteration:     iteration,
			UpdatedAt:     time.Now().Unix(),
			WaitingTarget: boundPeers,
		}
		flowStates <- waitingState
		s.Publish(pubsub.UpdatedEvent, *waitingState)
	}

	var result agentpkg.AgentEvent
	maxAttempts := 1
	retryDelay := 0
	if step.Fallback != nil {
		maxAttempts = 1 + step.Fallback.Retry
		retryDelay = step.Fallback.Delay
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			logging.Info("Retrying step", "step", step.ID, "attempt", attempt+1, "max", maxAttempts)
			if retryDelay > 0 {
				select {
				case <-ctx.Done():
					lastErr = ctx.Err()
					goto doneRetry
				case <-time.After(time.Duration(retryDelay) * time.Second):
				}
			}
		}

		{
			// Flow steps are non-interactive: RunWith holds the turn open
			// at the end of each agentic cycle until pending background
			// tasks (bash run_in_background, task async, monitor) reach
			// terminal state, so the step's struct_output reflects the
			// post-completion state rather than the immediate ack. See
			// openspec/specs/flow-runtime-resume.
			//
			// stepCtx applies the precedence chain:
			//   Step.Timeout > OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT > ctx unwrapped.
			runCtx, cancelStep := stepCtx(ctx, step)
			done, runErr := agentSvc.RunWith(runCtx, sess.ID, prompt, step.MaxTurns, agentpkg.RunOptions{NonInteractive: true})
			if runErr != nil {
				cancelStep()
				lastErr = runErr
				continue
			}

			result = <-done
			cancelStep()
			if result.Type == agentpkg.AgentEventTypeError {
				lastErr = result.Error
				continue
			}

			// Validate output when the step defines an output schema.
			// Two severity levels:
			//  1. Agent produced NOTHING (no struct output AND no text) — treat as
			//     retryable failure. Catches transient model issues (empty API
			//     responses reported as end_turn).
			//  2. Agent produced text but didn't call struct_output — log a warning
			//     but proceed. The text is stored as output and unconditional routing
			//     rules still work. Conditional rules referencing output fields will
			//     evaluate to false (missing key), same as pre-validation behavior.
			if step.Output != nil && (result.StructOutput == nil || result.StructOutput.Content == "") {
				textOutput := result.Message.Content().Text
				if textOutput == "" {
					lastErr = fmt.Errorf("step %q expects structured output but agent produced empty response", step.ID)
					logging.Warn("Empty agent response for step with output schema",
						"step", step.ID,
						"attempt", attempt+1,
						"max_attempts", maxAttempts,
						"finish_reason", result.Message.FinishReason())
					continue
				}
				logging.Warn("Step has output schema but agent returned text instead of struct_output — proceeding with text fallback",
					"step", step.ID,
					"text_length", len(textOutput))
			}

			lastErr = nil
			break
		}
	}
doneRetry:

	if lastErr != nil {
		// When the parent ctx is cancelled (graceful shutdown, ctx-cancelled
		// retry-delay path above) the failure-state UPDATE would also fail
		// the SQL call immediately, leaving the flow_state row stuck on
		// `running` from the entry-time write at line ~425. Subsequent
		// inspection / resume can't then tell apart "agent still working"
		// from "agent gave up". Persist with a fresh deadline so the
		// terminal status lands regardless of how this run is unwinding.
		writeCtx := ctx
		if ctx.Err() != nil {
			var cancelWrite context.CancelFunc
			writeCtx, cancelWrite = context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelWrite()
		}
		if state, updateErr := s.querier.UpdateFlowState(writeCtx, db.UpdateFlowStateParams{
			Status:         string(FlowStatusFailed),
			Args:           sql.NullString{String: string(argsJSON), Valid: true},
			Output:         sql.NullString{String: lastErr.Error(), Valid: true},
			IsStructOutput: false,
			Iteration:      int64(iteration),
			SessionID:      sessionID,
		}); updateErr != nil {
			logging.Warn("Failed to persist step failure state", "session_id", sessionID, "error", updateErr)
			updatedAt = time.Now().Unix()
		} else {
			updatedAt = state.UpdatedAt
		}

		failedState := &FlowState{
			SessionID:     sessionID,
			RootSessionID: rootSessionID,
			FlowID:        f.ID,
			StepID:        step.ID,
			Status:        FlowStatusFailed,
			Args:          args,
			Output:        lastErr.Error(),
			Iteration:     iteration,
			UpdatedAt:     updatedAt,
		}
		flowStates <- failedState
		s.Publish(pubsub.UpdatedEvent, *failedState)

		if step.Fallback != nil && step.Fallback.To != "" {
			fallbackStep := findStep(f.Spec.Steps, step.Fallback.To)
			if fallbackStep != nil {
				wg.Add(1)
				nextSteps <- stepWork{step: *fallbackStep, args: copyArgs(args), prevStep: failedState, iteration: 1}
			}
		}
		return
	}

	var output string
	isStructOutput := false
	if result.StructOutput != nil {
		output = result.StructOutput.Content
		isStructOutput = true
		// Minify
		var buf bytes.Buffer
		if err := json.Compact(&buf, []byte(output)); err == nil {
			output = buf.String()
		}
		var structData map[string]any
		if err := json.Unmarshal([]byte(output), &structData); err == nil {
			maps.Copy(args, structData)
		}
	} else {
		output = result.Message.Content().Text
	}

	// Resolve next steps and pre-check maxIterations BEFORE publishing the
	// completed state. This way a max-iter exhaustion produces a single
	// terminal `failed` event (no `completed → failed` flip on the wire).
	nextResolved := resolveNextSteps(step.Rules, f.Spec.Steps, args, stepVars)
	for _, rs := range nextResolved {
		isSelfRoute := rs.step.ID == step.ID && !rs.postpone
		if isSelfRoute && step.MaxIterations > 0 && iteration+1 > step.MaxIterations {
			lastErr = fmt.Errorf("step %q exceeded maxIterations (%d)", step.ID, step.MaxIterations)
			s.handleStepError(ctx, step, sessionID, rootSessionID, f.ID, args, iteration, lastErr, wg, agentEvents, flowStates, nextSteps, f)
			return
		}
	}

	if state, updateErr := s.querier.UpdateFlowState(ctx, db.UpdateFlowStateParams{
		Status:         string(FlowStatusCompleted),
		Args:           sql.NullString{String: string(argsJSON), Valid: true},
		Output:         sql.NullString{String: output, Valid: output != ""},
		IsStructOutput: isStructOutput,
		Iteration:      int64(iteration),
		SessionID:      sessionID,
	}); updateErr != nil {
		logging.Warn("Failed to persist step completion state", "session_id", sessionID, "error", updateErr)
		updatedAt = time.Now().Unix()
	} else {
		updatedAt = state.UpdatedAt
	}

	completedState := &FlowState{
		SessionID:      sessionID,
		RootSessionID:  rootSessionID,
		FlowID:         f.ID,
		StepID:         step.ID,
		Status:         FlowStatusCompleted,
		Args:           args,
		Output:         output,
		IsStructOutput: isStructOutput,
		Iteration:      iteration,
		UpdatedAt:      updatedAt,
	}
	flowStates <- completedState
	s.Publish(pubsub.UpdatedEvent, *completedState)

	result.FlowStepID = step.ID
	agentEvents <- result

	for _, rs := range nextResolved {
		var nextIteration int
		switch {
		case rs.step.ID == step.ID && !rs.postpone:
			// In-process self-route: bump for the next pass.
			nextIteration = iteration + 1
		case rs.step.ID == step.ID && rs.postpone:
			// Postpone-self: carry iteration so the persisted `postponed`
			// row records the iteration that decided to postpone, and resume
			// continues at that iteration.
			nextIteration = iteration
		default:
			// Different target: a fresh step starts at iteration 1.
			nextIteration = 1
		}
		wg.Add(1)
		nextSteps <- stepWork{step: rs.step, args: copyArgs(args), prevStep: completedState, postpone: rs.postpone, iteration: nextIteration}
	}
}

func (s *service) handleStepError(
	ctx context.Context,
	step Step,
	sessionID string,
	rootSessionID string,
	flowID string,
	args map[string]any,
	iteration int,
	err error,
	wg *sync.WaitGroup,
	agentEvents chan<- agentpkg.AgentEvent,
	flowStates chan<- *FlowState,
	nextSteps chan<- stepWork,
	f *Flow,
) {
	logging.Error("Flow step failed", "step", step.ID, "error", err)

	if iteration < 1 {
		iteration = 1
	}

	argsJSON, _ := json.Marshal(args)
	var updatedAt int64
	if state, updateErr := s.querier.UpdateFlowState(ctx, db.UpdateFlowStateParams{
		Status:         string(FlowStatusFailed),
		Args:           sql.NullString{String: string(argsJSON), Valid: true},
		Output:         sql.NullString{String: err.Error(), Valid: true},
		IsStructOutput: false,
		Iteration:      int64(iteration),
		SessionID:      sessionID,
	}); updateErr != nil {
		logging.Warn("Failed to persist step error state", "session_id", sessionID, "error", updateErr)
		updatedAt = time.Now().Unix()
	} else {
		updatedAt = state.UpdatedAt
	}

	failedState := &FlowState{
		SessionID:     sessionID,
		RootSessionID: rootSessionID,
		FlowID:        flowID,
		StepID:        step.ID,
		Status:        FlowStatusFailed,
		Args:          args,
		Output:        err.Error(),
		Iteration:     iteration,
		UpdatedAt:     updatedAt,
	}
	flowStates <- failedState
	s.Publish(pubsub.UpdatedEvent, *failedState)

	agentEvents <- agentpkg.AgentEvent{
		Type:       agentpkg.AgentEventTypeError,
		Error:      err,
		FlowStepID: step.ID,
	}

	if step.Fallback != nil && step.Fallback.To != "" {
		fallbackStep := findStep(f.Spec.Steps, step.Fallback.To)
		if fallbackStep != nil {
			wg.Add(1)
			// Fallback runs as iteration 1 of the fallback step — distinct
			// step ID, distinct flow_states row.
			nextSteps <- stepWork{step: *fallbackStep, args: copyArgs(args), prevStep: failedState, iteration: 1}
		}
	}
}

// hasResumableWork reports whether Run should enter the resume path
// (collectResumableSteps) for the given existing-states set. A
// re-invocation of Run for the same session prefix resumes when there
// is work still owed to the prior run — either a mid-state status
// (running / postponed / waiting_for_input, or `failed` under per-flow
// opt-in), OR a completed step whose rules still produce a pending
// target: a self-route (next iteration was never scheduled — the
// crash window between writing "iter N completed" and "iter N+1
// running") or a forward route to a step that hasn't reached terminal.
//
// Statuses considered terminal for forward-route checks:
//   - completed
//   - failed (only when resumeOnFailure is false; with resumeOnFailure
//     the function returns true above and never reaches this check)
//
// When all completed rows' rules point only at terminal targets and no
// in-progress row exists, the prior run terminated cleanly and a
// re-trigger restarts from step 0 (with per-step sessions preserved —
// see openspec/specs/flow-runtime-resume requirement D4).
//
// The function is pure (no I/O, no state mutation); it deserializes
// row.Args and row.Output locally to evaluate rule predicates against
// the same args+iteration context collectResumableSteps would use.
func hasResumableWork(states []db.FlowState, f *Flow, resumeOnFailure bool) bool {
	terminal := make(map[string]bool, len(states))
	rowByStep := make(map[string]db.FlowState, len(states))
	for _, st := range states {
		switch st.Status {
		case string(FlowStatusRunning),
			string(FlowStatusPostponed),
			string(FlowStatusWaitingForInput):
			return true
		case string(FlowStatusFailed):
			if resumeOnFailure {
				return true
			}
			terminal[st.StepID] = true
		case string(FlowStatusCompleted):
			terminal[st.StepID] = true
			rowByStep[st.StepID] = st
		default:
			// Schema drift or partial migration: treat as non-terminal so
			// any forward route lands as pending. Log once so the row is
			// discoverable instead of silently steering the gate.
			logging.Warn("Unknown flow_states.status in hasResumableWork",
				"step", st.StepID, "status", st.Status, "flow", f.ID)
		}
	}
	for stepID, row := range rowByStep {
		stepDef := findStep(f.Spec.Steps, stepID)
		if stepDef == nil {
			continue
		}
		for _, ns := range evaluateRowNextSteps(row, stepDef, f.Spec.Steps) {
			if ns.step.ID == stepID {
				// Self-route: the next iteration was never scheduled.
				// collectResumableSteps may still trip MaxIterations when
				// it gets to schedule the next iter — that's fine; the
				// gate predicate just decides whether to enter the resume
				// path at all, not what the resume path produces.
				return true
			}
			if !terminal[ns.step.ID] {
				return true
			}
		}
	}
	return false
}

// evaluateRowNextSteps reconstructs the args+iteration context that
// runStep would have at the END of the given completed row's
// execution, then evaluates the step's routing rules against that
// context. Returns the resolved next-step list — same shape
// collectResumableSteps uses when walking the rule graph on resume.
//
// args at end-of-step = row.Args (the input args persisted on entry)
// merged with row.Output when the row carries struct output, matching
// the merge collectResumableSteps performs at its skip-completed
// emission.
func evaluateRowNextSteps(row db.FlowState, stepDef *Step, allSteps []Step) []resolvedStep {
	var rowArgs map[string]any
	if row.Args.Valid {
		if err := json.Unmarshal([]byte(row.Args.String), &rowArgs); err != nil {
			// Corrupted args JSON: predicates referencing ${args.x} will
			// see absent keys and may produce a different routing than the
			// original run. Surface so the row is observable; the gate
			// still falls through with empty args (safe default).
			logging.Debug("Malformed flow_states.args in evaluateRowNextSteps",
				"step", row.StepID, "session", row.SessionID, "error", err)
		}
	}
	if rowArgs == nil {
		rowArgs = map[string]any{}
	}
	if row.IsStructOutput && row.Output.Valid && row.Output.String != "" {
		var structData map[string]any
		if err := json.Unmarshal([]byte(row.Output.String), &structData); err == nil {
			maps.Copy(rowArgs, structData)
		}
	}
	iter := int(row.Iteration)
	if iter < 1 {
		iter = 1
	}
	stepVars := map[string]any{"iteration": iter}
	return resolveNextSteps(stepDef.Rules, allSteps, rowArgs, stepVars)
}

func (s *service) collectResumableSteps(
	ctx context.Context,
	f *Flow,
	step Step,
	args map[string]any,
	prevState *FlowState,
	stateMap map[string]*FlowState,
	visited map[string]bool,
	startedSteps *sync.Map,
	flowStates chan<- *FlowState,
) []stepWork {
	if visited[step.ID] {
		return nil
	}
	visited[step.ID] = true

	existing := stateMap[step.ID]

	if existing == nil || (existing.Status != FlowStatusCompleted && existing.Status != FlowStatusPostponed) {
		if existing != nil {
			logging.Info("Resuming non-completed step", "step", step.ID, "status", existing.Status)
		} else {
			logging.Info("Running step not yet attempted", "step", step.ID)
		}
		stepArgs := args
		iteration := 1
		if existing != nil {
			stepArgs = existing.Args
			if existing.Iteration > 0 {
				iteration = existing.Iteration
			}
		}
		return []stepWork{{step: step, args: copyArgs(stepArgs), prevStep: prevState, iteration: iteration}}
	}

	if existing.Status == FlowStatusPostponed {
		logging.Info("Resuming postponed step", "step", step.ID, "iteration", existing.Iteration)
		iteration := existing.Iteration
		if iteration < 1 {
			iteration = 1
		}
		return []stepWork{{step: step, args: copyArgs(existing.Args), prevStep: existing, iteration: iteration}}
	}

	logging.Debug("Skipping completed step during resume", "step", step.ID)
	startedSteps.Store(step.ID, true)
	flowStates <- existing
	s.Publish(pubsub.UpdatedEvent, *existing)

	if existing.IsStructOutput && existing.Output != "" {
		var structData map[string]any
		if err := json.Unmarshal([]byte(existing.Output), &structData); err == nil {
			maps.Copy(args, structData)
		}
	}

	// Rule evaluation on resume uses the iteration the step actually ran at,
	// so ${step.iteration}-conditional rules behave consistently with the
	// original execution.
	currentIter := existing.Iteration
	if currentIter < 1 {
		currentIter = 1
	}
	stepVars := map[string]any{"iteration": currentIter}

	var result []stepWork
	for _, rs := range resolveNextSteps(step.Rules, f.Spec.Steps, args, stepVars) {
		if rs.step.ID == step.ID && !rs.postpone {
			// In-process self-route from a completed iteration. Recursing
			// would hit the `visited` guard and silently drop the next
			// iteration. Emit the next iteration's stepWork directly so
			// the loop resumes at iter N+1 after a crash between the
			// completed write and the next running write.

			// Honor MaxIterations on resume. In the normal path the cap
			// fires at the end of iter N's runStep, before iter N+1 is
			// scheduled. On resume that pre-check is gone, so we replicate
			// the failure semantics here: overwrite the completed row with
			// a failed status and route to the step's fallback (if any).
			if step.MaxIterations > 0 && currentIter+1 > step.MaxIterations {
				result = append(result, s.failResumedSelfLoop(ctx, step, existing, args, currentIter, flowStates, f)...)
				continue
			}

			result = append(result, stepWork{
				step:      rs.step,
				args:      copyArgs(args),
				prevStep:  existing,
				iteration: currentIter + 1,
			})
			continue
		}
		result = append(result, s.collectResumableSteps(ctx, f, rs.step, copyArgs(args), existing, stateMap, visited, startedSteps, flowStates)...)
	}
	return result
}

// failResumedSelfLoop transitions a previously-completed self-looping step
// to `failed` when, on resume, the cap-tripping next iteration is detected.
// It persists the failed row, emits the failed state event, and returns
// fallback stepWork (if any) so the resume planner can include it in initial
// work. Mirrors handleStepError's outcome without needing access to the
// scheduler channels — collectResumableSteps runs synchronously before any
// scheduler goroutine is spawned.
func (s *service) failResumedSelfLoop(
	ctx context.Context,
	step Step,
	existing *FlowState,
	args map[string]any,
	currentIter int,
	flowStates chan<- *FlowState,
	f *Flow,
) []stepWork {
	capErr := fmt.Errorf("step %q exceeded maxIterations (%d)", step.ID, step.MaxIterations)
	argsJSON, _ := json.Marshal(args)
	updatedAt := time.Now().Unix()
	if state, updateErr := s.querier.UpdateFlowState(ctx, db.UpdateFlowStateParams{
		Status:         string(FlowStatusFailed),
		Args:           sql.NullString{String: string(argsJSON), Valid: true},
		Output:         sql.NullString{String: capErr.Error(), Valid: true},
		IsStructOutput: false,
		Iteration:      int64(currentIter),
		SessionID:      existing.SessionID,
	}); updateErr != nil {
		logging.Warn("Failed to persist resume cap-trip state", "session_id", existing.SessionID, "error", updateErr)
	} else {
		updatedAt = state.UpdatedAt
	}

	failedState := &FlowState{
		SessionID:     existing.SessionID,
		RootSessionID: existing.RootSessionID,
		FlowID:        f.ID,
		StepID:        step.ID,
		Status:        FlowStatusFailed,
		Args:          args,
		Output:        capErr.Error(),
		Iteration:     currentIter,
		UpdatedAt:     updatedAt,
	}
	flowStates <- failedState
	s.Publish(pubsub.UpdatedEvent, *failedState)

	var work []stepWork
	if step.Fallback != nil && step.Fallback.To != "" {
		if fb := findStep(f.Spec.Steps, step.Fallback.To); fb != nil {
			work = append(work, stepWork{
				step:      *fb,
				args:      copyArgs(args),
				prevStep:  failedState,
				iteration: 1,
			})
		}
	}
	return work
}

func (s *service) resolveSession(ctx context.Context, step Step, sessionID string, rootSessionID string, prevState *FlowState) (session.Session, error) {
	existing, err := s.sessions.Get(ctx, sessionID)
	if err == nil {
		return existing, nil
	}

	title := fmt.Sprintf("Flow step: %s", step.ID)
	sess, err := s.sessions.CreateFlowSession(ctx, sessionID, rootSessionID, title)
	if err != nil {
		return session.Session{}, fmt.Errorf("creating session: %w", err)
	}

	if step.Session.Fork && prevState != nil && prevState.SessionID != "" {
		if copyErr := s.copySessionMessages(ctx, prevState.SessionID, sess.ID); copyErr != nil {
			logging.Warn("Failed to fork session messages", "from", prevState.SessionID, "to", sess.ID, "error", copyErr)
		}
	}

	return sess, nil
}

func (s *service) copySessionMessages(ctx context.Context, fromSessionID, toSessionID string) error {
	msgs, err := s.messages.List(ctx, fromSessionID)
	if err != nil {
		return fmt.Errorf("listing messages from %s: %w", fromSessionID, err)
	}
	for _, msg := range msgs {
		_, err := s.messages.Create(ctx, toSessionID, message.CreateMessageParams{
			Role:  msg.Role,
			Parts: msg.Parts,
			Model: msg.Model,
			Seq:   msg.Seq,
		})
		if err != nil {
			return fmt.Errorf("copying message to %s: %w", toSessionID, err)
		}
	}
	return nil
}

// resolveSessionPrefix determines the session prefix from the flow spec, CLI flag, or timestamp.
func resolveSessionPrefix(specPrefix string, args map[string]any) (string, error) {
	if specPrefix == "" {
		return fmt.Sprintf("%d", time.Now().Unix()), nil
	}

	result := substituteArgs(specPrefix, args)
	if strings.Contains(result, "${args.") {
		return "", fmt.Errorf("session prefix contains unresolved variables: %s", result)
	}

	return result, nil
}

// substituteArgs is a thin wrapper around substituteScoped for callers that
// have no step-scoped variables. Prefer substituteScoped at sites that know
// the current iteration.
func substituteArgs(template string, args map[string]any) string {
	return substituteScoped(template, args, nil)
}

// argsPlaceholderRegex matches ${args.PATH} where PATH is any run of
// non-brace characters. PATH may be a top-level key ("email") or a
// dot-separated path into a nested map ("reviewer.email"). Bare
// ${args} without a path is handled separately above and is NOT
// matched by this regex (the `.` after `args` is required).
var argsPlaceholderRegex = regexp.MustCompile(`\$\{args\.([^}]+)\}`)

// substituteScoped expands ${args.X} and ${step.X} placeholders in template.
// Step-scoped variables are substituted first so they can't accidentally
// shadow args. Step variables are NOT merged into args and never persisted —
// they exist only for prompt rendering and predicate evaluation.
//
// Args placeholders support dot-path traversal into nested maps —
// ${args.reviewer.email} resolves to args["reviewer"]["email"] when
// args["reviewer"] is a map. Top-level keys are still preferred: if
// args has a key that literally contains a dot (e.g. "a.b"), that
// wins over walking a["b"]. Unresolved placeholders are left in place
// verbatim so callers can detect them (see resolveSessionPrefix).
// TODO: consider adding default value support ${args.name:-default}
func substituteScoped(template string, args map[string]any, stepVars map[string]any) string {
	// Step scope first — closed namespace, only known keys.
	for key, value := range stepVars {
		placeholder := fmt.Sprintf("${step.%s}", key)
		template = strings.ReplaceAll(template, placeholder, fmt.Sprintf("%v", value))
	}

	if strings.Contains(template, "${args}") {
		argsJSON, err := json.MarshalIndent(args, "", "  ")
		if err != nil {
			argsJSON = []byte("{}")
		}
		template = strings.ReplaceAll(template, "${args}", string(argsJSON))
	}

	return argsPlaceholderRegex.ReplaceAllStringFunc(template, func(match string) string {
		// match is the whole placeholder "${args.PATH}". Strip the
		// wrapper to recover PATH.
		path := match[len("${args.") : len(match)-1]
		value, ok := resolveArgsPath(args, path)
		if !ok {
			// Preserve the literal placeholder — matches the pre-dot-path
			// behaviour and lets resolveSessionPrefix detect misses.
			return match
		}
		return fmt.Sprintf("%v", value)
	})
}

// resolveArgsPath resolves a dot-path against args. Top-level exact-key
// match wins first (preserves backward compatibility with any flat key
// that literally contains a dot); otherwise the path is split on `.`
// and walked through nested map[string]any values. Returns (value, true)
// when the full path resolves, otherwise (nil, false). Traversal stops
// at the first non-map value or missing key.
func resolveArgsPath(args map[string]any, path string) (any, bool) {
	if v, ok := args[path]; ok {
		return v, true
	}
	parts := strings.Split(path, ".")
	if len(parts) < 2 {
		return nil, false
	}
	var cur any = args
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

type resolvedStep struct {
	step     Step
	postpone bool
}

func resolveNextSteps(rules []Rule, allSteps []Step, args map[string]any, stepVars map[string]any) []resolvedStep {
	var result []resolvedStep
	for _, rule := range rules {
		if rule.If == "" {
			if next := findStep(allSteps, rule.Then); next != nil {
				result = append(result, resolvedStep{step: *next, postpone: rule.Postpone})
			}
			continue
		}
		match, err := evaluatePredicate(rule.If, args, stepVars)
		if err != nil {
			logging.Warn("Failed to evaluate rule predicate", "predicate", rule.If, "error", err)
			continue
		}
		if match {
			if next := findStep(allSteps, rule.Then); next != nil {
				result = append(result, resolvedStep{step: *next, postpone: rule.Postpone})
			}
		}
	}
	return result
}

func findStep(steps []Step, id string) *Step {
	for i := range steps {
		if steps[i].ID == id {
			return &steps[i]
		}
	}
	return nil
}

func copyArgs(args map[string]any) map[string]any {
	data, err := json.Marshal(args)
	if err != nil {
		result := make(map[string]any, len(args))
		maps.Copy(result, args)
		return result
	}
	var result map[string]any
	json.Unmarshal(data, &result)
	return result
}

// validateArgs validates the provided args against the flow's args JSON Schema.
// The "prompt" key is always allowed regardless of the schema definition.
func validateArgs(args map[string]any, schema map[string]any) error {
	if len(schema) == 0 {
		return nil
	}

	properties, _ := schema["properties"].(map[string]any)
	requiredList, _ := schema["required"].([]any)

	// Check required fields
	for _, r := range requiredList {
		key, ok := r.(string)
		if !ok {
			continue
		}
		if _, exists := args[key]; !exists {
			return fmt.Errorf("missing required argument %q", key)
		}
	}

	// Type-check provided args against schema properties
	if properties == nil {
		return nil
	}

	additionalProperties := true
	if ap, ok := schema["additionalProperties"]; ok {
		if b, isBool := ap.(bool); isBool {
			additionalProperties = b
		}
	}

	for key, val := range args {
		if key == "prompt" {
			continue
		}
		propSchema, defined := properties[key]
		if !defined {
			if !additionalProperties {
				return fmt.Errorf("unexpected argument %q", key)
			}
			continue
		}
		propMap, ok := propSchema.(map[string]any)
		if !ok {
			continue
		}
		expectedType, _ := propMap["type"].(string)
		if expectedType == "" {
			continue
		}
		if err := checkType(key, val, expectedType); err != nil {
			return err
		}
	}

	return nil
}

func checkType(key string, val any, expectedType string) error {
	switch expectedType {
	case "string":
		if _, ok := val.(string); !ok {
			return fmt.Errorf("argument %q must be a string, got %T", key, val)
		}
	case "number":
		switch val.(type) {
		case float64, float32, int, int64, json.Number:
		default:
			return fmt.Errorf("argument %q must be a number, got %T", key, val)
		}
	case "integer":
		switch v := val.(type) {
		case int, int64:
		case float64:
			if v != float64(int64(v)) {
				return fmt.Errorf("argument %q must be an integer, got float", key)
			}
		case json.Number:
			if _, err := v.Int64(); err != nil {
				return fmt.Errorf("argument %q must be an integer, got %s", key, v)
			}
		default:
			return fmt.Errorf("argument %q must be an integer, got %T", key, val)
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("argument %q must be a boolean, got %T", key, val)
		}
	case "array":
		if _, ok := val.([]any); !ok {
			return fmt.Errorf("argument %q must be an array, got %T", key, val)
		}
	case "object":
		if _, ok := val.(map[string]any); !ok {
			return fmt.Errorf("argument %q must be an object, got %T", key, val)
		}
	}
	return nil
}

// withFlowArgs extracts top-level args whose names match the configured
// telemetry.flowArgs patterns and stores them in context for downstream
// Langfuse trace metadata.
func withFlowArgs(ctx context.Context, args map[string]any) context.Context {
	cfg := config.Get()
	if cfg == nil || cfg.Telemetry == nil || len(cfg.Telemetry.FlowArgs) == 0 || len(args) == 0 {
		return ctx
	}
	extracted := make(map[string]string)
	for _, pattern := range cfg.Telemetry.FlowArgs {
		for k, v := range args {
			if permission.MatchWildcard(pattern, k) {
				extracted[k] = fmt.Sprintf("%v", v)
			}
		}
	}
	if len(extracted) == 0 {
		return ctx
	}
	return context.WithValue(ctx, tools.FlowArgsContextKey, extracted)
}

func dbFlowStateToFlowState(fs db.FlowState) *FlowState {
	var args map[string]any
	if fs.Args.Valid {
		json.Unmarshal([]byte(fs.Args.String), &args)
	}
	output := ""
	if fs.Output.Valid {
		output = fs.Output.String
	}
	iteration := int(fs.Iteration)
	if iteration < 1 {
		// Backfill defensively: pre-migration rows or zero-value reads.
		iteration = 1
	}
	return &FlowState{
		SessionID:      fs.SessionID,
		RootSessionID:  fs.RootSessionID,
		FlowID:         fs.FlowID,
		StepID:         fs.StepID,
		Status:         FlowStatus(fs.Status),
		Args:           args,
		Output:         output,
		IsStructOutput: fs.IsStructOutput,
		Iteration:      iteration,
		CreatedAt:      fs.CreatedAt,
		UpdatedAt:      fs.UpdatedAt,
	}
}

// envNonInteractiveTaskWaitTimeout is the parsed-once value of the
// OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT environment variable. Zero
// when unset, malformed, or non-positive. Initialised lazily on first
// call to envTaskWaitTimeout.
var (
	envNonInteractiveTaskWaitTimeoutOnce sync.Once
	envNonInteractiveTaskWaitTimeoutVal  time.Duration
)

// envTaskWaitTimeout returns the parsed value of the
// OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT env var. Parsed once at
// first call so SIGHUP-style reloads are explicit (process restart
// required to change). Returns 0 when unset / malformed.
func envTaskWaitTimeout() time.Duration {
	envNonInteractiveTaskWaitTimeoutOnce.Do(func() {
		raw := strings.TrimSpace(os.Getenv("OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT"))
		if raw == "" {
			return
		}
		d, err := time.ParseDuration(raw)
		if err != nil {
			logging.Warn("Invalid OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT; ignoring", "value", raw, "err", err)
			return
		}
		if d <= 0 {
			logging.Warn("OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT must be positive; ignoring", "value", raw)
			return
		}
		envNonInteractiveTaskWaitTimeoutVal = d
	})
	return envNonInteractiveTaskWaitTimeoutVal
}

// stepCtx builds the per-step ctx for the agent.RunWith call. Precedence:
//  1. Step.Timeout (parsed via Step.TimeoutDuration)
//  2. OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT env var
//  3. Parent ctx unwrapped (the parent's own deadline, if any, is the only
//     bound)
//
// Always returns a non-nil cancel func; callers MUST invoke it on every
// exit path to release resources.
func stepCtx(parent context.Context, step Step) (context.Context, context.CancelFunc) {
	if step.Timeout != "" {
		// If parsing fails here, we silently fall through to the env-var
		// fallback — validation has already surfaced the bad value (see
		// validateFlow). Treating this as a soft error keeps the runtime
		// resilient to YAML drift.
		if d, err := step.TimeoutDuration(); err == nil && d > 0 {
			return context.WithTimeout(parent, d)
		}
	}
	if d := envTaskWaitTimeout(); d > 0 {
		return context.WithTimeout(parent, d)
	}
	return context.WithCancel(parent)
}
