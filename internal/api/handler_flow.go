package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/opencode-ai/opencode/internal/flow"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/agent/mcpauthctx"
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

// isTerminalStatus reports whether a run status is terminal (completed,
// failed, or postponed). Non-terminal statuses are running and
// waiting_for_input — pool mode treats BOTH as in-flight for the POST
// /flow, POST /pool/bind, and POST /flow/recycle guards (openspec
// change agent-pod-pool-runtime, design D8).
func isTerminalStatus(s flowRunStatus) bool {
	switch s {
	case flowRunCompleted, flowRunFailed, flowRunPostponed:
		return true
	default:
		return false
	}
}

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
	// evFlowStepRetrying marks the engine spending its one bounded re-prompt
	// on a schema-bearing step whose agent turn ended with no struct_output
	// call. The step is still running — this is a progress signal, not a
	// terminal transition — so orchestrators can distinguish "recovering" from
	// "hung" instead of inferring it from timing. Output carries the reason.
	evFlowStepRetrying flowEventType = "flow.step.retrying"
	evFlowCompleted    flowEventType = "flow.completed"
	evFlowFailed       flowEventType = "flow.failed"
	evFlowPostponed    flowEventType = "flow.postponed"
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

	// bridgeJobs, when non-nil, is the bridge service. Used to rebind the
	// pod's orchestrator job identity per run (POST /flow bridgeJobID).
	// Nil when the pod runs without a bridge.
	bridgeJobs bridgeJobScoper

	// mcpDiscovery, when non-nil, is the MCP registry. Used to publish the
	// run's Authorization override to registry-owned tool discovery.
	mcpDiscovery mcpDiscoveryAuthSetter

	// stepAgents, when non-nil, is the agent factory. Its per-step agent
	// cache is keyed on the flow YAML's step ID, which recurs across runs,
	// so a process serving many runs must drop it between them.
	stepAgents stepAgentCacheResetter

	// --- Pool-mode state (openspec change agent-pod-pool-runtime) ---

	// poolMode gates every pool-only behaviour below. Set once at
	// construction (NewServer, from --pool-mode); never mutated after.
	// When false the runner behaves exactly as before the pool change:
	// in-flight guard keyed on Status == running only, terminal snapshot
	// retained until process exit, no idle reset.
	poolMode bool

	// idleResetGrace is how long a terminal snapshot is retained before
	// the runner clears currentRun to nil so GET /flow/status returns
	// {"status":"idle"} (--flow-idle-reset-grace, default 5s). Only
	// consulted when poolMode is true. Zero means the reset happens
	// synchronously in finish().
	idleResetGrace time.Duration

	// idleTimer is the pending terminal→idle reset timer scheduled by
	// finish(). Guarded by mu. Start stops and clears it so a new run
	// arriving inside the grace window cancels the previous run's reset.
	idleTimer *time.Timer

	// draining, when non-nil, is shared with the API server's recycle
	// state. Start refuses new runs (errPodDraining) while it is set so
	// a POST /flow racing POST /flow/recycle can't slip past the
	// handler-level draining gate. Only consulted when poolMode is true.
	draining *atomic.Bool

	// binding, when non-nil, is shared with the API server's POST
	// /pool/bind state. Start refuses new runs (errPodBinding) while it
	// is set: an accepted bind has already armed the process-exit timer,
	// so a run started inside the exit grace would be killed mid-flight.
	// Only consulted when poolMode is true.
	binding *atomic.Bool

	// terminalRing retains the last few terminal snapshots so a run
	// whose live snapshot has already been idle-reset stays addressable
	// via GET /flow/status?runID=<id>. Without it, a run that reached
	// terminal while the orchestrator was restarting became
	// unrecoverable: the reconnect saw {"status":"idle"}, could not tell
	// "finished" from "never started", and drove an SSE stream against
	// an idle pod until the job deadline. Guarded by mu; pool mode only
	// (non-pool retains currentRun until process exit, so the live
	// snapshot already answers).
	terminalRing []*flowRunSnapshot

	// runCount counts every run STARTED via Start since process boot,
	// regardless of outcome (completed, failed, postponed, aborted).
	// Guarded by mu. Surfaced as pool.runCount in /global/health.
	runCount int64

	// lastTerminalAt is the unix-ms timestamp of the most recent terminal
	// transition (finish call), zero when no run has terminated yet.
	// Guarded by mu. Surfaced as pool.lastTerminalAt in /global/health.
	lastTerminalAt int64
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

	// done is closed by finish() when this run reaches terminal. DELETE
	// /flow waits on it (bounded) in pool mode so the 200 means "the
	// runner is ready for the next POST /flow", which is what the pool
	// contract promises and what the orchestrator's abort-then-reclaim
	// sequence relies on. Created in Start; never nil for a started run.
	done chan struct{}

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

	// setBridgeJobID / setDiscoveryAuth record which process-level
	// singletons THIS run actually replaced, so finish() reverts exactly
	// those and no others. They are deliberately separate flags: the two
	// POST /flow fields are independent on the wire, and a run that
	// carried only one of them must not revert the other. A run that
	// carried neither leaves both boot-time values alone — the per-Job
	// pod's path, byte-identical to before this change.
	setBridgeJobID   bool
	setDiscoveryAuth bool
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
//
// Pool mode (agent-pod-pool-runtime) adds gates in front of the start:
// 503 while draining, 400 while the pod is unbound, 409 when the body's
// optional `workspace` field mismatches the bound workspace, and 400
// when mcpAuth is set without mcpAuthServer. The error bodies use the
// spec's {"error": ...} wire shape (writePoolError) — the orchestrator's
// pool controller parses them.
func (s *Server) handleFlowStart(w http.ResponseWriter, r *http.Request) {
	if s.flowRunner == nil {
		writeError(w, http.StatusServiceUnavailable, "flow runner not configured")
		return
	}
	if s.poolMode && s.poolDraining.Load() {
		writePoolError(w, http.StatusServiceUnavailable, "pod draining", nil)
		return
	}
	if s.poolMode && s.poolBinding.Load() {
		// Ahead of the unbound check below on purpose: a pod inside its
		// bind exit grace IS unbound, but "pod not bound" (400) reads as a
		// protocol error the caller got wrong, whereas 503 tells it the
		// bind it just issued is still landing and to wait for the
		// respawn.
		writePoolError(w, http.StatusServiceUnavailable, "pod binding; exiting for respawn", nil)
		return
	}
	var body struct {
		FlowID string         `json:"flowID"`
		Args   map[string]any `json:"args"`
		Fresh  bool           `json:"fresh"`
		// MCPAuth carries a job-scoped bearer token applied to the named
		// MCP server's calls for the duration of this run only (design D1).
		MCPAuth string `json:"mcpAuth"`
		// MCPAuthServer names the MCP server whose Authorization header
		// MCPAuth overrides. REQUIRED when MCPAuth is set — no default-by-
		// name, workspaces are free to rename the server in their config.
		MCPAuthServer string `json:"mcpAuthServer"`
		// Workspace is an optional defence-in-depth assertion of the git
		// URL the caller believes this pod is bound to (pool mode only).
		Workspace string `json:"workspace"`
		// BridgeJobID is the orchestrator job identity this run's bridge
		// registrations and outbound relay frames are stamped with
		// (design D9). Per-Job pods omit it and keep using the boot-time
		// OPENCODE_BRIDGE_JOB_ID env var; a pool pod has no boot-time job,
		// so without this every interactive step registers nothing and the
		// reviewer's reply has no route back to the pod.
		BridgeJobID string `json:"bridgeJobID"`
	}
	if err := readJSON(r, &body); err != nil && !isEmptyBodyError(err) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.FlowID == "" {
		writeError(w, http.StatusBadRequest, "flowID is required")
		return
	}
	if body.MCPAuth != "" && body.MCPAuthServer == "" {
		writePoolError(w, http.StatusBadRequest, "mcpAuthServer required when mcpAuth is set", nil)
		return
	}
	if s.poolMode {
		if s.poolBoundWorkspace == "" {
			writePoolError(w, http.StatusBadRequest, "pod not bound; call POST /pool/bind first", nil)
			return
		}
		if body.Workspace != "" && normalizeWorkspaceURL(body.Workspace) != s.poolBoundWorkspace {
			writePoolError(w, http.StatusConflict,
				fmt.Sprintf("pod bound to %s; workspace param doesn't match", s.poolBoundWorkspace),
				map[string]any{"boundWorkspace": s.poolBoundWorkspace})
			return
		}
	}

	result, err := s.flowRunner.StartWithOptions(r.Context(), body.FlowID, body.Args, body.Fresh, flowStartOptions{
		mcpAuth:       body.MCPAuth,
		mcpAuthServer: body.MCPAuthServer,
		bridgeJobID:   body.BridgeJobID,
	})
	switch {
	case errors.Is(err, errFlowAlreadyRunning):
		writeError(w, http.StatusConflict, "another flow is already running")
	case errors.Is(err, errPodDraining):
		writePoolError(w, http.StatusServiceUnavailable, "pod draining", nil)
	case errors.Is(err, errPodBinding):
		writePoolError(w, http.StatusServiceUnavailable, "pod binding; exiting for respawn", nil)
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
//
// The optional ?runID=<id> query parameter asks for a SPECIFIC run
// instead of "whatever is current". That distinction matters on a pool
// pod, where the idle reset clears the live snapshot after
// --flow-idle-reset-grace: an orchestrator that was restarting when the
// run finished would otherwise read {"status":"idle"} and be unable to
// tell "this run completed" from "nothing ever ran here". With a runID
// the pod answers from its terminal-snapshot ring, or 404s so the caller
// fails fast instead of driving an event stream against an idle pod.
func (s *Server) handleFlowStatus(w http.ResponseWriter, r *http.Request) {
	if s.flowRunner == nil {
		writeError(w, http.StatusServiceUnavailable, "flow runner not configured")
		return
	}
	if runID := r.URL.Query().Get("runID"); runID != "" {
		snap := s.flowRunner.SnapshotByRunID(runID)
		if snap == nil {
			writePoolError(w, http.StatusNotFound, "unknown runID",
				map[string]any{"runID": runID})
			return
		}
		writeJSON(w, http.StatusOK, snap)
		return
	}
	snap := s.flowRunner.Snapshot()
	if snap == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "idle"})
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// abortSettleTimeout bounds how long DELETE /flow waits for the aborted
// run to actually reach terminal (pool mode only). Sized under the
// orchestrator's own 10s abort context so the wait can never be the
// thing that trips its client timeout.
const abortSettleTimeout = 5 * time.Second

// handleFlowAbort cancels the in-flight run.
//
// In pool mode the response is held (bounded by abortSettleTimeout)
// until the run has actually settled, and reports whether it did. A pool
// pod is reclaimed by the orchestrator the moment abort returns, so
// answering 200 while the engine is still unwinding hands back a pod
// that still 409s POST /flow — which the orchestrator reads as inventory
// desync and poisons a healthy pod for. The contract this honours is the
// change's own: "a flow termination (... or DELETE /flow) MUST leave the
// runner in a state where the next POST /flow succeeds".
//
// Non-pool mode is unchanged: it answers immediately, exactly as before.
func (s *Server) handleFlowAbort(w http.ResponseWriter, r *http.Request) {
	if s.flowRunner == nil {
		writeError(w, http.StatusServiceUnavailable, "flow runner not configured")
		return
	}
	runID, ok := s.flowRunner.Abort()
	if !ok {
		writeError(w, http.StatusConflict, "no flow is running")
		return
	}
	if !s.poolMode {
		writeJSON(w, http.StatusOK, map[string]any{"aborted": true})
		return
	}
	terminal := s.flowRunner.AwaitTerminal(r.Context(), runID, abortSettleTimeout)
	if !terminal {
		logging.Warn("pool: aborted run did not settle within the abort grace",
			"runID", runID, "grace", abortSettleTimeout)
	}
	writeJSON(w, http.StatusOK, map[string]any{"aborted": true, "terminal": terminal})
}

// errFlowAlreadyRunning is the sentinel returned by Start when another
// run is in flight.
var errFlowAlreadyRunning = errors.New("flow: another run is already in flight")

// errPodDraining is the sentinel returned by Start when the pod is
// draining after POST /flow/recycle (pool mode only). Checked under
// fr.mu so a POST /flow racing a recycle can't start a run after the
// recycle's own in-flight check observed the runner idle.
var errPodDraining = errors.New("flow: pod draining")

// errPodBinding is the sentinel returned by Start when POST /pool/bind
// has been accepted and the process-exit timer is armed (pool mode
// only). Checked under fr.mu, paired with latchExclusive, so a POST
// /flow racing a bind can't start a run on a process that is seconds
// from exiting for its workspace-clone respawn.
var errPodBinding = errors.New("flow: pod binding")

// flowStartOptions carries the optional per-run parameters POST /flow
// accepts in pool deployments (agent-pod-pool-runtime, design D1).
// Zero value = today's behaviour exactly.
type flowStartOptions struct {
	// mcpAuth is the job-scoped bearer token; empty means no override.
	mcpAuth string
	// mcpAuthServer names the MCP server the override applies to. The
	// HTTP handler enforces "required when mcpAuth is set" before Start.
	mcpAuthServer string
	// bridgeJobID is the orchestrator job identity this run's bridge
	// registrations are grouped under; empty leaves the process-level
	// (boot env) identity untouched, which is the per-Job pod's path.
	bridgeJobID string
}

// bridgeJobScoper is the narrow slice of the bridge service the flow
// runner needs: rebind the pod's orchestrator job identity for the run
// about to start. Declared here (rather than importing the bridge
// service) for the same reason RouteRegistrar and HealthReporter are —
// internal/api must not depend on internal/bridge/service.
type bridgeJobScoper interface {
	SetRemoteJobID(jobID string)
}

// mcpDiscoveryAuthSetter is the narrow slice of the MCP registry the
// flow runner needs: record the Authorization overrides that
// registry-owned tool DISCOVERY applies. Tool calls take theirs from the
// run context; discovery is context-free by design and cannot.
type mcpDiscoveryAuthSetter interface {
	SetDiscoveryAuth(overrides map[string]string)
}

// stepAgentCacheResetter is the narrow slice of the agent factory the
// flow runner needs: drop the per-step agent memoisation between runs.
type stepAgentCacheResetter interface {
	ResetStepCache()
}

// foldMCPServerName canonicalises an MCP server name the way config
// loading does. Viper lower-cases map keys read from .opencode.json, so
// `mcpServers.My-Orchestrator` is stored as `my-orchestrator` and every
// lookup — StartClient's, resolveMCPHeaders' — uses the folded form. An
// override keyed on the raw wire value would therefore never be found
// for any server whose configured name has an uppercase letter, and the
// failure is silent: discovery falls back to the boot-time header, 401s,
// and the tools are simply absent.
func foldMCPServerName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

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
	return fr.StartWithOptions(parent, flowID, args, fresh, flowStartOptions{})
}

