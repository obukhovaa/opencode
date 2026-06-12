package service

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/logging"
)

// inboundAuditTextMax bounds the length of in.Text recorded in the
// audit log line so a pasted 50 KB blob doesn't dominate the log
// stream. The full payload still reaches the agent; only the audit
// record is truncated. Counted in runes (codepoints), not bytes, so
// the cap is consistent for ASCII and multi-byte scripts.
const inboundAuditTextMax = 500

// runeCount returns the number of codepoints in s without allocating.
// Used by logInboundAudit to decide whether to truncate, paired with
// truncateRunes (defined in dispatch.go) for the actual cut.
func runeCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// logInboundAudit records a single structured log line per inbound
// message at the orchestrator's funnel point. Mirrors the legacy
// openrouter "[INBOUND] …" trace. Keys are stable so operators can
// grep / jq across logs. Text is truncated; everything else is
// emitted verbatim.
func (s *Service) logInboundAudit(in bridge.Inbound) {
	text := in.Text
	truncated := false
	// Rune-based truncation so a multi-byte codepoint at the boundary
	// is not split into invalid UTF-8 in the log record. A reviewer
	// sending Cyrillic / CJK / emoji text would otherwise see broken
	// chars in the audit trail.
	if runeCount(text) > inboundAuditTextMax {
		text = truncateRunes(text, inboundAuditTextMax)
		truncated = true
	}
	cmd := ""
	if in.Text != "" && in.Text[0] == '/' {
		// Pre-parse the command name so the audit log surfaces it
		// even when handleChatCommand later short-circuits or the
		// inbound is treated as a regular prompt.
		cmd, _ = splitChatCommand(in.Text)
	}
	logging.Info("bridge: inbound",
		"channel", in.Peer.Channel,
		"identity", in.Peer.Identity,
		"peerId", in.Peer.PeerID,
		"authorId", in.AuthorID,
		"command", cmd,
		"attachments", len(in.Attachments),
		"truncated", truncated,
		"text", text,
	)
}

// dispatchInbound resolves an inbound message to its session, applies the
// reviewer-attribution envelope when the session has multiple bound peers,
// and pushes onto the right per-session dispatcher.
//
// Per the chat-bridge spec's "Peer-to-session resolution" requirement:
//
//  1. If a binding row exists and session_id is non-null → use it.
//  2. If a binding row exists but session_id is NULL (parent session GC'd
//     via FK ON DELETE SET NULL) → allocate a fresh opencode session and
//     UPDATE the row.
//  3. If no binding row exists (user-initiated DM with no prior context)
//     → create a new opencode session AND insert the bridge_sessions row
//     pointing at it.
//
// Router-initiated mode (binding pre-created by Bind() before any inbound
// is seen) is implicit in case 1 — Bind installs the row + dispatcher
// up-front, so the first inbound from that peer routes through the
// existing dispatcher.
func (s *Service) dispatchInbound(ctx context.Context, in bridge.Inbound) {
	// Audit log: single line per inbound at the orchestrator's funnel
	// point. Mirrors the legacy openrouter "[INBOUND] …" trace so
	// operators can grep server logs to reconstruct exactly what a
	// reviewer sent and when. Text is truncated to keep individual
	// log lines bounded; full message bodies still reach the agent.
	s.logInboundAudit(in)

	// Slash-command interception happens before session resolution: the
	// command may not be tied to a session (e.g. /help has no agent run).
	if in.Text != "" && in.Text[0] == '/' {
		in.Command, in.CommandArgs = splitChatCommand(in.Text)
		if reply := s.handleChatCommand(ctx, in); !reply.IsEmpty() {
			s.replyToPeerWithHint(ctx, in.Peer, reply)
			return
		}
	}

	binding, err := s.resolveBinding(ctx, in.Peer)
	if err != nil {
		logging.Warn("bridge: resolveBinding failed",
			"peer", in.Peer, "err", err)
		return
	}

	// If there's a pending agent question for this session, intercept the
	// inbound as the answer rather than feeding it to the agent as a new
	// prompt. The question router replies via question.Service.Reply,
	// unblocking the agent's Ask() call in-process.
	if s.questionRouter != nil && s.questionRouter.TryHandleQuestionReply(ctx, binding.SessionID, in) {
		return
	}

	// For multi-peer sessions, prepend the attribution envelope so the
	// agent knows which reviewer spoke. Lookup once per inbound — the
	// peerCount drives both the envelope decision and the fan-out cardinality.
	peerCount, err := s.peerCountForSession(ctx, binding.SessionID)
	if err != nil {
		logging.Warn("bridge: peerCount lookup", "session", binding.SessionID, "err", err)
		peerCount = 1
	}
	in.Text = PrependAttributionIfMultiPeer(in.Peer, in.Text, peerCount)

	disp := s.dispatcherFor(binding.SessionID)
	if err := disp.pushInbound(ctx, in); err != nil {
		logging.Warn("bridge: pushInbound", "session", binding.SessionID, "err", err)
	}
}

