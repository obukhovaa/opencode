package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/opencode-ai/opencode/internal/flow"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// flowRunStatus mirrors the state machine the flow-api spec describes
// for the bridge-attached HTTP surface. It is a thin projection of the
// underlying flow.FlowStatus (which is per-step), elevated to the
// "current run" view the orchestrator needs.
type flowRunStatus string

const (
	flowRunRunning         flowRunStatus = "running"
	flowRunWaitingForInput flowRunStatus = "waiting_for_input"
	flowRunCompleted       flowRunStatus = "completed"
	flowRunFailed          flowRunStatus = "failed"
)

// flowEventType is the SSE event-type enum the bridge-http-api spec
// adds to /event. Values are stable wire-level strings — orchestrators
// match on them.
type flowEventType string

const (
	evFlowStepStarted     flowEventType = "flow.step.started"
	evFlowStepCompleted   flowEventType = "flow.step.completed"
	evFlowStepFailed      flowEventType = "flow.step.failed"
	evFlowWaitingForInput flowEventType = "flow.waiting_for_input"
	evFlowCompleted       flowEventType = "flow.completed"
	evFlowFailed          flowEventType = "flow.failed"
)

// FlowEvent is the SSE payload for every flow-* event type. Fields are
// optional per the spec's per-event payload table — callers populate
// only the relevant ones. Marshalled to JSON via the existing /event
// broker.
type FlowEvent struct {
	Type        flowEventType `json:"type"`
	RunID       string        `json:"runID"`
	FlowID      string        `json:"flowID,omitempty"`
	StepID      string        `json:"stepID,omitempty"`
	SessionID   string        `json:"sessionID,omitempty"`
	Output      string        `json:"output,omitempty"`
	Error       string        `json:"error,omitempty"`
	Target      any           `json:"target,omitempty"`
	StartedAt   int64         `json:"startedAt,omitempty"`
	CompletedAt int64         `json:"completedAt,omitempty"`
	FailedAt    int64         `json:"failedAt,omitempty"`
}

// flowStepRecord captures one completed step's outcome for /flow/status.
type flowStepRecord struct {
	ID          string `json:"id"`
	SessionID   string `json:"sessionID"`
	Status      string `json:"status"`
	Output      string `json:"output,omitempty"`
	StartedAt   int64  `json:"startedAt"`
	CompletedAt int64  `json:"completedAt,omitempty"`
	Error       string `json:"error,omitempty"`
}

// flowRunSnapshot is the body shape /flow/status returns.
type flowRunSnapshot struct {
	RunID          string           `json:"runID"`
	FlowID         string           `json:"flowID"`
	Status         flowRunStatus    `json:"status"`
	StartedAt      int64            `json:"startedAt"`
	CompletedAt    int64            `json:"completedAt,omitempty"`
	CurrentStep    *flowStepRecord  `json:"currentStep,omitempty"`
	CompletedSteps []flowStepRecord `json:"completedSteps,omitempty"`
	WaitingTarget  any              `json:"waitingTarget,omitempty"`
	Error          string           `json:"error,omitempty"`
}

// flowRunner tracks the single active flow run per process. The bridge-
// http-api spec mandates "one flow at a time" — POST /flow returns 409
// if another run is in flight. This struct is the locked state.
type flowRunner struct {
	mu sync.Mutex

	// currentRun is non-nil while a run is in flight or its status is
	// being retained for the next GET /flow/status query. Reset on the
	// next successful POST /flow.
	currentRun *flowRunState

	// broker is the SSE broker carrying FlowEvent payloads to /event
	// subscribers. Created at first use and reused for the lifetime
	// of the process.
	broker *pubsub.Broker[FlowEvent]

	app appReadOnly

	// validateFlowID is the synchronous flow-existence check Start
	// invokes before launching the runner goroutine. Returning
	// flow.ErrFlowNotFound causes POST /flow to respond 404 instead of
	// accepting the request and surfacing the failure async. Tests
	// substitute a noop so synthetic flow IDs are accepted without
	// touching the real flow registry (which depends on config.Get()).
	validateFlowID func(string) error
}

