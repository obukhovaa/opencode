package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
)

// Bind associates one or more peers with an opencode session, starting
// the per-session dispatcher if it isn't already running. Idempotent for
// repeated identical binds. Used by:
//
//   - POST /router/bind HTTP handler (Phase 6.2)
//   - flow engine on entering an interactive: true step (Phase 8.8)
//   - external orchestrators wanting to re-bind mid-flow
//
// User-id-form peers (Slack U<id>, Mattermost 26-char user id) are
// resolved to DM channels via the adapter's ResolveUserToDM before the
// binding row is persisted, per the chat-bridge-router-initiated spec.
// The resolved value is returned in BindResult.ResolvedPeerID so callers
// can log what actually got persisted.
func (s *Service) Bind(ctx context.Context, sessionID string, peers []bridge.PeerRef) ([]BindResult, error) {
	if sessionID == "" {
		return nil, errors.New("bridge.Bind: sessionID required")
	}
	if len(peers) == 0 {
		return nil, errors.New("bridge.Bind: at least one peer required")
	}

	// The bridge_sessions FK requires the referenced opencode session to
	// exist (the FK is ON DELETE SET NULL, not ON DELETE CASCADE, so the
	// row CAN survive session deletion — but it CAN'T be created against
	// a non-existent session). For router-initiated conversations the
	// caller hands us a fresh sessionID that doesn't yet exist; create
	// it transparently here so the rest of the bind path works the same
	// for both pre-existing and brand-new sessions.
	if err := s.ensureSession(ctx, sessionID); err != nil {
		return nil, fmt.Errorf("bridge.Bind: ensure session: %w", err)
	}

	out := make([]BindResult, len(peers))
	for i, p := range peers {
		res := BindResult{Requested: p}
		adapter := s.Adapter(p.Channel, p.Identity)
		if adapter == nil {
			res.Err = fmt.Errorf("bridge.Bind: no adapter for %s:%s", p.Channel, p.Identity)
			out[i] = res
			continue
		}

		resolved, err := adapter.ResolveUserToDM(ctx, p.PeerID)
		if err != nil {
			res.Err = fmt.Errorf("bridge.Bind: resolve %s: %w", p.PeerID, err)
			out[i] = res
			continue
		}
		res.ResolvedPeerID = resolved

		row, err := s.store.UpsertBinding(ctx, store.Binding{
			ProjectID:     s.projectID,
			Channel:       p.Channel,
			IdentityID:    p.Identity,
			PeerID:        resolved,
			SessionID:     sessionID,
			MentionHandle: p.Mention,
		})
		if err != nil {
			res.Err = fmt.Errorf("bridge.Bind: upsert: %w", err)
			out[i] = res
			continue
		}
		res.Binding = row
		out[i] = res
	}

	// Ensure the dispatcher is running for this session — but ONLY if at
	// least one peer was bound successfully. Starting a dispatcher for a
	// session with zero successful bindings would leak a goroutine; the
	// caller's per-peer error handling can't reverse it without an
	// explicit Unbind.
	anySuccess := false
	for i := range out {
		if out[i].Err == nil {
			anySuccess = true
			break
		}
	}
	if anySuccess {
		_ = s.dispatcherFor(sessionID)
	}
	return out, nil
}

// BridgePlaceholderTitle is the title applied to opencode sessions
// the bridge pre-creates via ensureSession / allocateSession when a
// peer is being bound before any real conversation has started.
// Sessions with this title and MessageCount==0 are placeholder rows
// that should be hidden from chat-surface listings (see cmdSessions).
// The title is also what the agent's title-generation will overwrite
// with a real conversation summary once the first turn completes.
const BridgePlaceholderTitle = "chat-bridge"

