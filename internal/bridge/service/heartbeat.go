package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/contextfile"
	"github.com/opencode-ai/opencode/internal/heartbeat"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
)

// Heartbeat scheduling (openspec capability bridge-heartbeat).
//
// A heartbeat is a scheduled, synthetic inbound on a binding: the scheduler
// pushes a bridge.Inbound carrying a HeartbeatTurn onto the binding
// session's dispatcher, so the beat runs in the bound session, can never
// overlap another run there, and has its final reply relayed by the same
// path as any human turn. The dispatcher renders heartbeat turns quietly
// (see dispatch.go) and reports the outcome back through
// recordHeartbeatOutcome.
//
// Only identities whose adapter is registered in THIS process and owns
// the bot's platform connection are scheduled (see servesHeartbeats):
// the identity lock that guarantees one such adapter per identity across
// processes therefore also guarantees one scheduler per heartbeat. A
// mediated adapter (inbound disabled) takes no lock, so several processes
// can hold it at once; none of them schedules its beats.

var (
	// heartbeatTickInterval is how often the scheduler looks for due beats.
	// A variable so tests can drive ticks directly.
	heartbeatTickInterval = 30 * time.Second
	// heartbeatFirstTickDelay gives adapters time to connect before the
	// first tick (and the setup reminder) runs.
	heartbeatFirstTickDelay = 15 * time.Second
	// heartbeatNow is the scheduler's clock; tests override it.
	heartbeatNow = time.Now
	// heartbeatWorkDir resolves agenda paths; tests override it.
	heartbeatWorkDir = config.WorkingDirectory
)

// heartbeatPreemptedReason is the recorded outcome of a beat a human
// message cancelled.
const heartbeatPreemptedReason = "preempted by a message"

// heartbeatState is the Service's heartbeat bookkeeping.
type heartbeatState struct {
	mu sync.Mutex
	// reminded records identities whose setup reminder pass already ran
	// in this process, keyed by adapterKey.
	reminded map[string]bool
	// agents caches one model-override agent per model ID.
	agents map[string]agent.Service
}

// HeartbeatsEnabled reports whether this process runs heartbeats (daemon
// mode only).
func (s *Service) HeartbeatsEnabled() bool {
	return s.heartbeat != nil
}

// servesHeartbeats reports whether this process schedules beats and posts
// the setup reminder for the chats of adapter a: a chat platform adapter
// (not the external relay) that is inbound-active, i.e. owns the bot's
// own connection and its identity lock (see RegisterAdapter).
func servesHeartbeats(a bridge.Adapter) bool {
	if a == nil || a.Channel() == "external" {
		return false
	}
	if ia, ok := a.(bridge.AdapterInboundActiver); ok && !ia.InboundActive() {
		return false
	}
	return true
}