// resolveBinding returns the binding for the inbound's peer, creating a
// fresh session + row when no binding exists and reallocating the session
// when the existing row's session_id was NULL'd by the parent-session FK.
func (s *Service) resolveBinding(ctx context.Context, peer bridge.PeerRef) (store.Binding, error) {
	b, err := s.store.GetBinding(ctx, s.projectID, peer.Channel, peer.Identity, peer.PeerID)
	switch {
	case err == nil && b.SessionID != "":
		return b, nil
	case err == nil && b.SessionID == "":
		// Orphaned binding: allocate fresh session, repoint the row.
		newSess, err := s.allocateSession(ctx)
		if err != nil {
			return store.Binding{}, err
		}
		if err := s.store.UpdateBindingSessionID(ctx, s.projectID, peer.Channel, peer.Identity, peer.PeerID, newSess); err != nil {
			return store.Binding{}, err
		}
		b.SessionID = newSess
		return b, nil
	case errors.Is(err, store.ErrNotFound):
		// User-initiated DM: allocate session + insert binding.
		newSess, err := s.allocateSession(ctx)
		if err != nil {
			return store.Binding{}, err
		}
		nb, err := s.store.UpsertBinding(ctx, store.Binding{
			ProjectID:  s.projectID,
			Channel:    peer.Channel,
			IdentityID: peer.Identity,
			PeerID:     peer.PeerID,
			SessionID:  newSess,
			// MentionHandle stays unset — user-initiated peers
			// haven't been issued one. Router-initiated peers get
			// theirs at Bind time via the explicit Peer.Mention.
		})
		if err != nil {
			return store.Binding{}, err
		}
		return nb, nil
	default:
		return store.Binding{}, err
	}
}

// allocateSession creates a fresh opencode session and returns its ID.
// The bridge does NOT pick the session title — it leaves that for the
// title-generation path in the agent service. The session is created via
// the application's session.Service so triggers (cron, pubsub, etc.) fire
// just as they would for a user-created session.
func (s *Service) allocateSession(ctx context.Context) (string, error) {
	if s.app == nil || s.app.Sessions == nil {
		return "", errors.New("bridge.service: no session service")
	}
	id := uuid.NewString()
	if _, err := s.app.Sessions.CreateWithID(ctx, id, BridgePlaceholderTitle); err != nil {
		return "", err
	}
	return id, nil
}

// peerCountForSession reports how many bridge_sessions rows point at the
// given session_id. Used to decide whether to apply the reviewer
// attribution envelope.
func (s *Service) peerCountForSession(ctx context.Context, sessionID string) (int, error) {
	if sessionID == "" {
		return 0, nil
	}
	bindings, err := s.store.ListBindingsBySession(ctx, s.projectID, sessionID)
	if err != nil {
		return 0, err
	}
	return len(bindings), nil
}

// dispatcherFor returns (creating if needed) the per-session dispatcher
// for the given session_id. Idempotent and safe to call from any
// goroutine.
func (s *Service) dispatcherFor(sessionID string) *sessionDispatch {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	if d, ok := s.dispatchers[sessionID]; ok {
		return d
	}
	d := s.newSessionDispatch(sessionID)
	s.dispatchers[sessionID] = d
	return d
}

// closeDispatcher tears down the per-session dispatcher (if any) for
// sessionID. Called by Service.Stop on shutdown and by partial-Unbind
// when the caller has independently confirmed all bindings are gone.
// For the racey Unbind path use closeDispatcherIfEmpty instead.
func (s *Service) closeDispatcher(sessionID string) {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	if d, ok := s.dispatchers[sessionID]; ok {
		d.close()
		delete(s.dispatchers, sessionID)
	}
}

