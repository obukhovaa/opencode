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
	"github.com/opencode-ai/opencode/internal/session"
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
	// flowRunPostponed marks a run that terminated because the last
	// observed step transitioned to FlowStatusPostponed (via a
	// `postpone: true` rule) and no further work was queued. Distinct
	// from flowRunCompleted so downstream consumers can render
	// "Waiting for user action..." instead of "Completed".
	flowRunPostponed flowRunStatus = "postponed"
)

// flowEventType is the SSE event-type enum the bridge-http-api spec
// adds to /event. Values are stable wire-level strings — orchestrators
// match on them.
type flowEventType string

const (
	evFlowStepStarted     flowEventType = "flow.step.started"
	evFlowStepCompleted   flowEventType = "flow.step.completed"
	evFlowStepFailed      flowEventType = "flow.step.failed"
	evFlowStepPostponed   flowEventType = "flow.step.postponed"
	evFlowWaitingForInput flowEventType = "flow.waiting_for_input"
	evFlowCompleted       flowEventType = "flow.completed"
	evFlowFailed          flowEventType = "flow.failed"
	evFlowPostponed       flowEventType = "flow.postponed"
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
	// IsStructOutput is true when the step produced a JSON struct_output.
	// Sourced from flow.FlowState.IsStructOutput. Orchestrators use this
	// to render the per-step block differently (struct vs free-text).
	IsStructOutput bool `json:"isStructOutput,omitempty"`
	// Iteration is the 1-based self-loop iteration number for this step.
	// Sourced from flow.FlowState.Iteration. Surfaced for cost-attribution
	// of in-step retries.
	Iteration int `json:"iteration,omitempty"`
	// Cost is the running cumulative session cost (USD) at event-emit time.
	// Looked up via session.Service.Get(state.SessionID).Cost. Zero when
	// the session lookup fails (missing or service unavailable).
	Cost float64 `json:"cost,omitempty"`
	// ContextSize is the size of the LLM context window in use at emit
	// time — i.e., the last turn's input + cache-creation tokens plus
	// its output + cache-read tokens. Looked up via
	// session.Service.Get(state.SessionID).{PromptTokens,CompletionTokens}.
	// Mirrors the legacy `cmd/flow.go` accounting so live SSE-driven
	// Slack updates match the post-completion values. Zero on lookup
	// failure.
	ContextSize int64 `json:"contextSize,omitempty"`
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

	// warnedSessions records session IDs whose lookup has already
	// produced a warn so lookupSessionCost logs at most once per ID.
	// Lazy-initialised in markWarned.
	warnedMu       sync.Mutex
	warnedSessions map[string]struct{}
}

// appReadOnly is the minimal app surface the flow runner uses. We don't
// import internal/app's full struct just to read Flows.
//
// SessionsService is optional; nil-returning implementations cause the
// per-step Cost/ContextSize lookup to gracefully fall back to zero
// values (the FlowEvent JSON omits the fields via omitempty). Production
// always wires the real service; tests can leave it unset.
type appReadOnly interface {
	FlowsService() flow.Service
	SessionsService() session.Service
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

	// lastStepPostponed records whether the most recent step transition
	// was a postpone (FlowStatusPostponed). Cleared by any subsequent
	// Running / Completed / Failed transition so a postpone-then-resume
	// sequence ends in flowRunCompleted, not flowRunPostponed. Read by
	// the runner's terminal selector to pick between flow.completed and
	// flow.postponed when the flowStates channel drains. Same-goroutine
	// access only: observeStep writes it under fr.mu; the run() terminal
	// selector reads it from the same goroutine that drove observeStep,
	// so the read sees the latest write without re-locking.
	lastStepPostponed bool
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
	// Terminal status selector — mutually exclusive:
	//   err set            → flow.failed
	//   last step postponed → flow.postponed
	//   otherwise          → flow.completed
	// The lastStepPostponed flag is set in observeStep when a step
	// transitions to FlowStatusPostponed and cleared by any subsequent
	// Running/Completed/Failed transition. See flowRunState comment.
	switch {
	case state.err != "":
		fr.finish(state, flowRunFailed, state.err)
	case state.lastStepPostponed:
		fr.finish(state, flowRunPostponed, "")
	default:
		fr.finish(state, flowRunCompleted, "")
	}
}