// runHeartbeats is the scheduler loop.
func (s *Service) runHeartbeats(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(heartbeatFirstTickDelay):
	}
	ticker := time.NewTicker(heartbeatTickInterval)
	defer ticker.Stop()
	for {
		s.heartbeatTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// heartbeatTick runs one scheduler pass: the setup reminder for newly
// registered identities, then every due beat. A beat that comes due
// outside the active hours or days (a catch-up after downtime, or one a
// busy session held back) is moved to the next allowed slot instead of
// firing. A session bound to several chats gets one beat per slot: once
// one of its rows has used the slot, its other due rows move on with it.
func (s *Service) heartbeatTick(ctx context.Context) {
	now := heartbeatNow().UTC()
	s.remindNewIdentities(ctx, now)

	rows, err := s.store.ListHeartbeats(ctx, s.projectID)
	if err != nil {
		logging.Warn("bridge: heartbeat list failed", "err", err)
		return
	}
	beaten := map[string]bool{}
	for _, row := range rows {
		if row.State != heartbeat.StateOn || !servesHeartbeats(s.Adapter(row.Channel, row.IdentityID)) {
			continue
		}
		if row.NextBeatAt.IsZero() {
			s.rescheduleHeartbeat(ctx, row, heartbeat.NextBeat(now, row.Settings))
			continue
		}
		if row.NextBeatAt.After(now) {
			continue
		}
		if !heartbeat.InWindow(now, row.Settings) {
			s.rescheduleHeartbeat(ctx, row, heartbeat.NextBeat(now, row.Settings))
			continue
		}
		binding, err := s.store.GetBinding(ctx, s.projectID, row.Channel, row.IdentityID, row.PeerID)
		if err != nil || binding.SessionID == "" {
			continue
		}
		if beaten[binding.SessionID] {
			s.rescheduleHeartbeat(ctx, row, nextSlot(now, row.Settings))
			continue
		}
		if used, _ := s.fireHeartbeat(ctx, row, binding, now, false); used {
			beaten[binding.SessionID] = true
		}
	}
}

// nextSlot is the slot after a beat that fires at now. Computing it from
// now, not from the missed due time, is what coalesces missed beats.
func nextSlot(now time.Time, s heartbeat.Settings) time.Time {
	return heartbeat.NextBeat(now.Truncate(time.Minute).Add(time.Minute), s)
}

// updateHeartbeat re-reads the row of snapshot's binding and applies set
// to that fresh copy, so a scheduler write changes only the fields its
// path owns. It writes nothing and returns false when the row's state or
// settings differ from snapshot: a /heartbeat change made since the
// snapshot was read (off, a new interval) wins over a write computed from
// the old settings. A missing row is created from snapshot.
func (s *Service) updateHeartbeat(ctx context.Context, snapshot store.Heartbeat, set func(*store.Heartbeat)) (bool, error) {
	cur, err := s.store.GetHeartbeat(ctx, s.projectID, snapshot.Channel, snapshot.IdentityID, snapshot.PeerID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		cur = snapshot
	case err != nil:
		return false, err
	}
	if !cur.Settings.Equal(snapshot.Settings) {
		return false, nil
	}
	set(&cur)
	return true, s.store.PutHeartbeat(ctx, cur)
}

// rescheduleHeartbeat moves row's next beat to next without firing.
func (s *Service) rescheduleHeartbeat(ctx context.Context, row store.Heartbeat, next time.Time) {
	if _, err := s.updateHeartbeat(ctx, row, func(h *store.Heartbeat) { h.NextBeatAt = next }); err != nil {
		logging.Warn("bridge: heartbeat schedule write failed", "peer", row.PeerID, "err", err)
	}
}

// fireHeartbeat queues one beat for row's binding.
//
// A scheduled beat waits, schedule unchanged, while the session is busy,
// a message is queued or an interactive flow step owns the session. It
// advances next_beat_at BEFORE it is queued, so a crash can never re-fire
// it in a loop. A manual beat (/heartbeat now, or the heartbeat tool's
// now, which runs inside the session's own turn) instead waits in line
// behind the current turn, and leaves the schedule alone. An empty agenda
// skips either kind with no model call; the first scheduled skip for a
// reason posts one notice to the chat.
//
// used reports whether the beat's slot was spent (queued or skipped);
// note says what happened, for the /heartbeat reply.
func (s *Service) fireHeartbeat(ctx context.Context, row store.Heartbeat, binding store.Binding, now time.Time, manual bool) (used bool, note string) {
	disp := s.dispatcherFor(binding.SessionID)
	// Claim the dispatcher's one beat before any check or write, so a
	// tick and a /heartbeat now racing here cannot both queue a beat.
	if !disp.heartbeatQueued.CompareAndSwap(false, true) {
		return false, "a heartbeat is already running"
	}
	queued := false
	defer func() {
		if !queued {
			disp.heartbeatQueued.Store(false)
		}
	}()

	// An interactive flow step owns its session (see dispatchInbound): a
	// beat there would run the default agent on the step's session.
	if s.app.Permissions != nil && s.app.Permissions.IsInteractiveSession(binding.SessionID) {
		return false, "an interactive flow step owns this session"
	}
	ag := s.app.ActiveAgent()
	busy := (ag != nil && ag.IsSessionBusy(binding.SessionID)) || disp.hasQueuedInbound()
	if busy && !manual {
		return false, "the session is busy; the beat will run when it is idle"
	}

	next := row.NextBeatAt
	if !manual {
		next = nextSlot(now, row.Settings)
	}
	agenda := row.EffectiveAgendaFile()
	if empty, why := agendaEmpty(agenda); empty {
		notify := false
		ok, err := s.updateHeartbeat(ctx, row, func(h *store.Heartbeat) {
			// Notify when scheduled beats start being skipped for this
			// reason, not on every skip.
			notify = !manual && !(h.LastStatus == heartbeat.OutcomeSkipped && h.LastError == why)
			if !manual {
				h.NextBeatAt = next
			}
			h.LastBeatAt, h.LastStatus, h.LastError = now, heartbeat.OutcomeSkipped, why
		})
		switch {
		case err != nil:
			logging.Warn("bridge: heartbeat write failed", "peer", row.PeerID, "err", err)
		case !ok:
			return false, "the heartbeat changed meanwhile; not firing"
		case notify:
			s.replyToPeer(ctx, binding.AsPeerRef(), heartbeat.SkipNotice(now, why), false, binding.SessionID)
		}
		logging.Info("bridge: heartbeat skipped", "session", binding.SessionID, "reason", why)
		return true, "skipped: " + why
	}
	if !manual {
		ok, err := s.updateHeartbeat(ctx, row, func(h *store.Heartbeat) { h.NextBeatAt = next })
		if err != nil {
			logging.Warn("bridge: heartbeat write failed; not firing", "peer", row.PeerID, "err", err)
			return false, "could not save the schedule"
		}
		if !ok {
			return false, "the heartbeat changed meanwhile; not firing"
		}
	}
	queued = true
	disp.pushInbound(bridge.Inbound{
		Peer:       binding.AsPeerRef(),
		Text:       heartbeat.Prompt(now, agenda),
		ReceivedAt: now.UnixMilli(),
		Heartbeat:  &bridge.HeartbeatTurn{At: now, Model: row.Model, Manual: manual, Next: next},
	})
	logging.Info("bridge: heartbeat queued", "session", binding.SessionID, "manual", manual,
		"next", next.Format(time.RFC3339))
	if busy {
		return true, "queued; it runs when the current turn ends"
	}
	return true, "queued"
}

// agendaEmpty reports whether the agenda file gives a beat nothing to do.
func agendaEmpty(rel string) (bool, string) {
	content, err := os.ReadFile(filepath.Join(heartbeatWorkDir(), rel))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, rel + " does not exist"
		}
		return true, "cannot read " + rel + ": " + err.Error()
	}
	if heartbeat.AgendaIsEmpty(string(content)) {
		return true, rel + " is empty"
	}
	return false, ""
}