// ensureSession looks up the opencode session by ID; if it's truly
// missing (sql.ErrNoRows), creates it via Sessions.CreateWithID. Used
// by Bind so router-initiated callers can hand us an arbitrary
// sessionID for a fresh conversation without pre-creating the session
// themselves.
//
// Race safety: two concurrent Bind calls for the same sessionID could
// both observe ErrNoRows and both call CreateWithID — the second one
// would fail with a PK violation. We swallow that specific error here
// so the "we both lost the race but the row is now there" outcome is
// treated as success. Any other CreateWithID error (DB down, schema
// mismatch) still bubbles up.
//
// Transient Get errors (DB connection blip) MUST NOT trigger a
// CreateWithID — otherwise we risk masking a real outage with an
// unconditional insert that may also fail. Only sql.ErrNoRows takes
// the create branch.
func (s *Service) ensureSession(ctx context.Context, sessionID string) error {
	if s.app == nil || s.app.Sessions == nil {
		// Tests and headless builds without a session service skip
		// the check — store-level FK enforcement still gates against
		// truly bad IDs.
		return nil
	}
	_, err := s.app.Sessions.Get(ctx, sessionID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("ensureSession Get: %w", err)
	}
	if _, createErr := s.app.Sessions.CreateWithID(ctx, sessionID, BridgePlaceholderTitle); createErr != nil {
		// Treat "already exists by the time we got here" as success —
		// another goroutine won the race and the row is now there.
		// Any other error (FK violation against a different table,
		// DB outage, schema mismatch) bubbles up.
		if isSessionAlreadyExistsErr(createErr) {
			return nil
		}
		return fmt.Errorf("ensureSession CreateWithID: %w", createErr)
	}
	return nil
}

// isSessionAlreadyExistsErr reports whether a CreateWithID error is
// the "row already exists" kind we can safely ignore in the
// ensureSession race-recovery path.
//
// Provider-specific signatures we match:
//   - MySQL: `Error 1062 (23000): Duplicate entry 'X' for key 'PRIMARY'`
//   - SQLite: `UNIQUE constraint failed: sessions.id`
//   - SQLite (PK-specific): `PRIMARY KEY must be unique`
//
// Match strings are kept narrow on purpose: a bare `"PRIMARY"` would
// also match unrelated FK / index errors that happen to mention the
// word, so we anchor on the `for key 'PRIMARY'` MySQL phrase and the
// SQLite-specific `PRIMARY KEY must be unique`.
func isSessionAlreadyExistsErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Duplicate entry") ||
		strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "PRIMARY KEY must be unique") ||
		strings.Contains(msg, "for key 'PRIMARY'")
}

// BindResult reports the outcome of binding one peer. Requested is the
// caller's input verbatim; ResolvedPeerID is the post-resolution form the
// adapter returned (DM channel for user-id inputs, same value otherwise);
// Binding is the persisted row when Err is nil.
type BindResult struct {
	Requested      bridge.PeerRef
	ResolvedPeerID string
	Binding        store.Binding
	Err            error
}

// Unbind removes peer bindings for the given session. When peers is empty,
// every binding for sessionID is removed and the dispatcher is torn down.
// When peers is non-empty, only the matching bindings are removed; the
// dispatcher continues as long as at least one binding remains.
//
// An in-flight agent.Run for the session is allowed to complete — the
// dispatcher won't accept further inbound after close, but a turn already
// in progress drains naturally.
func (s *Service) Unbind(ctx context.Context, sessionID string, peers ...bridge.PeerRef) error {
	if sessionID == "" {
		return errors.New("bridge.Unbind: sessionID required")
	}
	if len(peers) == 0 {
		if err := s.store.DeleteBindingsBySession(ctx, s.projectID, sessionID); err != nil {
			return err
		}
		s.closeDispatcher(sessionID)
		return nil
	}
	for _, p := range peers {
		if err := s.store.DeleteBindingByPeer(ctx, s.projectID, p.Channel, p.Identity, p.PeerID); err != nil {
			return err
		}
	}
	// Tear down the dispatcher if no bindings remain. The check + close
	// happens atomically under dispatchMu inside closeDispatcherIfEmpty
	// so a concurrent Bind cannot race the teardown (see method docs).
	s.closeDispatcherIfEmpty(ctx, sessionID)
	return nil
}
