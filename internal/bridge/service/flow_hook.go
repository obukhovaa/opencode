package service

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/logging"
)

// sameCancelFunc returns true when two context.CancelFunc values
// refer to the same underlying function. CancelFunc is a func type,
// so equality requires reflect pointer comparison.
func sameCancelFunc(a, b context.CancelFunc) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// remoteRegisterRetryInterval is how often the bridge retries a
// failed Register call against the orchestrator while the
// interactive step is still active. 5min matches the openspec
// "Phase F retry every 5min while the step is active" contract —
// frequent enough that a brief orchestrator restart heals
// automatically; sparse enough that a wedged orchestrator doesn't
// flood logs.
//
// var rather than const so package-internal tests can tighten the
// loop without sleeping for minutes.
var remoteRegisterRetryInterval = 5 * time.Minute

// InteractiveHook satisfies flow.InteractiveHook so the flow engine can
// auto-bind / auto-unbind a session to chat-platform peers at the
// boundaries of an interactive: true step.
//
// Implementation note: this type lives in internal/bridge/service rather
// than internal/flow because it needs Service.Bind / Service.Unbind. The
// flow package declares the interface and only depends on
// internal/bridge for the PeerRef type — no cycle.
type interactiveBridge struct {
	svc *Service

	// retryMu guards retryCancels — the per-session cancel funcs
	// for outstanding remote-register retry goroutines. When the
	// step completes (OnInteractiveStepComplete) we cancel any
	// in-flight retry for that session so the goroutine exits
	// promptly.
	retryMu      sync.Mutex
	retryCancels map[string]context.CancelFunc
}

// InteractiveHook returns a flow.InteractiveHook-compatible value
// bound to this Service. cmd/serve.go installs it via
// flow.SetInteractiveHook after the bridge has started.
func (s *Service) InteractiveHook() *interactiveBridge {
	return &interactiveBridge{svc: s, retryCancels: make(map[string]context.CancelFunc)}
}

// OnInteractiveStepStart binds the step's session to the resolved
// target peers. Per the flow-api spec, this MUST complete BEFORE the
// agent's first turn fires so the agent's output naturally fans out to
// every reviewer.
//
// When the bridge service has a remote registrar wired (orchestrator-
// mediated-inbound mode, openspec change
// bridge-orchestrator-mediated-inbound Phase F), the hook ALSO mirrors
// each peer into the orchestrator's global binding index. Remote
// failure is non-fatal — the local bind is what gates step progress;
// the remote retry runs in the background.
func (h *interactiveBridge) OnInteractiveStepStart(ctx context.Context, sessionID string, target []bridge.PeerRef) error {
	if h == nil || h.svc == nil {
		return nil
	}
	results, err := h.svc.Bind(ctx, sessionID, target)
	if err != nil {
		return err
	}
	for _, r := range results {
		if r.Err != nil {
			return r.Err
		}
	}
	h.registerRemoteBindings(sessionID, results)
	return nil
}

// OnInteractiveStepComplete unbinds the session after the step's agent
// completes (struct_output fires). The dispatcher and all bindings for
// this session are torn down. Cancels any in-flight remote-register
// retry for this session so the goroutine exits before we drop the
// local rows.
func (h *interactiveBridge) OnInteractiveStepComplete(ctx context.Context, sessionID string) error {
	if h == nil || h.svc == nil {
		return nil
	}
	h.cancelRemoteRetry(sessionID)
	h.deregisterRemoteBindings(ctx, sessionID)
	return h.svc.Unbind(ctx, sessionID)
}

// registerRemoteBindings issues one Register call per bound peer.
// Each peer is a separate row in the orchestrator's binding index
// (PRIMARY KEY includes peer_id). Failures schedule a per-session
// background retry that re-attempts every
// remoteRegisterRetryInterval until the step completes.
func (h *interactiveBridge) registerRemoteBindings(sessionID string, results []BindResult) {
	if h == nil || h.svc == nil || h.svc.remoteRegistrar == nil {
		return
	}
	// Self-identity must be wired alongside the registrar — without
	// host:port the orchestrator can't route inbound back to us.
	// Skip the remote call rather than POST a malformed row.
	if h.svc.remoteSelfHost == "" || h.svc.remoteSelfPort == 0 || h.svc.remoteJobID == "" {
		logging.Warn("bridge: remote registrar configured but self-identity incomplete — skipping remote register",
			"sessionID", sessionID,
			"selfHost", h.svc.remoteSelfHost, "selfPort", h.svc.remoteSelfPort,
			"jobID", h.svc.remoteJobID)
		return
	}

	var failed []bridge.RemoteBinding
	for _, r := range results {
		if r.Err != nil {
			continue
		}
		peerID := r.ResolvedPeerID
		if peerID == "" {
			peerID = r.Requested.PeerID
		}
		rb := bridge.RemoteBinding{
			ProjectID:     h.svc.remoteProjectID,
			Channel:       r.Requested.Channel,
			Identity:      r.Requested.Identity,
			PeerID:        peerID,
			JobID:         h.svc.remoteJobID,
			ContainerHost: h.svc.remoteSelfHost,
			ContainerPort: h.svc.remoteSelfPort,
			SessionID:     sessionID,
			MentionHandle: r.Requested.Mention,
		}
		if err := h.attemptRemoteRegister(rb); err != nil {
			logging.Warn("bridge: remote register failed — scheduling retry",
				"sessionID", sessionID, "peer", peerID, "err", err)
			failed = append(failed, rb)
		}
	}
	if len(failed) > 0 {
		h.scheduleRemoteRetry(sessionID, failed)
	}
}