// recordHeartbeatOutcome writes a finished beat's outcome to its row,
// re-read here so only the outcome fields change. A manual beat of a
// chat that never set its heartbeat up creates the (unset) row.
func (s *Service) recordHeartbeatOutcome(ctx context.Context, peer bridge.PeerRef, at time.Time, outcome, reason string) {
	row, err := s.loadHeartbeat(ctx, peer)
	if err != nil {
		logging.Warn("bridge: heartbeat outcome lookup failed", "peer", peer.PeerID, "err", err)
		return
	}
	row.LastBeatAt, row.LastStatus, row.LastError = at, outcome, reason
	if err := s.store.PutHeartbeat(ctx, row); err != nil {
		logging.Warn("bridge: heartbeat outcome write failed", "peer", peer.PeerID, "err", err)
	}
}

// heartbeatDeferred puts a scheduled beat that lost the race for its
// session back on the schedule: due now, so a later tick tries again once
// the session is idle. Only while the row is as the scheduler left it
// (on, next_beat_at unchanged): a /heartbeat change since then
// recomputed the schedule, and wins.
func (s *Service) heartbeatDeferred(ctx context.Context, peer bridge.PeerRef, hb *bridge.HeartbeatTurn) {
	row, err := s.store.GetHeartbeat(ctx, s.projectID, peer.Channel, peer.Identity, peer.PeerID)
	if err != nil || row.State != heartbeat.StateOn || !row.NextBeatAt.Equal(hb.Next) {
		return
	}
	row.NextBeatAt = hb.At
	if err := s.store.PutHeartbeat(ctx, row); err != nil {
		logging.Warn("bridge: heartbeat reschedule failed", "peer", peer.PeerID, "err", err)
	}
}

