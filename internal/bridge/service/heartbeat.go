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
// Only identities with an adapter registered in THIS process are
// scheduled: the identity lock that guarantees one adapter per identity
// across processes therefore also guarantees one scheduler per heartbeat.

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
// registered identities, then every due beat.
func (s *Service) heartbeatTick(ctx context.Context) {
	now := heartbeatNow().UTC()
	s.remindNewIdentities(ctx, now)

	rows, err := s.store.ListHeartbeats(ctx, s.projectID)
	if err != nil {
		logging.Warn("bridge: heartbeat list failed", "err", err)
		return
	}
	for _, row := range rows {
		if row.State != heartbeat.StateOn || s.Adapter(row.Channel, row.IdentityID) == nil {
			continue
		}
		if row.NextBeatAt.IsZero() {
			row.NextBeatAt = heartbeat.NextBeat(now, row.Settings)
			if err := s.store.PutHeartbeat(ctx, row); err != nil {
				logging.Warn("bridge: heartbeat schedule write failed", "peer", row.PeerID, "err", err)
			}
			continue
		}
		if row.NextBeatAt.After(now) {
			continue
		}
		s.fireHeartbeat(ctx, row, now, false)
	}
}

// fireHeartbeat queues one beat for row's binding, unless the session is
// busy (the beat then waits for a later tick, schedule unchanged) or the
// agenda is empty (recorded as skipped, no model call). A scheduled beat
// advances next_beat_at BEFORE it is queued, so a crash can never re-fire
// it in a loop; computing the next slot from now coalesces missed beats.
// manual beats (/heartbeat now) leave the schedule alone. Returns a short
// reason when nothing was queued.
func (s *Service) fireHeartbeat(ctx context.Context, row store.Heartbeat, now time.Time, manual bool) string {
	binding, err := s.store.GetBinding(ctx, s.projectID, row.Channel, row.IdentityID, row.PeerID)
	if err != nil || binding.SessionID == "" {
		return "this chat has no session yet"
	}
	disp := s.dispatcherFor(binding.SessionID)
	if disp.heartbeatQueued.Load() {
		return "a heartbeat is already running"
	}
	if ag := s.app.ActiveAgent(); (ag != nil && ag.IsSessionBusy(binding.SessionID)) || disp.hasQueuedInbound() {
		return "the session is busy; the beat will run when it is idle"
	}

	if !manual {
		row.NextBeatAt = heartbeat.NextBeat(now.Truncate(time.Minute).Add(time.Minute), row.Settings)
	}
	agenda := row.EffectiveAgendaFile()
	if empty, why := agendaEmpty(agenda); empty {
		row.LastBeatAt, row.LastStatus, row.LastError = now, heartbeat.OutcomeSkipped, why
		if err := s.store.PutHeartbeat(ctx, row); err != nil {
			logging.Warn("bridge: heartbeat write failed", "peer", row.PeerID, "err", err)
		}
		logging.Info("bridge: heartbeat skipped", "session", binding.SessionID, "reason", why)
		return "skipped: " + why
	}
	if err := s.store.PutHeartbeat(ctx, row); err != nil {
		logging.Warn("bridge: heartbeat write failed; not firing", "peer", row.PeerID, "err", err)
		return "could not save the schedule"
	}
	disp.heartbeatQueued.Store(true)
	disp.pushInbound(bridge.Inbound{
		Peer:       binding.AsPeerRef(),
		Text:       heartbeat.Prompt(now, agenda),
		ReceivedAt: now.UnixMilli(),
		Heartbeat:  &bridge.HeartbeatTurn{At: now, Model: row.Model},
	})
	logging.Info("bridge: heartbeat queued", "session", binding.SessionID, "manual", manual,
		"next", row.NextBeatAt.Format(time.RFC3339))
	return ""
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

// recordHeartbeatOutcome writes a finished beat's outcome to its row.
func (s *Service) recordHeartbeatOutcome(ctx context.Context, peer bridge.PeerRef, at time.Time, outcome, reason string) {
	row, err := s.store.GetHeartbeat(ctx, s.projectID, peer.Channel, peer.Identity, peer.PeerID)
	if err != nil {
		logging.Warn("bridge: heartbeat outcome lookup failed", "peer", peer.PeerID, "err", err)
		return
	}
	row.LastBeatAt, row.LastStatus, row.LastError = at, outcome, reason
	if err := s.store.PutHeartbeat(ctx, row); err != nil {
		logging.Warn("bridge: heartbeat outcome write failed", "peer", peer.PeerID, "err", err)
	}
}

// heartbeatDeferred puts a beat that lost the race for its session back
// on the schedule: due now, so the next tick tries again once the session
// is idle.
func (s *Service) heartbeatDeferred(ctx context.Context, peer bridge.PeerRef, at time.Time) {
	row, err := s.store.GetHeartbeat(ctx, s.projectID, peer.Channel, peer.Identity, peer.PeerID)
	if err != nil {
		return
	}
	row.NextBeatAt = at
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
// an identity's adapter, to that identity's bindings whose heartbeat is
// unset and whose last reminder is absent or older than ReminderInterval.
// reminded_at is written before the post so a failed or crashed post can
// never turn into a burst of repeats.
func (s *Service) remindNewIdentities(ctx context.Context, now time.Time) {
	s.mu.Lock()
	keys := make([][2]string, 0, len(s.adapters))
	for _, a := range s.adapters {
		keys = append(keys, [2]string{a.Channel(), a.Identity()})
	}
	s.mu.Unlock()

	for _, k := range keys {
		channel, identity := k[0], k[1]
		s.heartbeat.mu.Lock()
		done := s.heartbeat.reminded[adapterKey(channel, identity)]
		s.heartbeat.reminded[adapterKey(channel, identity)] = true
		s.heartbeat.mu.Unlock()
		if done {
			continue
		}
		bindings, err := s.store.ListBindingsByIdentity(ctx, s.projectID, channel, identity)
		if err != nil {
			logging.Warn("bridge: heartbeat reminder binding lookup failed", "identity", identity, "err", err)
			continue
		}
		for _, b := range bindings {
			s.remindBinding(ctx, b, now)
		}
	}
}

func (s *Service) remindBinding(ctx context.Context, b store.Binding, now time.Time) {
	if b.SessionID == "" || !s.sessionHasMessages(ctx, b.SessionID) {
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
	row.RemindedAt = now
	if err := s.store.PutHeartbeat(ctx, row); err != nil {
		logging.Warn("bridge: heartbeat reminder write failed; not posting", "peer", b.PeerID, "err", err)
		return
	}
	s.replyToPeer(ctx, b.AsPeerRef(), heartbeat.Reminder(), false, b.SessionID)
	logging.Info("bridge: heartbeat setup reminder posted", "session", b.SessionID, "peer", b.PeerID)
}

func (s *Service) sessionHasMessages(ctx context.Context, sessionID string) bool {
	if s.app == nil || s.app.Sessions == nil {
		return false
	}
	sess, err := s.app.Sessions.Get(ctx, sessionID)
	return err == nil && sess.MessageCount > 0
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
		if _, ok := models.SupportedModels[models.ModelID(*cmd.Model)]; !ok {
			return "", fmt.Errorf("unknown model %q (/model lists the supported ones)", *cmd.Model)
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
		}
	}
	if cmd.Now {
		if reason := s.fireHeartbeat(ctx, row, now, true); reason != "" {
			notes = append(notes, "Beat now: "+reason+".")
		} else {
			notes = append(notes, "Beat now: queued.")
		}
	}

	text := heartbeat.Describe(row.Record, s.activeModelID())
	if len(notes) > 0 {
		text += "\n" + strings.Join(notes, "\n")
	}
	return text, nil
}
