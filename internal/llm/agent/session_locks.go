package agent

import (
	"sync"
)

// globalSessionSlots is the process-wide session-run ledger. Every
// agent.Service instance — primary agents, per-step flow agents, task
// subagent instances — claims a session's slot here before starting a Run,
// so the one-Run-per-session invariant holds across instances, not just
// within one. Values are either a context.CancelFunc (a live Run's cancel)
// or cronLock (the cron scheduler's synthetic-commit lock).
//
// Rationale: the per-instance activeRequests map used to be the only
// ledger, but a session's Run and its observers do not always share an
// instance. In flow mode a step's session runs on a per-step agent
// instance while task auto-resume, cron, and the bridge consult a
// different one (the active/primary agent) — their busy checks were
// guaranteed false-negatives, letting a second Run interleave the same
// session's message log (GENAI-239). The per-instance map remains for
// instance-scoped concerns (Cancel bookkeeping, IsBusy, summarize slots);
// mutual exclusion lives here.
var globalSessionSlots sync.Map

// SessionBusy reports whether any agent instance currently holds the
// session's run slot (an in-flight Run or the cron scheduler's lock).
// This is the process-wide truth; prefer it over Service.IsSessionBusy
// when the caller doesn't know which instance owns the session.
func SessionBusy(sessionID string) bool {
	_, busy := globalSessionSlots.Load(sessionID)
	return busy
}

// acquireSessionSlot atomically claims the session's run slot. Returns
// false when another holder — on any instance — already owns it. The
// LoadOrStore also closes the check-then-store race the old per-instance
// gate had between IsSessionBusy and activeRequests.Store.
func acquireSessionSlot(sessionID string, holder any) bool {
	_, loaded := globalSessionSlots.LoadOrStore(sessionID, holder)
	return !loaded
}

// releaseSessionSlot frees the slot. Callers MUST hold it (a successful
// acquireSessionSlot) — the delete is unconditional.
func releaseSessionSlot(sessionID string) {
	globalSessionSlots.Delete(sessionID)
}