// heartbeatAgent returns the agent a heartbeat turn runs on: the active
// agent, or a cached per-instance model override of it.
func (s *Service) heartbeatAgent(ctx context.Context, model string) (agent.Service, error) {
	active := s.app.ActiveAgent()
	if model == "" || active == nil || string(active.Model().ID) == model {
		return active, nil
	}
	if s.app.AgentFactory == nil {
		return nil, errors.New("no agent factory for a heartbeat model override")
	}
	key := string(s.app.ActiveAgentName()) + "|" + model
	s.heartbeat.mu.Lock()
	defer s.heartbeat.mu.Unlock()
	if ag, ok := s.heartbeat.agents[key]; ok {
		return ag, nil
	}
	ag, err := s.app.AgentFactory.NewAgent(ctx, string(s.app.ActiveAgentName()), nil, "", false, nil, nil,
		contextfile.TemplateVars{}, agent.ModelOverride{Model: models.ModelID(model)})
	if err != nil {
		return nil, fmt.Errorf("heartbeat agent for model %s: %w", model, err)
	}
	s.heartbeat.agents[key] = ag
	return ag, nil
}

// remindNewIdentities posts the setup reminder, once per process start of
// an identity's adapter, to that identity's top-level direct-message
// bindings whose heartbeat is unset and whose last reminder is absent or
// older than ReminderInterval. Only conversations this daemon serves are
// reminded: never the external relay, an adapter whose inbound is
// mediated (it may hold other processes' bindings in a shared project),
// a channel or a thread, or a flow step's session. An adapter that cannot
// tell a direct message (no bridge.DirectPeerChecker) gets no reminder.
// router.heartbeatReminder: false turns the reminder off.
func (s *Service) remindNewIdentities(ctx context.Context, now time.Time) {
	if !s.cfg.HeartbeatReminderEnabled() {
		return
	}
	s.mu.Lock()
	adapters := make([]bridge.Adapter, 0, len(s.adapters))
	for _, a := range s.adapters {
		adapters = append(adapters, a)
	}
	s.mu.Unlock()

	for _, a := range adapters {
		channel, identity := a.Channel(), a.Identity()
		s.heartbeat.mu.Lock()
		done := s.heartbeat.reminded[adapterKey(channel, identity)]
		s.heartbeat.reminded[adapterKey(channel, identity)] = true
		s.heartbeat.mu.Unlock()
		direct, ok := a.(bridge.DirectPeerChecker)
		if done || !ok || !servesHeartbeats(a) {
			continue
		}
		bindings, err := s.store.ListBindingsByIdentity(ctx, s.projectID, channel, identity)
		if err != nil {
			logging.Warn("bridge: heartbeat reminder binding lookup failed", "identity", identity, "err", err)
			continue
		}
		for _, b := range bindings {
			s.remindBinding(ctx, direct, b, now)
		}
	}
}

// remindBinding posts the setup reminder to one binding if it is due.
// reminded_at is written before the post so a failed or crashed post can
// never turn into a burst of repeats.
func (s *Service) remindBinding(ctx context.Context, direct bridge.DirectPeerChecker, b store.Binding, now time.Time) {
	if b.SessionID == "" || strings.Contains(b.PeerID, "|") || !s.remindableSession(ctx, b.SessionID) {
		return
	}
	row, err := s.store.GetHeartbeat(ctx, s.projectID, b.Channel, b.IdentityID, b.PeerID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		row = store.Heartbeat{ProjectID: s.projectID, Channel: b.Channel, IdentityID: b.IdentityID, PeerID: b.PeerID}
		row.State = heartbeat.StateUnset
	case err != nil:
		logging.Warn("bridge: heartbeat reminder lookup failed", "peer", b.PeerID, "err", err)
		return
	}
	if row.State != heartbeat.StateUnset {
		return
	}
	if !row.RemindedAt.IsZero() && now.Sub(row.RemindedAt) < heartbeat.ReminderInterval {
		return
	}
	// Last, as it may ask the platform.
	if !direct.IsDirectPeer(ctx, b.PeerID) {
		return
	}
	row.RemindedAt = now
	if err := s.store.PutHeartbeat(ctx, row); err != nil {
		logging.Warn("bridge: heartbeat reminder write failed; not posting", "peer", b.PeerID, "err", err)
		return
	}
	s.replyToPeer(ctx, b.AsPeerRef(), heartbeat.Reminder(), false, b.SessionID)
	logging.Info("bridge: heartbeat setup reminder posted", "session", b.SessionID, "peer", b.PeerID)
}

