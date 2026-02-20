package flow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
)

type FlowStatus string

const (
	FlowStatusRunning   FlowStatus = "running"
	FlowStatusCompleted FlowStatus = "completed"
	FlowStatusFailed    FlowStatus = "failed"
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
	CreatedAt      int64
	UpdatedAt      int64
}

// AgentProvider resolves agent services by ID on demand.
// Implementations should handle lazy creation and caching.
type AgentProvider interface {
	Get(agentID string) (agentpkg.Service, error)
}

type Service interface {
	pubsub.Suscriber[FlowState]
	Run(ctx context.Context, sessionPrefix string, flowID string, args map[string]any, fresh bool) (<-chan agentpkg.AgentEvent, <-chan *FlowState, error)
}

type service struct {
	*pubsub.Broker[FlowState]
	sessions    session.Service
	querier     db.QuerierWithTx
	permissions permission.Service
	agents      AgentProvider
}

func NewService(
	sessions session.Service,
	querier db.QuerierWithTx,
	permissions permission.Service,
	agents AgentProvider,
) Service {
	return &service{
		Broker:      pubsub.NewBroker[FlowState](),
		sessions:    sessions,
		querier:     querier,
		permissions: permissions,
		agents:      agents,
	}
}

type stepWork struct {
	step     Step
	args     map[string]any
	prevStep *FlowState
}

