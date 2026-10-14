package service

import (
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/logging"
)

// cancelOutcome is what cancelSession stopped.
type cancelOutcome struct {
	// runCancelled is true when a run was in flight and got cancelled.
	runCancelled bool
	// dropped counts the user messages that were waiting to run.
	dropped int
}

// cancelSession stops everything that would run on sessionID: the in-flight
// run (on any agent instance — Cancel reaches the holder through the global
// session ledger), the app's drain worker and queue for the session, and the
// messages waiting in the session's dispatcher. Without the last two a
// message queued behind the cancelled run starts a new run right after it.
func (s *Service) cancelSession(sessionID string) cancelOutcome {
	var out cancelOutcome
	if sessionID == "" || s.app == nil {
		return out
	}
	if ag := s.app.ActiveAgent(); ag != nil && ag.IsSessionBusy(sessionID) {
		ag.Cancel(sessionID)
		out.runCancelled = true
	}
	out.dropped += s.app.StopSessionQueue(sessionID)
	s.dispatchMu.Lock()
	d := s.dispatchers[sessionID]
	s.dispatchMu.Unlock()
	if d != nil {
		out.dropped += d.abortQueued()
	}
	if out.runCancelled || out.dropped > 0 {
		logging.Info("bridge: session cancelled",
			"session", sessionID, "run_cancelled", out.runCancelled, "dropped_messages", out.dropped)
	}
	return out
}

// abortQueued drops every inbound waiting in the dispatcher and makes a
// message that is already waiting out a busy session give up. It returns the
// number of user messages dropped; a dropped heartbeat turn releases its
// claim so the next beat can be queued.
func (d *sessionDispatch) abortQueued() int {
	d.abortEpoch.Add(1)
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	count := func(in bridge.Inbound) {
		if in.Heartbeat != nil {
			d.heartbeatQueued.Store(false)
			return
		}
		n++
	}
	for _, in := range d.overflow {
		count(in)
	}
	d.overflow = nil
	if d.stop.Load() {
		// close() drained and closed d.inbound under mu.
		return n
	}
	for {
		select {
		case in := <-d.inbound:
			count(in)
		default:
			return n
		}
	}
}