// remindableSession reports whether a bound session is a daemon
// conversation the setup reminder is for: it has messages, it is not a
// flow step's or subagent's session (whose root is another session), and
// no interactive flow step owns it.
func (s *Service) remindableSession(ctx context.Context, sessionID string) bool {
	if s.app == nil || s.app.Sessions == nil {
		return false
	}
	sess, err := s.app.Sessions.Get(ctx, sessionID)
	if err != nil || sess.MessageCount == 0 {
		return false
	}
	if sess.RootSessionID != "" && sess.RootSessionID != sess.ID {
		return false
	}
	return s.app.Permissions == nil || !s.app.Permissions.IsInteractiveSession(sessionID)
}

// cmdHeartbeat implements /heartbeat. The exact grammar (see
// heartbeat.ParseCommand), including a bare /heartbeat, is applied here
// without a model call. Anything else is natural language: the handler
// returns nil and dispatchInbound hands the request to the agent (see
// heartbeatAgentRequest), which applies it with the heartbeat tool.
func (s *Service) cmdHeartbeat(ctx context.Context, in bridge.Inbound) *bridge.CommandReply {
	if !s.HeartbeatsEnabled() {
		return replyText("Heartbeats are only available when opencode runs as a daemon.")
	}
	cmd, err := heartbeat.ParseCommand(in.CommandArgs)
	if err == nil {
		text, err := s.applyHeartbeat(ctx, in.Peer, cmd)
		if err != nil {
			return replyText(err.Error() + "\n" + heartbeat.Usage)
		}
		return replyText(text)
	}
	if !s.agentHasHeartbeatTool() {
		return replyText(fmt.Sprintf("%v\n%s\nTo describe the heartbeat in your own words, enable the %q tool for the agent.",
			err, heartbeat.Usage, tools.HeartbeatToolName))
	}
	return nil
}

// heartbeatAgentRequest is the prompt a natural-language /heartbeat
// becomes. It carries the current status so the agent can apply a
// relative change ("twice as often") without a read first.
func (s *Service) heartbeatAgentRequest(ctx context.Context, in bridge.Inbound) string {
	status, err := s.heartbeatStatus(ctx, in.Peer)
	if err != nil {
		status = "(status unavailable: " + err.Error() + ")"
	}
	return heartbeat.AgentRequest(in.CommandArgs, status)
}

// agentHasHeartbeatTool reports whether the active agent can carry out a
// natural-language /heartbeat. A tool set still loading counts as yes.
func (s *Service) agentHasHeartbeatTool() bool {
	ag := s.app.ActiveAgent()
	if ag == nil {
		return false
	}
	ts, ready := ag.ResolvedTools()
	if !ready {
		return true
	}
	for _, t := range ts {
		if t.Info().Name == tools.HeartbeatToolName {
			return true
		}
	}
	return false
}

// HeartbeatStatus implements tools.HeartbeatConfigurer: the status of
// every chat binding of the session.
func (s *Service) HeartbeatStatus(ctx context.Context, sessionID string) (string, error) {
	return s.forSessionBindings(ctx, sessionID, func(peer bridge.PeerRef) (string, error) {
		return s.heartbeatStatus(ctx, peer)
	})
}

// ApplyHeartbeat implements tools.HeartbeatConfigurer: applies cmd to
// every chat binding of the session.
func (s *Service) ApplyHeartbeat(ctx context.Context, sessionID string, cmd heartbeat.Command) (string, error) {
	return s.forSessionBindings(ctx, sessionID, func(peer bridge.PeerRef) (string, error) {
		return s.applyHeartbeat(ctx, peer, cmd)
	})
}