func (s *service) Run(ctx context.Context, sessionPrefix string, flowID string, args map[string]any, fresh bool) (<-chan agentpkg.AgentEvent, <-chan *FlowState, error) {
	f, err := Get(flowID)
	if err != nil {
		return nil, nil, err
	}
	if f.Disabled {
		return nil, nil, fmt.Errorf("%w: %s", ErrFlowDisabled, flowID)
	}

	if sessionPrefix == "" {
		sessionPrefix = fmt.Sprintf("%d", time.Now().Unix())
	}
	rootSessionID := fmt.Sprintf("%s-%s-%s", sessionPrefix, flowID, f.Spec.Steps[0].ID)

	existingStates, err := s.querier.ListFlowStatesByRootSession(ctx, rootSessionID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("checking existing flow states: %w", err)
	}

	hasRunning := false
	for _, es := range existingStates {
		if es.Status == string(FlowStatusRunning) {
			hasRunning = true
			break
		}
	}

	agentEvents := make(chan agentpkg.AgentEvent, 100)
	flowStates := make(chan *FlowState, 100)

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

	if fresh {
		if err := s.querier.DeleteFlowStatesByRootSession(ctx, rootSessionID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			logging.Warn("Failed to delete existing flow states", "error", err)
		}
		for _, step := range f.Spec.Steps {
			stepSessionID := fmt.Sprintf("%s-%s-%s", sessionPrefix, flowID, step.ID)
			if err := s.sessions.Delete(ctx, stepSessionID); err != nil {
				logging.Debug("Could not delete session during fresh start", "session_id", stepSessionID, "error", err)
			}
		}
	}

	nextSteps := make(chan stepWork, len(f.Spec.Steps))
	nextSteps <- stepWork{step: f.Spec.Steps[0], args: copyArgs(args)}

	var wg sync.WaitGroup
	startedSteps := &sync.Map{}

	go func() {
		for work := range nextSteps {
			stepSessionID := fmt.Sprintf("%s-%s-%s", sessionPrefix, flowID, work.step.ID)
			if _, loaded := startedSteps.LoadOrStore(work.step.ID, true); loaded {
				logging.Debug("Step already started, skipping (diamond convergence)", "step", work.step.ID)
				continue
			}

			wg.Add(1)
			go func(w stepWork, sessID string) {
				defer wg.Done()
				s.runStep(ctx, f, w.step, sessID, rootSessionID, sessionPrefix, w.args, w.prevStep, agentEvents, flowStates, nextSteps, startedSteps)
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
	sessionPrefix string,
	args map[string]any,
	prevState *FlowState,
	agentEvents chan<- agentpkg.AgentEvent,
	flowStates chan<- *FlowState,
	nextSteps chan<- stepWork,
	startedSteps *sync.Map,
) {
	agentID := step.Agent
	if agentID == "" {
		agentID = "coder"
	}

	agentSvc, err := s.agents.Get(agentID)
	if err != nil {
		s.handleStepError(ctx, step, sessionID, rootSessionID, f.ID, args, fmt.Errorf("agent %q not found", agentID), agentEvents, flowStates, nextSteps, f, sessionPrefix, startedSteps)
		return
	}

	sess, err := s.resolveSession(ctx, step, sessionID, rootSessionID, prevState)
	if err != nil {
		s.handleStepError(ctx, step, sessionID, rootSessionID, f.ID, args, fmt.Errorf("resolving session: %w", err), agentEvents, flowStates, nextSteps, f, sessionPrefix, startedSteps)
		return
	}

	s.permissions.AutoApproveSession(sess.ID)

	prompt := substituteArgs(step.Prompt, args)
	if prevState != nil && prevState.Output != "" {
		prompt = fmt.Sprintf("Previous step (%s) output:\n%s\n\n%s", prevState.StepID, prevState.Output, prompt)
	}

	argsJSON, _ := json.Marshal(args)
	_, err = s.querier.CreateFlowState(ctx, db.CreateFlowStateParams{
		SessionID:      sessionID,
		RootSessionID:  rootSessionID,
		FlowID:         f.ID,
		StepID:         step.ID,
		Status:         string(FlowStatusRunning),
		Args:           sql.NullString{String: string(argsJSON), Valid: true},
		IsStructOutput: false,
	})
	if err != nil {
		s.handleStepError(ctx, step, sessionID, rootSessionID, f.ID, args, fmt.Errorf("persisting flow state: %w", err), agentEvents, flowStates, nextSteps, f, sessionPrefix, startedSteps)
		return
	}

	runningState := &FlowState{
		SessionID:     sessionID,
		RootSessionID: rootSessionID,
		FlowID:        f.ID,
		StepID:        step.ID,
		Status:        FlowStatusRunning,
		Args:          args,
	}
	flowStates <- runningState
	s.Publish(pubsub.UpdatedEvent, *runningState)

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
			done, runErr := agentSvc.Run(ctx, sess.ID, prompt)
			if runErr != nil {
				lastErr = runErr
				continue
			}

			result = <-done
			if result.Type == agentpkg.AgentEventTypeError {
				lastErr = result.Error
				continue
			}

			lastErr = nil
			break
		}
	}
doneRetry:

	if lastErr != nil {
		s.querier.UpdateFlowState(ctx, db.UpdateFlowStateParams{
			Status:         string(FlowStatusFailed),
			Output:         sql.NullString{String: lastErr.Error(), Valid: true},
			IsStructOutput: false,
			SessionID:      sessionID,
		})

		failedState := &FlowState{
			SessionID:     sessionID,
			RootSessionID: rootSessionID,
			FlowID:        f.ID,
			StepID:        step.ID,
			Status:        FlowStatusFailed,
			Args:          args,
			Output:        lastErr.Error(),
		}
		flowStates <- failedState
		s.Publish(pubsub.UpdatedEvent, *failedState)

		if step.Fallback != nil && step.Fallback.To != "" {
			fallbackStep := findStep(f.Spec.Steps, step.Fallback.To)
			if fallbackStep != nil {
				nextSteps <- stepWork{step: *fallbackStep, args: copyArgs(args), prevStep: failedState}
			}
		}
		return
	}

	var output string
	isStructOutput := false
	if result.StructOutput != nil {
		output = result.StructOutput.Content
		isStructOutput = true

		var structData map[string]any
		if err := json.Unmarshal([]byte(output), &structData); err == nil {
			for k, v := range structData {
				args[k] = v
			}
		}
	} else {
		output = result.Message.Content().Text
	}

	s.querier.UpdateFlowState(ctx, db.UpdateFlowStateParams{
		Status:         string(FlowStatusCompleted),
		Output:         sql.NullString{String: output, Valid: output != ""},
		IsStructOutput: isStructOutput,
		SessionID:      sessionID,
	})

	completedState := &FlowState{
		SessionID:      sessionID,
		RootSessionID:  rootSessionID,
		FlowID:         f.ID,
		StepID:         step.ID,
		Status:         FlowStatusCompleted,
		Args:           args,
		Output:         output,
		IsStructOutput: isStructOutput,
	}
	flowStates <- completedState
	s.Publish(pubsub.UpdatedEvent, *completedState)

	result.FlowStepID = step.ID
	agentEvents <- result

	for _, rule := range step.Rules {
		match, err := evaluatePredicate(rule.If, args)
		if err != nil {
			logging.Warn("Failed to evaluate rule predicate", "step", step.ID, "predicate", rule.If, "error", err)
			continue
		}
		if match {
			nextStep := findStep(f.Spec.Steps, rule.Then)
			if nextStep != nil {
				nextSteps <- stepWork{step: *nextStep, args: copyArgs(args), prevStep: completedState}
			}
		}
	}
}

func (s *service) handleStepError(
	ctx context.Context,
	step Step,
	sessionID string,
	rootSessionID string,
	flowID string,
	args map[string]any,
	err error,
	agentEvents chan<- agentpkg.AgentEvent,
	flowStates chan<- *FlowState,
	nextSteps chan<- stepWork,
	f *Flow,
	sessionPrefix string,
	startedSteps *sync.Map,
) {
	logging.Error("Flow step failed", "step", step.ID, "error", err)

	failedState := &FlowState{
		SessionID:     sessionID,
		RootSessionID: rootSessionID,
		FlowID:        flowID,
		StepID:        step.ID,
		Status:        FlowStatusFailed,
		Args:          args,
		Output:        err.Error(),
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
			nextSteps <- stepWork{step: *fallbackStep, args: copyArgs(args), prevStep: failedState}
		}
	}
}

