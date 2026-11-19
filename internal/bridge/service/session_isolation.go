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
// may be shown to, or switched into by, a peer on callerChannel whose
// current session is currentID.
//
// A caller on the external channel sees only currentID, whatever the
// setting says and without reading anything: each external peer is one
// conversation, its only session is the one bound to it, and it may not
// learn of or enter any other (the router_send rule of GENAI-464). This
// holds even when an operator allows the session commands on external.
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
func (s *Service) chatSessionVisible(ctx context.Context, callerChannel, currentID string, rows []session.Session) func(sessionID string) bool {
	isCurrent := onlySession(currentID)
	if callerChannel == externalChannel {
		return isCurrent
	}
	if !s.cfg.ExternalSessionsIsolated() {
		return func(string) bool { return true }
	}
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

// onlySession is the predicate that admits sessionID alone, and nothing
// when it is empty.
func onlySession(sessionID string) func(string) bool {
	return func(id string) bool { return id != "" && id == sessionID }
}

// cronSessionVisible checks only the distinct session IDs referenced by jobs.
// External callers see only their own jobs. A failed visibility query also
// fails closed to the current session.
func (s *Service) cronSessionVisible(ctx context.Context, callerChannel, currentID string, jobs []cron.CronJob) func(sessionID string) bool {
	if callerChannel == externalChannel {
		return onlySession(currentID)
	}
	ids := make([]string, 0, len(jobs))
	seen := map[string]bool{}
	for _, job := range jobs {
		if job.SessionID != "" && !seen[job.SessionID] {
			ids = append(ids, job.SessionID)
			seen[job.SessionID] = true
		}
	}
	visible, err := s.app.Sessions.VisibleSessionIDs(ctx, ids, currentID)
	if err != nil {
		logging.Warn("bridge: cron session visibility failed; showing only the current session", "err", err)
		return onlySession(currentID)
	}
	allowed := make(map[string]bool, len(visible))
	for _, id := range visible {
		allowed[id] = true
	}
	return func(id string) bool { return allowed[id] }
}