func (s *Service) forSessionBindings(ctx context.Context, sessionID string, fn func(bridge.PeerRef) (string, error)) (string, error) {
	if !s.HeartbeatsEnabled() {
		return "", tools.ErrHeartbeatUnavailable
	}
	bindings, err := s.store.ListBindingsBySession(ctx, s.projectID, sessionID)
	if err != nil {
		return "", err
	}
	if len(bindings) == 0 {
		return "", errors.New("this session is not bound to a chat, so it has no heartbeat")
	}
	parts := make([]string, 0, len(bindings))
	for _, b := range bindings {
		text, err := fn(b.AsPeerRef())
		if err != nil {
			return "", err
		}
		if len(bindings) > 1 {
			text = fmt.Sprintf("[%s %s]\n%s", b.Channel, b.PeerID, text)
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n\n"), nil
}

func (s *Service) loadHeartbeat(ctx context.Context, peer bridge.PeerRef) (store.Heartbeat, error) {
	row, err := s.store.GetHeartbeat(ctx, s.projectID, peer.Channel, peer.Identity, peer.PeerID)
	if errors.Is(err, store.ErrNotFound) {
		row = store.Heartbeat{ProjectID: s.projectID, Channel: peer.Channel, IdentityID: peer.Identity, PeerID: peer.PeerID}
		row.State = heartbeat.StateUnset
		return row, nil
	}
	if err != nil {
		return store.Heartbeat{}, fmt.Errorf("failed to read the heartbeat: %w", err)
	}
	return row, nil
}

func (s *Service) heartbeatStatus(ctx context.Context, peer bridge.PeerRef) (string, error) {
	row, err := s.loadHeartbeat(ctx, peer)
	if err != nil {
		return "", err
	}
	return heartbeat.Describe(row.Record, s.activeModelID()), nil
}

func (s *Service) activeModelID() string {
	if ag := s.app.ActiveAgent(); ag != nil {
		return string(ag.Model().ID)
	}
	return ""
}

// applyHeartbeat applies a parsed /heartbeat command to one binding and
// returns the resulting status, with notes for anything worth knowing.
func (s *Service) applyHeartbeat(ctx context.Context, peer bridge.PeerRef, cmd heartbeat.Command) (string, error) {
	if cmd.Model != nil && *cmd.Model != "" {
		if err := checkHeartbeatModel(*cmd.Model); err != nil {
			return "", err
		}
	}
	row, err := s.loadHeartbeat(ctx, peer)
	if err != nil {
		return "", err
	}

	now := heartbeatNow().UTC()
	var notes []string
	if cmd.ChangesSettings() {
		row.Settings = cmd.Apply(row.Settings)
		if row.State == heartbeat.StateOn {
			row.NextBeatAt = heartbeat.NextBeat(now, row.Settings)
		} else {
			row.NextBeatAt = time.Time{}
		}
		if err := s.store.PutHeartbeat(ctx, row); err != nil {
			return "", fmt.Errorf("failed to save the heartbeat: %w", err)
		}
		if cmd.Model != nil && *cmd.Model != "" {
			notes = append(notes, "A different model than the session's cannot reuse its prompt cache, so each beat re-reads the whole conversation at full price.")
		}
		if row.State == heartbeat.StateOn {
			if empty, why := agendaEmpty(row.EffectiveAgendaFile()); empty {
				notes = append(notes, fmt.Sprintf("Beats are skipped until the agenda has something in it (%s).", why))
			}
			if !servesHeartbeats(s.Adapter(peer.Channel, peer.Identity)) {
				notes = append(notes, "Scheduled beats do not run for this chat: this process does not own the bot's connection (its inbound is mediated), and only the owner schedules beats. /heartbeat now still works.")
			}
		}
	}
	if cmd.Now {
		note := "this chat has no session yet"
		if b, err := s.store.GetBinding(ctx, s.projectID, peer.Channel, peer.Identity, peer.PeerID); err == nil && b.SessionID != "" {
			_, note = s.fireHeartbeat(ctx, row, b, now, true)
		}
		notes = append(notes, "Beat now: "+note+".")
	}

	text := heartbeat.Describe(row.Record, s.activeModelID())
	if len(notes) > 0 {
		text += "\n" + strings.Join(notes, "\n")
	}
	return text, nil
}

// checkHeartbeatModel validates a `/heartbeat model <id>`: a supported
// model whose provider is configured and enabled, as the agent factory
// requires when it builds the beat's model-override agent.
func checkHeartbeatModel(id string) error {
	m, ok := models.SupportedModels[models.ModelID(id)]
	if !ok {
		return fmt.Errorf("unknown model %q (/model lists the supported ones)", id)
	}
	if cfg := config.Get(); cfg != nil {
		if p, ok := cfg.Providers[m.Provider]; !ok || p.Disabled {
			return fmt.Errorf("model %q needs the %s provider, which is not configured", id, m.Provider)
		}
	}
	return nil
}
