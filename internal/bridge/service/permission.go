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
	// i.e. ones with a bound row. Permission requests from sessions
	// owned by the TUI / API caller / scheduled flows pass through
	// unchanged so the human-in-the-loop UX still works for them.
	bindings, err := r.svc.store.ListBindingsBySession(ctx, r.svc.projectID, req.SessionID)
	if err != nil {
		logging.Warn("bridge: permission router store lookup",
			"session", req.SessionID, "err", err)
		return
	}
	if len(bindings) == 0 {
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