// observeStep updates the run state when a step transitions and
// publishes the corresponding FlowEvent on the SSE broker.
func (fr *flowRunner) observeStep(state *flowRunState, st *flow.FlowState) {
	if st == nil {
		return
	}
	// Pull Cost / ContextSize from session.Service BEFORE taking fr.mu so
	// the bounded (250 ms) DB read doesn't block concurrent Snapshot() /
	// Abort() callers (the orchestrator polls /flow/status). Lookup-failure
	// path zero-values both fields (the JSON omits them via omitempty).
	cost, contextSize := fr.lookupSessionCost(st.SessionID)

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
		// A step entering Running invalidates a previous postpone —
		// either it's a fresh step or a resume of the postponed one.
		// Either way the run can no longer terminate as postponed.
		state.lastStepPostponed = false
		if state.Status == flowRunWaitingForInput {
			state.Status = flowRunRunning
		}
		fr.publishEvent(state, FlowEvent{
			Type:           evFlowStepStarted,
			RunID:          state.RunID,
			FlowID:         state.FlowID,
			StepID:         rec.ID,
			SessionID:      rec.SessionID,
			StartedAt:      now,
			IsStructOutput: st.IsStructOutput,
			Iteration:      st.Iteration,
			Cost:           cost,
			ContextSize:    contextSize,
		})
	case flow.FlowStatusWaitingForInput:
		// Interactive step transitioned to bound-and-waiting. Per the
		// flow-api spec, emit flow.waiting_for_input carrying the
		// resolved target peers; the snapshot's WaitingTarget reflects
		// the same value until the agent's next turn closes the wait.
		state.waitingTarget = st.WaitingTarget
		state.Status = flowRunWaitingForInput
		fr.publishEvent(state, FlowEvent{
			Type:           evFlowWaitingForInput,
			RunID:          state.RunID,
			FlowID:         state.FlowID,
			StepID:         rec.ID,
			SessionID:      rec.SessionID,
			Target:         st.WaitingTarget,
			IsStructOutput: st.IsStructOutput,
			Iteration:      st.Iteration,
			Cost:           cost,
			ContextSize:    contextSize,
		})
	case flow.FlowStatusCompleted:
		rec.CompletedAt = now
		state.completedSteps = append(state.completedSteps, rec)
		state.currentStep = nil
		state.lastStepPostponed = false
		fr.publishEvent(state, FlowEvent{
			Type:           evFlowStepCompleted,
			RunID:          state.RunID,
			FlowID:         state.FlowID,
			StepID:         rec.ID,
			SessionID:      rec.SessionID,
			Output:         rec.Output,
			CompletedAt:    now,
			IsStructOutput: st.IsStructOutput,
			Iteration:      st.Iteration,
			Cost:           cost,
			ContextSize:    contextSize,
		})
	case flow.FlowStatusPostponed:
		// A step matched a `postpone: true` rule — the row in flow_states
		// was updated to status=postponed and the previous iteration's
		// Output is preserved on it. Emit flow.step.postponed carrying
		// the same per-step extension fields as completed so consumers
		// can render the waiting-for-resume state. Mark the run-level
		// flag so the terminal selector picks flowRunPostponed when the
		// channel drains.
		rec.CompletedAt = now
		state.completedSteps = append(state.completedSteps, rec)
		state.currentStep = nil
		state.lastStepPostponed = true
		fr.publishEvent(state, FlowEvent{
			Type:           evFlowStepPostponed,
			RunID:          state.RunID,
			FlowID:         state.FlowID,
			StepID:         rec.ID,
			SessionID:      rec.SessionID,
			Output:         rec.Output,
			CompletedAt:    now,
			IsStructOutput: st.IsStructOutput,
			Iteration:      st.Iteration,
			Cost:           cost,
			ContextSize:    contextSize,
		})
	case flow.FlowStatusFailed:
		rec.Error = st.Output
		state.completedSteps = append(state.completedSteps, rec)
		state.err = st.Output
		state.lastStepPostponed = false
		fr.publishEvent(state, FlowEvent{
			Type:           evFlowStepFailed,
			RunID:          state.RunID,
			FlowID:         state.FlowID,
			StepID:         rec.ID,
			Error:          rec.Error,
			FailedAt:       now,
			IsStructOutput: st.IsStructOutput,
			Iteration:      st.Iteration,
			Cost:           cost,
			ContextSize:    contextSize,
		})
	}
}

// lookupSessionCost reads cumulative Cost and PromptTokens for the
// session. Returns zero values on any failure (missing session, nil
// service) — callers MUST tolerate that. The warn-log is deduplicated
// per session ID so a flow whose session row is permanently missing
// doesn't flood the log on every step transition.
//
// MUST NOT be called while holding fr.mu — svc.Get is bounded by a
// 250 ms ctx timeout and would re-block concurrent Snapshot() / Abort()
// callers (which is the bug this method's call-site reordering fixed).
// The function itself is safe to call concurrently — it touches only
// fr.app (set once at construction) and the warnedSessions set
// (guarded by its own mutex).
func (fr *flowRunner) lookupSessionCost(sessionID string) (cost float64, contextSize int64) {
	if sessionID == "" {
		return 0, 0
	}
	svc := fr.app.SessionsService()
	if svc == nil {
		return 0, 0
	}
	// Use a short-lived background context: this is a fast in-memory or
	// SQLite read; we don't want to inherit a long-deadline parent.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	sess, err := svc.Get(ctx, sessionID)
	if err != nil {
		if fr.markWarned(sessionID) {
			logging.Warn("flow event: session lookup failed; cost/context fields will be zero",
				"session", sessionID, "err", err)
		}
		return 0, 0
	}
	return sess.Cost, sess.PromptTokens + sess.CompletionTokens
}

