package service

import (
	"context"
	"sync"

	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// PermissionRouter wires cfg.Router.PermissionMode into the agent's
// permission flow. On every PermissionRequest emitted by the agent
// while running a bridge-bound session, the router checks the
// configured mode and auto-resolves the request:
//
//   - "allow" → Grant immediately, no human interaction
//   - "deny"  → Deny immediately
//   - any other value (empty, "ask", etc.) → leave alone so the
//     opencode default handler (TUI / API consumer) can answer
//
// The router scopes its action to sessions that have at least one
// bridge_sessions row — permission requests from sessions opencode
// uses outside the chat bridge (TUI, scripted runs) are untouched.
//
// Implementation note: this mirrors QuestionRouter (question.go) — both
// subscribe to a per-service pubsub broker, both filter by "is this
// session bridge-bound?", both auto-resolve in-process. The two flows
// are kept separate because they hit different services and have
// different scope semantics.
type PermissionRouter struct {
	svc *Service

	// warnUnknownOnce guards a one-shot "unrecognised permission mode"
	// log line so a misconfigured PermissionMode doesn't spam the log
	// for every permission request. Triggered the first time a request
	// from a bridge-bound session arrives with a mode the router
	// doesn't understand.
	warnUnknownOnce sync.Once

	// ownedSessions caches the "is this session bound (or a descendant
	// of a bound session)" answer per session ID. MCP-heavy subagent
	// runs can fire dozens of permission requests per turn; without
	// the cache each one would hit the store for a binding lookup AND
	// a session-row lookup. Keys: session ID; values: bool.
	//
	// The verdict depends on live binding rows, so every binding
	// mutation (bind, unbind, /session switch) MUST invalidate via
	// Service.invalidateSessionScopeCaches. A stale false here is how
	// cron jobs on a re-bound session silently deferred forever: the
	// scheduler's HasPermissionResolver gate kept reading the cached
	// verdict from before the switch until the process restarted.
	ownedSessions sync.Map

	// cacheMu serialises verdict commits (isBridgeOwnedSession) against
	// invalidations. Without it a verdict computed from binding rows read
	// *before* an invalidation could be written back *after* the Clear() —
	// a TOCTOU that re-caches a stale verdict permanently, reviving the
	// exact defer-forever bug behind a narrow race window. cacheGen is
	// bumped on every invalidation; a commit whose snapshot generation no
	// longer matches is dropped and recomputed on the next lookup. cacheGen
	// is only ever read or written under cacheMu.
	cacheMu  sync.Mutex
	cacheGen uint64

	// beforeCommitHook, when non-nil, runs inside isBridgeOwnedSession after
	// the binding rows are read but before the verdict is committed.
	// Test-only (mirrors Scheduler.transitionHook); lets a test drop an
	// invalidation into the commit window deterministically. Production
	// leaves it nil.
	beforeCommitHook func()
}

// invalidateOwnedSessions drops every cached ownership verdict so the
// next lookup re-reads the binding rows. Called on any binding mutation;
// bindings change rarely (human-driven), so a full clear is simpler and
// safer than tracking which session IDs a mutation affected — a rebind
// changes the answer for BOTH the old and the new session. Bumping
// cacheGen under cacheMu also cancels any in-flight verdict computation
// whose store read predates this call (see isBridgeOwnedSession).
func (r *PermissionRouter) invalidateOwnedSessions() {
	if r == nil {
		return
	}
	r.cacheMu.Lock()
	r.cacheGen++
	r.ownedSessions.Clear()
	r.cacheMu.Unlock()
}

// invalidateSessionScopeCaches drops binding-derived caches after a
// binding mutation. Dispatcher-level ownedSessions caches are NOT
// touched: they key on root_session_id lineage, which is immutable.
func (s *Service) invalidateSessionScopeCaches() {
	if s == nil {
		return
	}
	s.permissionRouter.invalidateOwnedSessions()
}

// newPermissionRouter constructs the router and launches its
// subscriber goroutine when both the permission service and a
// permission mode are configured. A nil app or unset mode means the
// router stays dormant — opencode's default permission UI handles
// everything.
func (s *Service) newPermissionRouter() *PermissionRouter {
	r := &PermissionRouter{svc: s}
	if s.app == nil || s.app.Permissions == nil {
		return r
	}
	if s.cfg == nil || s.cfg.PermissionMode == "" {
		return r
	}
	s.launchSupervised("permission-router", r.run)
	return r
}

// run subscribes to permission.Service.Subscribe and dispatches each
// new request to the auto-resolver. Exits when the service ctx ends.
func (r *PermissionRouter) run(ctx context.Context) {
	sub := r.svc.app.Permissions.Subscribe(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub:
			if !ok {
				return
			}
			if ev.Type != pubsub.CreatedEvent {
				continue
			}
			r.handleRequest(ctx, ev.Payload)
		}
	}
}