// StartWithOptions is Start with the optional pool-mode per-run
// parameters (per-flow MCP auth). The in-flight guard differs by mode:
//
//   - non-pool (per-Job / daemon): a run is in flight only while its
//     Status == running — today's behaviour, preserved exactly. A run
//     sitting in waiting_for_input is replaced by the next Start.
//   - pool: ANY non-terminal status (running, waiting_for_input) is
//     in flight and returns errFlowAlreadyRunning (design D8) — a run
//     waiting on a reviewer answer must not be clobbered by an
//     inventory-desynced orchestrator.
func (fr *flowRunner) StartWithOptions(parent context.Context, flowID string, args map[string]any, fresh bool, opts flowStartOptions) (StartResult, error) {
	if fr.validateFlowID != nil {
		if err := fr.validateFlowID(flowID); err != nil {
			return StartResult{}, err
		}
	}
	fr.mu.Lock()
	if fr.poolMode && fr.draining != nil && fr.draining.Load() {
		fr.mu.Unlock()
		return StartResult{}, errPodDraining
	}
	if fr.poolMode && fr.binding != nil && fr.binding.Load() {
		fr.mu.Unlock()
		return StartResult{}, errPodBinding
	}
	if fr.currentRun != nil {
		inFlight := fr.currentRun.Status == flowRunRunning
		if fr.poolMode {
			inFlight = !isTerminalStatus(fr.currentRun.Status)
		}
		if inFlight {
			fr.mu.Unlock()
			return StartResult{}, errFlowAlreadyRunning
		}
	}
	// A new run cancels any pending terminal→idle reset for the previous
	// run — the new state replaces the snapshot immediately.
	if fr.idleTimer != nil {
		fr.idleTimer.Stop()
		fr.idleTimer = nil
	}
	fr.runCount++
	runID := uuid.NewString()
	// Inject the per-run MCP auth override BEFORE deriving the cancelable
	// run context so cancellation (DELETE /flow, natural terminal) bounds
	// the override's reach: tool calls under runCtx carry it, everything
	// else never sees it (design D1).
	base := context.Background()
	if opts.mcpAuth != "" {
		base = mcpauthctx.WithAuthOverride(base, foldMCPServerName(opts.mcpAuthServer), "Bearer "+opts.mcpAuth)
	}
	runCtx, cancel := context.WithCancel(base)
	state := &flowRunState{
		RunID:     runID,
		FlowID:    flowID,
		StartedAt: time.Now().UnixMilli(),
		Status:    flowRunRunning,
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	// Track each singleton SEPARATELY. A single OR'd flag would have a run
	// that carried only mcpAuth clear the bridge identity it never set —
	// permanently wiping a per-Job pod's boot-time OPENCODE_BRIDGE_JOB_ID.
	//
	// The bridge identity is additionally gated on pool mode. A per-Job or
	// daemon pod's identity comes from OPENCODE_BRIDGE_JOB_ID at boot and
	// is the only one it will ever have; honouring a body field there
	// would let any caller replace it and, at terminal, clear it for the
	// rest of the process's life. Pool pods have no boot identity, which
	// is exactly why they need the field.
	state.setBridgeJobID = fr.poolMode && opts.bridgeJobID != "" && fr.bridgeJobs != nil
	state.setDiscoveryAuth = opts.mcpAuth != "" && opts.mcpAuthServer != "" && fr.mcpDiscovery != nil
	fr.currentRun = state
	// Publish the run-scoped process identity while STILL holding fr.mu,
	// and revert under it too (clearRunScopedIdentity takes the lock), so
	// a run's publish and a previous run's revert can never interleave.
	// Neither the bridge service nor the MCP registry calls back into the
	// flow runner, so there is no lock cycle to invert.
	fr.applyRunScopedIdentity(state, opts)
	result := StartResult{
		RunID:     state.RunID,
		FlowID:    state.FlowID,
		Status:    state.Status,
		StartedAt: state.StartedAt,
	}
	fr.mu.Unlock()
	_ = parent

	// Drop the per-step agent cache so this run builds fresh agents. The
	// cache is keyed on the flow YAML's step ID, which recurs across runs;
	// reusing run N-1's agents would also reuse their once-resolved MCP
	// toolsets, so a run whose discovery failed would poison every later
	// run on this process.
	if fr.stepAgents != nil {
		fr.stepAgents.ResetStepCache()
	}

	// Kick off the run in the background; SSE consumers see progress
	// via fr.broker.
	go fr.run(runCtx, state, flowID, args, fresh)
	return result, nil
}

// applyRunScopedIdentity publishes this run's bridge job identity and MCP
// discovery auth override to the process-level singletons that need them.
// A zero-valued option leaves the corresponding singleton untouched, so a
// per-Job pod (which sets neither) keeps its boot-time env identity.
// Caller must hold fr.mu.
func (fr *flowRunner) applyRunScopedIdentity(state *flowRunState, opts flowStartOptions) {
	if state.setBridgeJobID {
		fr.bridgeJobs.SetRemoteJobID(opts.bridgeJobID)
	}
	if state.setDiscoveryAuth {
		fr.mcpDiscovery.SetDiscoveryAuth(map[string]string{
			foldMCPServerName(opts.mcpAuthServer): "Bearer " + opts.mcpAuth,
		})
	}
	// A run that carries NO identity must positively clear whatever the
	// previous run left rather than inherit it. The terminal revert alone
	// cannot guarantee that: it is suppressed once another run is current,
	// which is precisely this case.
	//
	// The MCP discovery override is cleared in EVERY mode. It has no
	// boot-time source — the flow runner is its only writer — so there is
	// nothing to preserve, and not clearing it leaks one run's job-scoped
	// bearer token into every later run on the process. That is reachable
	// outside pool mode too: a non-pool runner lets a new Start replace a
	// run parked in waiting_for_input, so run B inherits run A's token and
	// A's suppressed revert never takes it back.
	if !state.setDiscoveryAuth && fr.mcpDiscovery != nil {
		fr.mcpDiscovery.SetDiscoveryAuth(nil)
	}
	// The bridge identity is pool-only, because outside pool mode the
	// boot-time OPENCODE_BRIDGE_JOB_ID is the only identity the process
	// will ever have and clearing it is unrecoverable.
	if fr.poolMode && !state.setBridgeJobID && fr.bridgeJobs != nil {
		fr.bridgeJobs.SetRemoteJobID("")
	}
}

// clearRunScopedIdentity reverts what applyRunScopedIdentity published.
// Called after finish() releases fr.mu (registered as the outermost
// defer) so the bridge's and registry's own locks are never taken while
// holding the runner's.
func (fr *flowRunner) clearRunScopedIdentity(state *flowRunState) {
	if state == nil || (!state.setBridgeJobID && !state.setDiscoveryAuth) {
		return
	}
	// Only the run that is STILL current may clear. Both the publish (in
	// Start, after fr.mu is released) and this revert (finish's outermost
	// defer, likewise after release) happen outside the runner lock, so
	// nothing otherwise orders them: run A's goroutine could be
	// descheduled between finish() releasing fr.mu and this defer firing,
	// during which the orchestrator sees flow.completed, claims the pod
	// and starts run B — and A's clear would then wipe B's identity for
	// B's whole life. The pointer-identity check makes a stale clear a
	// no-op; B's own clear still runs when B finishes.
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if fr.currentRun != nil && fr.currentRun != state {
		// A newer run is current. It published its own values (or, in pool
		// mode, positively cleared them) under this same lock, so reverting
		// here would clobber them. Held across the setter calls, not just
		// the read, so there is no window between deciding and acting.
		return
	}
	if state.setBridgeJobID {
		fr.bridgeJobs.SetRemoteJobID("")
	}
	if state.setDiscoveryAuth {
		fr.mcpDiscovery.SetDiscoveryAuth(nil)
	}
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
	case flow.FlowStatusRetrying:
		// In-flight only: the step stays `running` from the consumer's point
		// of view, so deliberately do NOT touch currentStep / completedSteps /
		// state.Status / waitingTarget. An interactive step that is being
		// re-prompted is still waiting on its reviewer, and clearing
		// waitingTarget here would drop that from /flow/status.
		fr.publishEvent(state, FlowEvent{
			Type:        evFlowStepRetrying,
			RunID:       state.RunID,
			FlowID:      state.FlowID,
			StepID:      rec.ID,
			SessionID:   rec.SessionID,
			Output:      rec.Output,
			Iteration:   st.Iteration,
			Cost:        cost,
			ContextSize: contextSize,
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
//
// finish is the convergence point for EVERY termination cause (natural
// terminal, abort via DELETE /flow, start failure) — design D7 — so the
// pool-mode bookkeeping (lastTerminalAt, terminal→idle reset) lives
// here and nowhere else.
func (fr *flowRunner) finish(state *flowRunState, status flowRunStatus, errMsg string) {
	// Registered FIRST so it runs LAST (defers are LIFO) — i.e. after
	// fr.mu is released. The bridge and MCP registry take their own
	// locks; taking them under fr.mu is how lock-order inversions start.
	defer fr.clearRunScopedIdentity(state)
	fr.mu.Lock()
	defer fr.mu.Unlock()
	state.Status = status
	state.completedAt = time.Now().UnixMilli()
	fr.lastTerminalAt = state.completedAt
	if errMsg != "" {
		state.err = errMsg
	}
	// Cancel the run context on EVERY terminal path, not only on abort.
	// This releases the context's resources and bounds the per-run MCP
	// auth override's reach (agent-pod-pool-runtime D1): once a run is
	// terminal, nothing can make MCP calls under its runCtx. finish is
	// only ever reached after flow.Service.Run's channels closed (or
	// never opened), so no in-flight engine work observes the cancel.
	if state.cancel != nil {
		state.cancel()
	}
	if fr.poolMode {
		fr.retainTerminalLocked(state)
		fr.scheduleIdleResetLocked(state)
	}
	// Unblock any DELETE /flow waiting for this run to actually settle.
	// finish() runs at most once per run (the terminal selector in run()
	// is a switch, and the abort path returns immediately after), so the
	// close is not guarded — but be defensive: a double close would panic
	// the runner goroutine and take the pod down.
	if state.done != nil {
		select {
		case <-state.done:
		default:
			close(state.done)
		}
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

// terminalRingSize bounds how many terminal snapshots stay addressable
// by runID after the idle reset clears the live one.
//
// One entry would cover "the orchestrator restarted during my run" only
// if nothing else ran on the pod before the reconnect — but the
// orchestrator's startup reconciliation frees a pod as soon as it reports
// available, roughly a minute before its orphan-reclaim loop gets to the
// job that was on it, so a couple of interleaved runs is the expected
// case rather than the exception. The depth is the recovery margin for
// exactly that window. Each entry holds that run's completed-step
// outputs, so it stays modest.
const terminalRingSize = 8

// retainTerminalLocked appends the just-finished run's snapshot to the
// retention ring, evicting the oldest when full. Caller must hold fr.mu.
//
// The ring is what makes a terminal run survivable across the idle
// reset. GET /flow/status (no runID) still follows the spec exactly —
// live snapshot for the grace window, then {"status":"idle"} — but
// GET /flow/status?runID=<id> keeps answering for a few runs, so an
// orchestrator that was restarting when the run finished can recover
// the result instead of driving a stream against an idle pod.
func (fr *flowRunner) retainTerminalLocked(state *flowRunState) {
	snap := snapshotOf(state)
	if snap == nil {
		return
	}
	// Replace in place on the (impossible-in-practice) repeat, so a ring
	// lookup never returns a stale projection of the same run.
	for i, existing := range fr.terminalRing {
		if existing.RunID == snap.RunID {
			fr.terminalRing[i] = snap
			return
		}
	}
	fr.terminalRing = append(fr.terminalRing, snap)
	if len(fr.terminalRing) > terminalRingSize {
		fr.terminalRing = fr.terminalRing[len(fr.terminalRing)-terminalRingSize:]
	}
}

// SnapshotByRunID returns the projection for a specific run: the live
// run when it matches, otherwise a retained terminal snapshot. Nil when
// this process has no record of that run — which the HTTP layer turns
// into a 404 so the caller can fail fast rather than misread it as
// "idle, nothing ever ran here".
func (fr *flowRunner) SnapshotByRunID(runID string) *flowRunSnapshot {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if fr.currentRun != nil && fr.currentRun.RunID == runID {
		return snapshotOf(fr.currentRun)
	}
	for _, snap := range fr.terminalRing {
		if snap.RunID == runID {
			clone := *snap
			clone.CompletedSteps = append([]flowStepRecord(nil), snap.CompletedSteps...)
			clone.CurrentStep = cloneStepRecordPtr(snap.CurrentStep)
			return &clone
		}
	}
	return nil
}

// LastTerminal returns the runID and status of the most recently
// finished run, or ("", "") when none has finished in this process.
// Surfaced in the pool health block so the orchestrator can tell
// "finished while I was away" from "never started" without a status
// call. Thread-safe.
func (fr *flowRunner) LastTerminal() (string, flowRunStatus) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.terminalRing) == 0 {
		return "", ""
	}
	last := fr.terminalRing[len(fr.terminalRing)-1]
	return last.RunID, last.Status
}

// latchResult reports the outcome of latchExclusive.
type latchResult int

const (
	// latchAcquired: the gate was clear, no run was in flight, and the
	// gate is now set.
	latchAcquired latchResult = iota
	// latchInFlight: a non-terminal run exists; the gate is untouched.
	latchInFlight
	// latchAlreadySet: the gate was already set by an earlier caller.
	latchAlreadySet
)

// latchExclusive sets a pod-level gate (draining / binding) if and only
// if no run is in flight, doing both under fr.mu so a POST /flow cannot
// interleave between the in-flight check and the latch. Start reads the
// same gates under fr.mu, so the two are strictly ordered: either the
// run was already visible as in-flight (latchInFlight), or the gate is
// visible to Start (errPodDraining / errPodBinding).
//
// This replaces the earlier latch-then-check-then-roll-back shape, whose
// rollback window let a second concurrent request observe the gate set,
// answer 202, and then have the first request clear it — leaving the
// caller believing the pod was draining when nothing had been scheduled.
func (fr *flowRunner) latchExclusive(gate *atomic.Bool) latchResult {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if gate.Load() {
		return latchAlreadySet
	}
	if fr.currentRun != nil && !isTerminalStatus(fr.currentRun.Status) {
		return latchInFlight
	}
	gate.Store(true)
	return latchAcquired
}

// unlatch clears a gate under fr.mu, pairing with latchExclusive so the
// clear is ordered against Start's read.
func (fr *flowRunner) unlatch(gate *atomic.Bool) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	gate.Store(false)
}

// AwaitTerminal blocks until the given run reaches terminal, ctx is
// done, or the timeout elapses. Reports whether the run is terminal on
// return. A run this process never started (or already forgot) counts as
// terminal — there is nothing left to wait for.
func (fr *flowRunner) AwaitTerminal(ctx context.Context, runID string, timeout time.Duration) bool {
	fr.mu.Lock()
	state := fr.currentRun
	fr.mu.Unlock()
	if state == nil || state.RunID != runID || state.done == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-state.done:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

// scheduleIdleResetLocked arms the terminal→idle transition for a run
// that just finished (pool mode only — the caller gates on fr.poolMode).
// After idleResetGrace the terminal snapshot is cleared so GET
// /flow/status returns {"status":"idle"} and the pool controller sees
// the pod as claimable. Grace zero clears synchronously. The pointer-
// identity guard means a run started inside the grace window (which
// replaces currentRun AND stops the timer in Start) can never be
// clobbered by a stale timer firing late. Caller must hold fr.mu.
func (fr *flowRunner) scheduleIdleResetLocked(state *flowRunState) {
	if fr.idleResetGrace <= 0 {
		if fr.currentRun == state {
			fr.currentRun = nil
		}
		return
	}
	if fr.idleTimer != nil {
		fr.idleTimer.Stop()
	}
	fr.idleTimer = time.AfterFunc(fr.idleResetGrace, func() {
		fr.mu.Lock()
		defer fr.mu.Unlock()
		if fr.currentRun == state && isTerminalStatus(fr.currentRun.Status) {
			fr.currentRun = nil
			fr.idleTimer = nil
		}
	})
}

// Abort cancels the in-flight run. Returns true if a run was cancelled.
//
// Non-pool mode preserves today's behaviour exactly: only a run with
// Status == running is abortable. Pool mode extends the definition of
// "in flight" to any non-terminal status (running, waiting_for_input) —
// consistent with the pool-mode POST /flow guard, and required so the
// orchestrator's documented recycle remedy ("on 409, DELETE /flow first
// and retry") works for a run parked on a reviewer question.
func (fr *flowRunner) Abort() (string, bool) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if fr.currentRun == nil {
		return "", false
	}
	inFlight := fr.currentRun.Status == flowRunRunning
	if fr.poolMode {
		inFlight = !isTerminalStatus(fr.currentRun.Status)
	}
	if !inFlight {
		return "", false
	}
	if fr.currentRun.cancel != nil {
		fr.currentRun.cancel()
	}
	return fr.currentRun.RunID, true
}

// InFlight reports whether a non-terminal run (running or
// waiting_for_input) exists. Used by the pool endpoints' guards (POST
// /pool/bind → 400, POST /flow/recycle → 409); both treat
// waiting_for_input as in-flight per design D8.
func (fr *flowRunner) InFlight() bool {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return fr.currentRun != nil && !isTerminalStatus(fr.currentRun.Status)
}

// RunCount returns the number of runs started via Start since process
// boot, regardless of outcome. Thread-safe.
func (fr *flowRunner) RunCount() int64 {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return fr.runCount
}

// LastTerminalAt returns the unix-ms timestamp of the most recent
// terminal transition, or zero when no run has terminated. Thread-safe.
func (fr *flowRunner) LastTerminalAt() int64 {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return fr.lastTerminalAt
}

// CurrentRunID returns the runID of the in-flight (non-terminal) run,
// or "" when the runner is idle or retaining a terminal snapshot.
// Thread-safe.
func (fr *flowRunner) CurrentRunID() string {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if fr.currentRun == nil || isTerminalStatus(fr.currentRun.Status) {
		return ""
	}
	return fr.currentRun.RunID
}

// Snapshot returns the current run's projection. Nil when no run has
// been started in this process.
func (fr *flowRunner) Snapshot() *flowRunSnapshot {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return snapshotOf(fr.currentRun)
}

// snapshotOf projects a run state into the /flow/status wire shape,
// deep-copying the mutable step slices so the caller can never observe
// a later mutation. Caller must hold fr.mu (or own the state outright).
func snapshotOf(state *flowRunState) *flowRunSnapshot {
	if state == nil {
		return nil
	}
	cs := make([]flowStepRecord, len(state.completedSteps))
	copy(cs, state.completedSteps)
	return &flowRunSnapshot{
		RunID:          state.RunID,
		FlowID:         state.FlowID,
		Status:         state.Status,
		StartedAt:      state.StartedAt,
		CompletedAt:    state.completedAt,
		CurrentStep:    cloneStepRecordPtr(state.currentStep),
		CompletedSteps: cs,
		WaitingTarget:  state.waitingTarget,
		Error:          state.err,
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