// attemptRemoteRegister wraps one Register call with the registrar's
// own timeout (HTTPRegistrar.Timeout). Pulled out for testability.
func (h *interactiveBridge) attemptRemoteRegister(b bridge.RemoteBinding) error {
	ctx, cancel := context.WithTimeout(context.Background(), bridge.RegistrarTimeout)
	defer cancel()
	return h.svc.remoteRegistrar.Register(ctx, b)
}

// scheduleRemoteRetry launches a goroutine that retries the failed
// register every remoteRegisterRetryInterval until ALL pending rows
// land OR the step completes (cancelRemoteRetry fires). One goroutine
// per session — multiple failures collapse into a single retry list.
func (h *interactiveBridge) scheduleRemoteRetry(sessionID string, pending []bridge.RemoteBinding) {
	if len(pending) == 0 {
		return
	}
	h.retryMu.Lock()
	if h.retryCancels == nil {
		h.retryCancels = make(map[string]context.CancelFunc)
	}
	if existing, ok := h.retryCancels[sessionID]; ok {
		// A previous retry goroutine is still alive — cancel it
		// before launching the fresh one with the updated pending
		// list (the caller knows what failed this round).
		existing()
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.retryCancels[sessionID] = cancel
	h.retryMu.Unlock()

	go h.runRemoteRetryLoop(ctx, sessionID, cancel, pending)
}

// runRemoteRetryLoop is the per-session retry goroutine. Recovers
// from panics so a bug in the orchestrator's response handling
// doesn't take down the bridge.
//
// The retry interval is snapshotted at goroutine start so that
// tests using `withFastRetry` to temporarily shorten the global var
// can't race the goroutine's reads.
//
// ownCancel is the cancel func this goroutine was launched with — used
// at teardown to ensure we only delete OUR entry from retryCancels.
// A racing scheduleRemoteRetry that already replaced the entry stays
// untouched, so subsequent cancelRemoteRetry calls still see it.
func (h *interactiveBridge) runRemoteRetryLoop(ctx context.Context, sessionID string, ownCancel context.CancelFunc, pending []bridge.RemoteBinding) {
	interval := remoteRegisterRetryInterval
	defer func() {
		if r := recover(); r != nil {
			logging.Error("bridge: remote-register retry panicked",
				"sessionID", sessionID, "panic", r)
		}
		h.retryMu.Lock()
		// Only clear the map entry if it still points at OUR cancel
		// func — comparing via reflect.ValueOf().Pointer() because
		// context.CancelFunc is not directly comparable. A racing
		// scheduleRemoteRetry that replaced the entry installs a
		// fresh func; we MUST NOT delete that one.
		if existing, ok := h.retryCancels[sessionID]; ok && sameCancelFunc(existing, ownCancel) {
			delete(h.retryCancels, sessionID)
		}
		h.retryMu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		// Iterate a copy so we can rebuild the pending list as
		// individual peers succeed.
		remaining := pending[:0]
		for _, b := range pending {
			if err := h.attemptRemoteRegister(b); err != nil {
				remaining = append(remaining, b)
				continue
			}
			logging.Info("bridge: remote register succeeded on retry",
				"sessionID", sessionID, "peer", b.PeerID)
		}
		pending = remaining
		if len(pending) == 0 {
			return
		}
	}
}

// cancelRemoteRetry stops the per-session retry goroutine, if any.
// Idempotent.
func (h *interactiveBridge) cancelRemoteRetry(sessionID string) {
	h.retryMu.Lock()
	defer h.retryMu.Unlock()
	if cancel, ok := h.retryCancels[sessionID]; ok {
		cancel()
		delete(h.retryCancels, sessionID)
	}
}

// deregisterRemoteBindings calls Deregister for every binding row
// the runner registered. Best-effort — failures are logged and the
// orchestrator's TTL sweeper picks up any rows we couldn't clear.
func (h *interactiveBridge) deregisterRemoteBindings(ctx context.Context, sessionID string) {
	if h == nil || h.svc == nil || h.svc.remoteRegistrar == nil {
		return
	}
	// Look up the local binding rows we registered for this session
	// so we know which peer keys to deregister. ListBindingsBySession
	// reads the local cache; it's strictly weaker than the remote
	// index (the local rows always exist before the remote ones do).
	bindings, err := h.svc.store.ListBindingsBySession(ctx, h.svc.projectID, sessionID)
	if err != nil {
		logging.Warn("bridge: list local bindings on deregister failed",
			"sessionID", sessionID, "err", err)
		return
	}
	for _, b := range bindings {
		dctx, cancel := context.WithTimeout(context.Background(), bridge.RegistrarTimeout)
		err := h.svc.remoteRegistrar.Deregister(dctx, h.svc.remoteProjectID, b.Channel, b.IdentityID, b.PeerID)
		cancel()
		if err != nil {
			logging.Warn("bridge: remote deregister failed — TTL sweeper will reap",
				"sessionID", sessionID, "peer", b.PeerID, "err", err)
		}
	}
}
