package service

import (
	"context"

	"github.com/opencode-ai/opencode/internal/logging"
)

// externalChannel is the relay channel whose every peer is a separate
// conversation, often of another tenant (see bridge.ExternalChannelConfig).
const externalChannel = "external"

// chatSessionVisible returns the predicate the chat session commands
// (/sessions, /session <id-prefix>, /crons) use to decide whether a session
// may be shown to, or switched into by, a chat peer whose current session is
// currentID.
//
// With router.isolateExternalSessions off every session is visible and the
// store is not touched, so the commands behave exactly as before. With it on,
// a session holding at least one binding on the external channel is hidden,
// except currentID: the caller's own session always stays visible. The
// external-bound set comes from one store query, not a lookup per session.
// A failed lookup fails closed — only currentID stays visible — and is
// logged at WARN.
//
// The setting is read per call, like the other scalar router fields.
func (s *Service) chatSessionVisible(ctx context.Context, currentID string) func(sessionID string) bool {
	if !s.cfg.ExternalSessionsIsolated() {
		return func(string) bool { return true }
	}
	isCurrent := func(id string) bool { return id != "" && id == currentID }
	ids, err := s.store.ListSessionIDsByChannel(ctx, s.projectID, externalChannel)
	if err != nil {
		logging.Warn("bridge: external-session lookup failed; chat session commands show only the current session",
			"err", err)
		return isCurrent
	}
	external := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		external[id] = struct{}{}
	}
	return func(id string) bool {
		if isCurrent(id) {
			return true
		}
		_, bound := external[id]
		return !bound
	}
}