// warnedSessionsCap bounds the warn-dedup set. Long-running serve
// processes can encounter many distinct broken session IDs over their
// lifetime; without a cap the map would grow without bound. When the
// set reaches the cap, it is reset — old session IDs will warn again
// on their next failure, which is acceptable degradation (worst case:
// one extra warn per session after wrap, then dedup resumes).
const warnedSessionsCap = 1024

// markWarned returns true the first time a given sessionID's lookup
// failure is recorded, false on subsequent failures. Used by
// lookupSessionCost to log a warn once per session ID. The dedup set
// is capped at warnedSessionsCap entries — see the constant's doc.
func (fr *flowRunner) markWarned(sessionID string) bool {
	fr.warnedMu.Lock()
	defer fr.warnedMu.Unlock()
	if fr.warnedSessions == nil {
		fr.warnedSessions = make(map[string]struct{})
	}
	if _, seen := fr.warnedSessions[sessionID]; seen {
		return false
	}
	if len(fr.warnedSessions) >= warnedSessionsCap {
		fr.warnedSessions = make(map[string]struct{})
	}
	fr.warnedSessions[sessionID] = struct{}{}
	return true
}

// publishEvent emits a FlowEvent on the SSE broker. Caller must hold mu.
func (fr *flowRunner) publishEvent(_ *flowRunState, ev FlowEvent) {
	if fr.broker == nil {
		return
	}
	fr.broker.Publish(pubsub.UpdatedEvent, ev)
}

// finish records the terminal status of a run and emits the matching
// terminal SSE event — flow.completed, flow.postponed, or flow.failed.
// The three events are mutually exclusive; exactly one fires per run.
func (fr *flowRunner) finish(state *flowRunState, status flowRunStatus, errMsg string) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	state.Status = status
	state.completedAt = time.Now().UnixMilli()
	if errMsg != "" {
		state.err = errMsg
	}
	switch status {
	case flowRunCompleted:
		fr.publishEvent(state, FlowEvent{
			Type:        evFlowCompleted,
			RunID:       state.RunID,
			FlowID:      state.FlowID,
			CompletedAt: state.completedAt,
		})
	case flowRunPostponed:
		fr.publishEvent(state, FlowEvent{
			Type:        evFlowPostponed,
			RunID:       state.RunID,
			FlowID:      state.FlowID,
			CompletedAt: state.completedAt,
		})
	default:
		fr.publishEvent(state, FlowEvent{
			Type:     evFlowFailed,
			RunID:    state.RunID,
			FlowID:   state.FlowID,
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
type flowAppAdapter struct {
	get        func() flow.Service
	getSession func() session.Service
}

func (a flowAppAdapter) FlowsService() flow.Service { return a.get() }
func (a flowAppAdapter) SessionsService() session.Service {
	if a.getSession == nil {
		return nil
	}
	return a.getSession()
}

// newFlowRunner constructs the singleton runner. cmd/serve.go indirectly
// invokes this through NewServer; tests can construct one directly via
// newFlowRunner and override validateFlowID afterwards.
//
// The validator defaults to flow.Get-based existence check, which in
// production reads .opencode/flows/* via the config-dependent registry.
// Tests that drive a stub flow.Service set fr.validateFlowID = nil to
// skip the check (synthetic flow IDs don't appear in any YAML).
func newFlowRunner(svc flow.Service) *flowRunner {
	return newFlowRunnerWithSessions(svc, nil)
}

// newFlowRunnerWithSessions constructs the runner with both the flow
// service and the session service. NewServer uses this so per-step
// FlowEvent payloads can include the running Cost / ContextSize via
// session.Service.Get. Tests can pass nil for the session service when
// they don't need cost/context fields populated.
func newFlowRunnerWithSessions(svc flow.Service, sessions session.Service) *flowRunner {
	return &flowRunner{
		broker: pubsub.NewBroker[FlowEvent](),
		app: flowAppAdapter{
			get:        func() flow.Service { return svc },
			getSession: func() session.Service { return sessions },
		},
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
// reaches a terminal status (completed | failed), then waits `grace`
// for any external reconciliation reader (e.g. an orchestrator calling
// GET /flow/status), then invokes onTerminal. Used by --flow-exit to
// trigger process shutdown after the auto-started flow finishes.
//
// The grace window deliberately holds the HTTP server up after terminal
// so the orchestrator's opportunistic reconciliation read doesn't race
// the pod's shutdown — see openspec change c2-agent-flow-http-migration
// design.md R3. A SIGTERM (parent ctx cancellation) during the grace
// short-circuits the wait to honor explicit shutdown intent.
func (s *Server) WaitFlowTerminal(ctx context.Context, runID string, grace time.Duration, onTerminal context.CancelFunc) {
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
			if snap.Status == flowRunCompleted || snap.Status == flowRunFailed || snap.Status == flowRunPostponed {
				logging.Info("auto-flow terminal — holding for reconciliation grace",
					"flow", snap.FlowID, "runID", runID, "status", snap.Status, "grace", grace)
				if grace > 0 {
					select {
					case <-ctx.Done():
						// Parent shutdown overrides grace.
					case <-time.After(grace):
					}
				}
				logging.Info("auto-flow grace elapsed — exiting", "runID", runID)
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