// handleRequest auto-resolves one permission request based on the
// configured PermissionMode. The mode is read fresh per-request from
// cfg so an operator can flip it via the HTTP CRUD endpoints without
// restarting (in-memory cfg mutates alongside the disk write).
func (r *PermissionRouter) handleRequest(ctx context.Context, req permission.PermissionRequest) {
	if r.svc.cfg == nil {
		return
	}

	// Only auto-resolve for sessions that this bridge cares about —
	// either the bound session itself OR one of its descendant subagent
	// sessions. Permission requests from sessions owned by the TUI /
	// API caller / scheduled flows pass through unchanged so the
	// human-in-the-loop UX still works for them.
	//
	// Subagent sessions (spawned via the `task` tool) carry the bound
	// session's ID as root_session_id but NOT in bridge_sessions
	// directly. Without the root-session fallback, every MCP tool call
	// in a subagent hangs forever in serve mode (mcp-tool.go's default
	// permission branch calls permissions.Request which blocks until
	// Grant/Deny — but with no TUI subscriber AND the router skipping
	// the unbound subagent session_id, nobody ever resolves it). That
	// is exactly the "MCP hangs in bridge but not in TUI" asymmetry.
	if !r.isBridgeOwnedSession(ctx, req.SessionID) {
		return
	}

	switch r.svc.cfg.PermissionMode {
	case "allow":
		logging.Info("bridge: auto-allow permission",
			"session", req.SessionID, "tool", req.ToolName, "action", req.Action)
		r.svc.app.Permissions.Grant(req)
	case "deny":
		logging.Info("bridge: auto-deny permission",
			"session", req.SessionID, "tool", req.ToolName, "action", req.Action)
		r.svc.app.Permissions.Deny(req)
	case "", "ask":
		// Empty = "no router-driven auto-resolution"; opencode's
		// default UI handles the request. In headless serve mode
		// with no UI consumer this would hang — but that's the
		// operator's choice (they didn't set PermissionMode), and
		// the rule for "ask" is the same. Don't fail-safe to deny
		// here, since the spec reserves "ask" for future chat-relayed
		// permission flows.
	default:
		// Unrecognised value (typo, future mode, etc.) — fail-safe
		// to deny so a misconfigured field can't accidentally
		// auto-approve agent operations OR leave them hanging
		// indefinitely in a headless deploy. Log once at WARN so an
		// operator can see the misconfig without log spam.
		r.warnUnknownOnce.Do(func() {
			logging.Warn("bridge: unrecognised PermissionMode; falling back to deny",
				"mode", r.svc.cfg.PermissionMode,
				"hint", "use \"allow\", \"deny\", \"ask\", or omit the field")
		})
		logging.Info("bridge: auto-deny permission (unrecognised mode)",
			"session", req.SessionID, "tool", req.ToolName, "action", req.Action,
			"mode", r.svc.cfg.PermissionMode)
		r.svc.app.Permissions.Deny(req)
	}
}

// HasPermissionResolver satisfies cron.PermissionResolverChecker so the
// cron scheduler can recognise bridge-bound sessions as "watched" and
// proceed to call permissions.Request() instead of deferring 60s/tick
// forever on the active-session gate. Only returns true when the bridge
// is configured with a mode that actually resolves requests today —
// "allow" and "deny". "ask" is reserved for a future chat-relayed
// permission flow that doesn't exist yet (see handleRequest), and an
// empty mode means the bridge stays out of permissions entirely. In
// both unhandled cases the scheduler keeps its safer "defer until the
// TUI active session" behaviour to avoid hanging Request() forever.
func (s *Service) HasPermissionResolver(ctx context.Context, sessionID string) bool {
	if s == nil || s.permissionRouter == nil {
		return false
	}
	if s.cfg == nil {
		return false
	}
	switch s.cfg.PermissionMode {
	case "allow", "deny":
		return s.permissionRouter.isBridgeOwnedSession(ctx, sessionID)
	default:
		return false
	}
}

// isBridgeOwnedSession reports whether a permission request's session
// is in this bridge's scope — either bound directly via bridge_sessions
// or a descendant subagent of a bound session (root_session_id matches
// a bound row). Cached in r.ownedSessions to amortise the per-request
// store lookups; subagent MCP-heavy turns can fire dozens of permission
// requests with the same session_id.
func (r *PermissionRouter) isBridgeOwnedSession(ctx context.Context, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	if v, ok := r.ownedSessions.Load(sessionID); ok {
		return v.(bool)
	}

	// Snapshot the cache generation before reading binding rows. If an
	// invalidation lands while we read (a binding mutation on another
	// goroutine), cacheGen moves and we drop the about-to-be-stale verdict
	// instead of caching it past the Clear().
	r.cacheMu.Lock()
	gen := r.cacheGen
	r.cacheMu.Unlock()

	owned := false

	// 1. Direct binding?
	bindings, err := r.svc.store.ListBindingsBySession(ctx, r.svc.projectID, sessionID)
	if err != nil {
		logging.Warn("bridge: permission router binding lookup",
			"session", sessionID, "err", err)
		// Don't cache on error so a transient DB blip doesn't make us
		// permanently ignore the session.
		return false
	}
	if len(bindings) > 0 {
		owned = true
	}

	// 2. Descendant via root_session_id? (subagent path)
	if !owned && r.svc.app != nil && r.svc.app.Sessions != nil {
		sess, err := r.svc.app.Sessions.Get(ctx, sessionID)
		if err == nil && sess.RootSessionID != "" && sess.RootSessionID != sessionID {
			rootBindings, err := r.svc.store.ListBindingsBySession(ctx,
				r.svc.projectID, sess.RootSessionID)
			if err == nil && len(rootBindings) > 0 {
				owned = true
			}
		}
	}

	if r.beforeCommitHook != nil {
		r.beforeCommitHook()
	}

	// Commit only if no invalidation happened while we were reading. The
	// gen-compare and the Store run together under cacheMu so an invalidation
	// can't slip between them — otherwise a verdict read before a rebind
	// could still be written back after the Clear() and stick forever.
	r.cacheMu.Lock()
	if r.cacheGen == gen {
		r.ownedSessions.Store(sessionID, owned)
	}
	r.cacheMu.Unlock()
	return owned
}