// closeDispatcherIfEmpty atomically (under dispatchMu) re-reads the
// session's binding count from the store and closes the dispatcher only
// when zero bindings remain. This serializes against concurrent
// dispatcherFor calls coming from Bind, eliminating the TOCTOU window
// between "count peers" and "close dispatcher" that a naive read-then-
// close pattern would leave open. Bind's UpsertBinding races freely with
// the count read; whichever ordering wins, the result is consistent:
// either the dispatcher is closed (and Bind will create a fresh one when
// it next calls dispatcherFor) or kept (Bind sees the same dispatcher).
func (s *Service) closeDispatcherIfEmpty(ctx context.Context, sessionID string) {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	bindings, err := s.store.ListBindingsBySession(ctx, s.projectID, sessionID)
	if err != nil || len(bindings) > 0 {
		return
	}
	if d, ok := s.dispatchers[sessionID]; ok {
		d.close()
		delete(s.dispatchers, sessionID)
	}
}

// handleChatCommand dispatches an inbound whose text starts with "/" to
// the matching command handler. Returns the reply the bridge should
// send back via replyToPeerWithHint, or nil if the command is unknown
// (in which case the dispatcher proceeds with normal agent-run dispatch —
// keeping the door open for prompt-style messages that happen to start
// with /).
//
// Adapter-scoped command filtering: /pair is only available on Telegram
// per bridge-adapter-scoped-commands. Unknown commands on a channel are
// treated identically to unknown commands globally — the inbound falls
// through to the agent as a regular prompt.
func (s *Service) handleChatCommand(ctx context.Context, in bridge.Inbound) *bridge.CommandReply {
	if !s.commandAvailableForChannel(in.Command, in.Peer.Channel) {
		return nil
	}
	cmds := s.ChatCommands()
	h, ok := cmds[in.Command]
	if !ok {
		return nil
	}
	return h(ctx, in)
}

// commandAvailableForChannel implements adapter-scoped command filtering
// per bridge-adapter-scoped-commands. /pair only resolves on Telegram;
// everything else is universal. Returning false makes handleChatCommand
// behave as if the command were unknown, so the inbound falls through
// to the agent as a regular prompt.
func (s *Service) commandAvailableForChannel(cmd, channel string) bool {
	if cmd == "pair" && channel != "telegram" {
		return false
	}
	return true
}

// replyToPeer sends a single text message back to the inbound's peer.
// Used by the question/permission flows (Phase 3.7) and other paths
// that don't carry a structured render.
func (s *Service) replyToPeer(ctx context.Context, peer bridge.PeerRef, text string) {
	adapter := s.Adapter(peer.Channel, peer.Identity)
	if adapter == nil {
		logging.Warn("bridge: replyToPeer no adapter", "peer", peer)
		return
	}
	result := adapter.Send(ctx, bridge.Outbound{Peer: peer, Text: text})
	if !result.Delivered && result.Err != nil {
		logging.Warn("bridge: replyToPeer delivery failed", "peer", peer, "err", result.Err)
	}
}

// replyToPeerWithHint sends a chat-command reply, preferring the
// structured render path when the adapter satisfies bridge.RichRenderer
// AND reply.Hint != nil. Falls through to the plain-text path
// otherwise (mirrors Service.Send's hint-routing logic).
func (s *Service) replyToPeerWithHint(ctx context.Context, peer bridge.PeerRef, reply *bridge.CommandReply) {
	if reply.IsEmpty() {
		return
	}
	adapter := s.Adapter(peer.Channel, peer.Identity)
	if adapter == nil {
		logging.Warn("bridge: replyToPeerWithHint no adapter", "peer", peer)
		return
	}
	if reply.Hint != nil {
		if renderer, ok := adapter.(bridge.RichRenderer); ok {
			res := renderer.Render(ctx, peer, reply.Hint)
			if res.Err == nil {
				return
			}
			// Fallback to text on ErrRenderUnsupported or any other
			// adapter-side failure.
			logging.Info("bridge: command-reply render failed, falling back to text",
				"peer", peer, "err", res.Err)
		}
	}
	result := adapter.Send(ctx, bridge.Outbound{Peer: peer, Text: reply.Text})
	if !result.Delivered && result.Err != nil {
		logging.Warn("bridge: replyToPeerWithHint delivery failed", "peer", peer, "err", result.Err)
	}
}

// splitChatCommand parses a "/command args..." string. Returns the
// command name (without leading "/") and the remainder as a single args
// string (split-by-space is the command handler's responsibility — e.g.
// "/model claude-sonnet-4-5" → ("model", "claude-sonnet-4-5")).
func splitChatCommand(text string) (cmd, args string) {
	if len(text) < 2 || text[0] != '/' {
		return "", ""
	}
	rest := text[1:]
	idx := strings.IndexAny(rest, " \t\n")
	if idx < 0 {
		return rest, ""
	}
	return rest[:idx], strings.TrimSpace(rest[idx:])
}