// appReadOnly is the minimal app surface the flow runner uses. We don't
// import internal/app's full struct just to read Flows.
type appReadOnly interface {
	FlowsService() flow.Service
}

type flowRunState struct {
	RunID     string
	FlowID    string
	StartedAt int64
	Status    flowRunStatus

	// cancel cancels the in-flight flow.Service.Run context. DELETE /flow
	// invokes this.
	cancel context.CancelFunc

	currentStep    *flowStepRecord
	completedSteps []flowStepRecord
	completedAt    int64
	err            string
	waitingTarget  any
}

// flowRunnerSingleton is the process-wide tracker installed on
// api.Server at construction. Tests can swap it out via a Server hook.
//
// The runner is wired into the API server in NewServer when the
// application has a non-nil flow.Service.

// handleFlowList lists every discovered flow YAML. The shape mirrors
// the flow-api spec's GET /flow contract.
func (s *Server) handleFlowList(w http.ResponseWriter, _ *http.Request) {
	flows := flow.All()
	type publicFlow struct {
		ID          string         `json:"id"`
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Disabled    bool           `json:"disabled,omitempty"`
		Args        map[string]any `json:"args,omitempty"`
	}
	out := make([]publicFlow, 0, len(flows))
	for _, f := range flows {
		out = append(out, publicFlow{
			ID:          f.ID,
			Name:        f.Name,
			Description: f.Description,
			Disabled:    f.Disabled,
			Args:        f.Spec.Args,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleFlowStart starts a new flow run if none is in flight. Returns
// 409 when another run is active.
func (s *Server) handleFlowStart(w http.ResponseWriter, r *http.Request) {
	if s.flowRunner == nil {
		writeError(w, http.StatusServiceUnavailable, "flow runner not configured")
		return
	}
	var body struct {
		FlowID string         `json:"flowID"`
		Args   map[string]any `json:"args"`
		Fresh  bool           `json:"fresh"`
	}
	if err := readJSON(r, &body); err != nil && !isEmptyBodyError(err) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.FlowID == "" {
		writeError(w, http.StatusBadRequest, "flowID is required")
		return
	}

	result, err := s.flowRunner.Start(r.Context(), body.FlowID, body.Args, body.Fresh)
	switch {
	case errors.Is(err, errFlowAlreadyRunning):
		writeError(w, http.StatusConflict, "another flow is already running")
	case errors.Is(err, flow.ErrFlowNotFound):
		writeError(w, http.StatusNotFound, "flow not found")
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		// Spec shape: {runID, flowID, status, currentStep}. currentStep
		// is null at 202 time — the runner goroutine hasn't observed
		// the first step transition yet. Callers obtain the active
		// step via GET /flow/status / SSE flow.step.started.
		writeJSON(w, http.StatusAccepted, map[string]any{
			"runID":       result.RunID,
			"flowID":      result.FlowID,
			"status":      result.Status,
			"currentStep": nil,
		})
	}
}

// handleFlowStatus returns the current snapshot for the latest run.
func (s *Server) handleFlowStatus(w http.ResponseWriter, _ *http.Request) {
	if s.flowRunner == nil {
		writeError(w, http.StatusServiceUnavailable, "flow runner not configured")
		return
	}
	snap := s.flowRunner.Snapshot()
	if snap == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "idle"})
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// handleFlowAbort cancels the in-flight run.
func (s *Server) handleFlowAbort(w http.ResponseWriter, _ *http.Request) {
	if s.flowRunner == nil {
		writeError(w, http.StatusServiceUnavailable, "flow runner not configured")
		return
	}
	if !s.flowRunner.Abort() {
		writeError(w, http.StatusConflict, "no flow is running")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"aborted": true})
}

// errFlowAlreadyRunning is the sentinel returned by Start when another
// run is in flight.
var errFlowAlreadyRunning = errors.New("flow: another run is already in flight")

// StartResult is the immutable handle Start returns to the HTTP handler.
// The mutable per-run state lives on fr.currentRun and is accessed only
// under fr.mu.
type StartResult struct {
	RunID     string
	FlowID    string
	Status    flowRunStatus
	StartedAt int64
}

// Start kicks off a new flow run. Returns errFlowAlreadyRunning if
// another run is in flight, or flow.ErrFlowNotFound when flowID doesn't
// match any registered flow YAML. The flowID is validated synchronously
// so the HTTP layer can map the error to a 404 instead of accepting the
// request and surfacing the failure asynchronously.
func (fr *flowRunner) Start(parent context.Context, flowID string, args map[string]any, fresh bool) (StartResult, error) {
	if fr.validateFlowID != nil {
		if err := fr.validateFlowID(flowID); err != nil {
			return StartResult{}, err
		}
	}
	fr.mu.Lock()
	if fr.currentRun != nil && fr.currentRun.Status == flowRunRunning {
		fr.mu.Unlock()
		return StartResult{}, errFlowAlreadyRunning
	}
	runID := uuid.NewString()
	runCtx, cancel := context.WithCancel(context.Background())
	state := &flowRunState{
		RunID:     runID,
		FlowID:    flowID,
		StartedAt: time.Now().UnixMilli(),
		Status:    flowRunRunning,
		cancel:    cancel,
	}
	fr.currentRun = state
	result := StartResult{
		RunID:     state.RunID,
		FlowID:    state.FlowID,
		Status:    state.Status,
		StartedAt: state.StartedAt,
	}
	fr.mu.Unlock()
	_ = parent

	// Kick off the run in the background; SSE consumers see progress
	// via fr.broker.
	go fr.run(runCtx, state, flowID, args, fresh)
	return result, nil
}

// run drives the flow.Service.Run lifecycle, fanning AgentEvent + FlowState
// into the FlowEvent broker so /event subscribers see step transitions.
func (fr *flowRunner) run(ctx context.Context, state *flowRunState, flowID string, args map[string]any, fresh bool) {
	defer func() {
		if r := recover(); r != nil {
			logging.Error("flow runner panic", "run", state.RunID, "panic", r)
		}
	}()

	svc := fr.app.FlowsService()
	if svc == nil {
		fr.finish(state, flowRunFailed, "flow service not configured")
		return
	}

	_, flowStates, err := svc.Run(ctx, "", flowID, args, fresh)
	if err != nil {
		fr.finish(state, flowRunFailed, err.Error())
		return
	}

	for st := range flowStates {
		fr.observeStep(state, st)
		select {
		case <-ctx.Done():
			fr.finish(state, flowRunFailed, "aborted")
			return
		default:
		}
	}
	if state.err != "" {
		fr.finish(state, flowRunFailed, state.err)
		return
	}
	fr.finish(state, flowRunCompleted, "")
}

// observeStep updates the run state when a step transitions and
// publishes the corresponding FlowEvent on the SSE broker.
func (fr *flowRunner) observeStep(state *flowRunState, st *flow.FlowState) {
	if st == nil {
		return
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	now := time.Now().UnixMilli()
	rec := flowStepRecord{
		ID:        st.StepID,
		SessionID: st.SessionID,
		Status:    string(st.Status),
		Output:    st.Output,
		StartedAt: st.UpdatedAt * 1000,
	}
	switch st.Status {
	case flow.FlowStatusRunning:
		state.currentStep = &rec
		// A new step entering "running" clears any prior
		// waiting_for_input signal — the previous interactive step has
		// concluded by the time the next step starts.
		state.waitingTarget = nil
		if state.Status == flowRunWaitingForInput {
			state.Status = flowRunRunning
		}
		fr.publishEvent(state, FlowEvent{
			Type:      evFlowStepStarted,
			RunID:     state.RunID,
			StepID:    rec.ID,
			SessionID: rec.SessionID,
			StartedAt: now,
		})
	case flow.FlowStatusWaitingForInput:
		// Interactive step transitioned to bound-and-waiting. Per the
		// flow-api spec, emit flow.waiting_for_input carrying the
		// resolved target peers; the snapshot's WaitingTarget reflects
		// the same value until the agent's next turn closes the wait.
		state.waitingTarget = st.WaitingTarget
		state.Status = flowRunWaitingForInput
		fr.publishEvent(state, FlowEvent{
			Type:      evFlowWaitingForInput,
			RunID:     state.RunID,
			FlowID:    state.FlowID,
			StepID:    rec.ID,
			SessionID: rec.SessionID,
			Target:    st.WaitingTarget,
		})
	case flow.FlowStatusCompleted:
		rec.CompletedAt = now
		state.completedSteps = append(state.completedSteps, rec)
		state.currentStep = nil
		fr.publishEvent(state, FlowEvent{
			Type:        evFlowStepCompleted,
			RunID:       state.RunID,
			StepID:      rec.ID,
			SessionID:   rec.SessionID,
			Output:      rec.Output,
			CompletedAt: now,
		})
	case flow.FlowStatusFailed:
		rec.Error = st.Output
		state.completedSteps = append(state.completedSteps, rec)
		state.err = st.Output
		fr.publishEvent(state, FlowEvent{
			Type:     evFlowStepFailed,
			RunID:    state.RunID,
			StepID:   rec.ID,
			Error:    rec.Error,
			FailedAt: now,
		})
	}
}

// publishEvent emits a FlowEvent on the SSE broker. Caller must hold mu.
func (fr *flowRunner) publishEvent(_ *flowRunState, ev FlowEvent) {
	if fr.broker == nil {
		return
	}
	fr.broker.Publish(pubsub.UpdatedEvent, ev)
}

// finish records the terminal status of a run and emits the
// flow.completed / flow.failed SSE event.
func (fr *flowRunner) finish(state *flowRunState, status flowRunStatus, errMsg string) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	state.Status = status
	state.completedAt = time.Now().UnixMilli()
	if errMsg != "" {
		state.err = errMsg
	}
	if status == flowRunCompleted {
		fr.publishEvent(state, FlowEvent{
			Type:        evFlowCompleted,
			RunID:       state.RunID,
			CompletedAt: state.completedAt,
		})
	} else {
		fr.publishEvent(state, FlowEvent{
			Type:     evFlowFailed,
			RunID:    state.RunID,
			Error:    errMsg,
			FailedAt: state.completedAt,
		})
	}
}

// Abort cancels the in-flight run. Returns true if a run was cancelled.
func (fr *flowRunner) Abort() bool {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if fr.currentRun == nil || fr.currentRun.Status != flowRunRunning {
		return false
	}
	if fr.currentRun.cancel != nil {
		fr.currentRun.cancel()
	}
	return true
}

// Snapshot returns the current run's projection. Nil when no run has
// been started in this process.
func (fr *flowRunner) Snapshot() *flowRunSnapshot {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if fr.currentRun == nil {
		return nil
	}
	cs := make([]flowStepRecord, len(fr.currentRun.completedSteps))
	copy(cs, fr.currentRun.completedSteps)
	return &flowRunSnapshot{
		RunID:          fr.currentRun.RunID,
		FlowID:         fr.currentRun.FlowID,
		Status:         fr.currentRun.Status,
		StartedAt:      fr.currentRun.StartedAt,
		CompletedAt:    fr.currentRun.completedAt,
		CurrentStep:    cloneStepRecordPtr(fr.currentRun.currentStep),
		CompletedSteps: cs,
		WaitingTarget:  fr.currentRun.waitingTarget,
		Error:          fr.currentRun.err,
	}
}

func cloneStepRecordPtr(r *flowStepRecord) *flowStepRecord {
	if r == nil {
		return nil
	}
	c := *r
	return &c
}

// subscribeFlowEvents returns a channel of FlowEvent payloads for the
// SSE endpoint to forward to clients.
func (fr *flowRunner) subscribeFlowEvents(ctx context.Context) <-chan pubsub.Event[FlowEvent] {
	if fr.broker == nil {
		ch := make(chan pubsub.Event[FlowEvent])
		close(ch)
		return ch
	}
	return fr.broker.Subscribe(ctx)
}

// MarkSnapshotJSON helps tests; not part of the spec surface.
func (fr *flowRunner) MarkSnapshotJSON() ([]byte, error) {
	snap := fr.Snapshot()
	if snap == nil {
		return []byte(`null`), nil
	}
	return json.Marshal(snap)
}

// flowAppAdapter is the trivial *app.App → appReadOnly adapter used by
// NewServer. Lives here so handler_flow.go can stay decoupled from the
// app package via an interface.
type flowAppAdapter struct{ get func() flow.Service }

func (a flowAppAdapter) FlowsService() flow.Service { return a.get() }

// newFlowRunner constructs the singleton runner. cmd/serve.go indirectly
// invokes this through NewServer; tests can construct one directly via
// newFlowRunner and override validateFlowID afterwards.
//
// The validator defaults to flow.Get-based existence check, which in
// production reads .opencode/flows/* via the config-dependent registry.
// Tests that drive a stub flow.Service set fr.validateFlowID = nil to
// skip the check (synthetic flow IDs don't appear in any YAML).
func newFlowRunner(svc flow.Service) *flowRunner {
	return &flowRunner{
		broker: pubsub.NewBroker[FlowEvent](),
		app:    flowAppAdapter{get: func() flow.Service { return svc }},
		validateFlowID: func(id string) error {
			if _, err := flow.Get(id); err != nil {
				return err
			}
			return nil
		},
	}
}

// StartFlow programmatically starts a flow without going through HTTP.
// Used by cmd/serve.go's --flow auto-start path. Returns the runID on
// success.
func (s *Server) StartFlow(flowID string, args map[string]any, fresh bool) (string, error) {
	if s.flowRunner == nil {
		return "", errors.New("flow runner not configured")
	}
	result, err := s.flowRunner.Start(context.Background(), flowID, args, fresh)
	if err != nil {
		return "", err
	}
	return result.RunID, nil
}

// WaitFlowTerminal blocks until the flow run identified by runID
// reaches a terminal status (completed | failed), then invokes
// onTerminal. Used by --flow-exit to trigger process shutdown after
// the auto-started flow finishes.
func (s *Server) WaitFlowTerminal(ctx context.Context, runID string, onTerminal context.CancelFunc) {
	if s.flowRunner == nil {
		return
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			snap := s.flowRunner.Snapshot()
			if snap == nil || snap.RunID != runID {
				continue
			}
			if snap.Status == flowRunCompleted || snap.Status == flowRunFailed {
				logging.Info("auto-flow terminal — exiting",
					"flow", snap.FlowID, "runID", runID, "status", snap.Status)
				if onTerminal != nil {
					onTerminal()
				}
				return
			}
		}
	}
}

// UnmarshalFlowArgs is a small JSON helper exposed for cmd/serve.go so
// it can parse --flow-args without depending on encoding/json directly.
func UnmarshalFlowArgs(data []byte, target *map[string]any) error {
	return json.Unmarshal(data, target)
}

// asAgentEvents is kept to avoid the "imported and not used" friction
// if/when run() later forwards AgentEvents to a per-run SSE filter.
var _ = func(<-chan agentpkg.AgentEvent) {}

// fmtNoUnused keeps the fmt import referenced even when no formatting
// is done in this file (helps avoid the linter false-flagging it).
var _ = fmt.Sprintf