func (s *service) resolveSession(ctx context.Context, step Step, sessionID string, rootSessionID string, prevState *FlowState) (session.Session, error) {
	existing, err := s.sessions.Get(ctx, sessionID)
	if err == nil {
		return existing, nil
	}

	title := fmt.Sprintf("Flow step: %s", step.ID)
	sess, err := s.sessions.CreateWithID(ctx, sessionID, title)
	if err != nil {
		return session.Session{}, fmt.Errorf("creating session: %w", err)
	}
	return sess, nil
}

func substituteArgs(template string, args map[string]any) string {
	if strings.Contains(template, "${args}") {
		argsJSON, err := json.MarshalIndent(args, "", "  ")
		if err != nil {
			argsJSON = []byte("{}")
		}
		template = strings.ReplaceAll(template, "${args}", string(argsJSON))
	}

	for key, value := range args {
		placeholder := fmt.Sprintf("${args.%s}", key)
		template = strings.ReplaceAll(template, placeholder, fmt.Sprintf("%v", value))
	}

	return template
}

var predicateRegex = regexp.MustCompile(`^\$\{args\.([^}]+)\}\s*(==|!=|=~)\s*(.+)$`)

func evaluatePredicate(predicate string, args map[string]any) (bool, error) {
	matches := predicateRegex.FindStringSubmatch(strings.TrimSpace(predicate))
	if matches == nil {
		return false, fmt.Errorf("%w: %q", ErrInvalidPredicate, predicate)
	}

	key := matches[1]
	op := matches[2]
	expected := strings.TrimSpace(matches[3])

	actual, ok := args[key]
	if !ok {
		return false, nil
	}
	actualStr := fmt.Sprintf("%v", actual)

	switch op {
	case "==":
		return actualStr == expected, nil
	case "!=":
		return actualStr != expected, nil
	case "=~":
		if len(expected) < 2 || expected[0] != '/' || expected[len(expected)-1] != '/' {
			return false, fmt.Errorf("regex pattern must be delimited by /: %q", expected)
		}
		pattern := expected[1 : len(expected)-1]
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false, fmt.Errorf("invalid regex pattern %q: %w", pattern, err)
		}
		return re.MatchString(actualStr), nil
	default:
		return false, fmt.Errorf("unknown operator %q", op)
	}
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
	result := make(map[string]any, len(args))
	for k, v := range args {
		result[k] = v
	}
	return result
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
	return &FlowState{
		SessionID:      fs.SessionID,
		RootSessionID:  fs.RootSessionID,
		FlowID:         fs.FlowID,
		StepID:         fs.StepID,
		Status:         FlowStatus(fs.Status),
		Args:           args,
		Output:         output,
		IsStructOutput: fs.IsStructOutput,
		CreatedAt:      fs.CreatedAt,
		UpdatedAt:      fs.UpdatedAt,
	}
}
