package service

import (
	"context"

	"github.com/opencode-ai/opencode/internal/cron"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/session"
)

// externalChannel is the relay channel whose every peer is a separate
// conversation, often of another tenant (see bridge.ExternalChannelConfig).
const externalChannel = "external"

// chatSessionVisible returns the predicate the chat session commands
// (/sessions, /session <id-prefix>, /crons) use to decide whether a session
// may be shown to, or switched into by, a chat peer whose current session is
// currentID.
//
// With router.isolateExternalSessions off every session is visible and
// neither the store nor the session service is touched, so the commands
// behave exactly as before. With it on, a session is hidden — unless it is
// currentID, which always stays visible — when it holds at least one binding
// on the external channel, or when its root group holds one: the root of a
// session is its RootSessionID, or its own id when that is unset, so every
// step session of a flow run (CreateFlowSession gives them the run's root)
// is hidden once one step serves an external peer.
//
// Roots come from rows, the session rows the caller already read; no session
// is looked up here. An external-bound id that is not among rows (not a
// top-level session) counts as its own root. The external-bound set comes
// from one store query, not a lookup per session. A failed query fails
// closed — only currentID stays visible — and is logged at WARN.
//
// The setting is read per call, like the other scalar router fields.
func (s *Service) chatSessionVisible(ctx context.Context, currentID string, rows []session.Session) func(sessionID string) bool {
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
	roots := make(map[string]string, len(rows))
	for _, r := range rows {
		roots[r.ID] = r.RootSessionID
	}
	rootOf := func(id string) string {
		if r := roots[id]; r != "" {
			return r
		}
		return id
	}
	external := make(map[string]struct{}, len(ids))
	externalRoots := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		external[id] = struct{}{}
		externalRoots[rootOf(id)] = struct{}{}
	}
	return func(id string) bool {
		if isCurrent(id) {
			return true
		}
		if _, bound := external[id]; bound {
			return false
		}
		_, sameRun := externalRoots[rootOf(id)]
		return !sameRun
	}
}

// cronSessionVisible is chatSessionVisible for /crons, whose jobs may sit on
// any session. Call it only with the setting on. Roots come from the listed
// top-level sessions plus one Sessions.Get per distinct job session that is
// not among them (a subagent's session, whose RootSessionID is its run's
// root). A failed read fails closed: a failed listing leaves only currentID
// visible, and a job session that cannot be read is hidden.
func (s *Service) cronSessionVisible(ctx context.Context, currentID string, jobs []cron.CronJob) func(sessionID string) bool {
	rows, err := s.app.Sessions.List(ctx)
	if err != nil {
		logging.Warn("bridge: session listing failed; /crons shows only the current session's jobs", "err", err)
		return func(id string) bool { return id != "" && id == currentID }
	}
	known := make(map[string]bool, len(rows))
	for _, r := range rows {
		known[r.ID] = true
	}
	unreadable := map[string]bool{}
	for _, job := range jobs {
		id := job.SessionID
		if id == "" || id == currentID || known[id] || unreadable[id] {
			continue
		}
		sess, err := s.app.Sessions.Get(ctx, id)
		if err != nil {
			logging.Warn("bridge: cron job session lookup failed; hiding its jobs from /crons", "session", id, "err", err)
			unreadable[id] = true
			continue
		}
		known[id] = true
		rows = append(rows, sess)
	}
	visible := s.chatSessionVisible(ctx, currentID, rows)
	return func(id string) bool { return !unreadable[id] && visible(id) }
}
