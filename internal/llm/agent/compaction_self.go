package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/task"
)

// Self-compaction: an agent that has finished a task calls compact_session,
// and its session is compacted right after that turn ends, or, when that
// could not happen, before the next turn's first model call. See
// openspec/specs/agent-self-compaction/spec.md.
//
// The request is the persisted compact_session tool result itself (its
// metadata carries tools.CompactSessionRequest). It lives in the message log
// after the current summary until a compaction writes a new summary after
// it, so a pending request survives a restart, and any compaction, whatever
// started it, serves every request before it exactly once.

// The self-compaction triggers, recorded on the summarizer generation.
const (
	compactionTriggerSelfAfterTurn = "self_after_turn"
	compactionTriggerSelfPreTurn   = "self_pre_turn"
)

func isSelfCompactionTrigger(trigger string) bool {
	return trigger == compactionTriggerSelfAfterTurn || trigger == compactionTriggerSelfPreTurn
}

// selfCompactionTraceSuffix ends the name of a self-compaction's summarizer
// trace (`<agent>/self-compaction`), so one the agent asked for is told
// apart, by name, from the agent's turns and its automatic compactions.
const selfCompactionTraceSuffix = "/self-compaction"

// compactionTraceNameSuffix is what a synchronous compaction's summarizer
// trace appends to the usual trace name: selfCompactionTraceSuffix for a
// self-compaction, nothing for an automatic one.
func compactionTraceNameSuffix(trigger string) string {
	if isSelfCompactionTrigger(trigger) {
		return selfCompactionTraceSuffix
	}
	return ""
}

// compactionTraceInput is a synchronous compaction's summarizer trace input.
func compactionTraceInput(trigger string, messages int) string {
	if isSelfCompactionTrigger(trigger) {
		return fmt.Sprintf("self-compaction requested by agent over %d messages", messages)
	}
	return fmt.Sprintf("auto-compaction over %d messages", messages)
}

// selfCompactSettings reads an agent's selfCompact: the registry-merged
// value (frontmatter and config overlay), else the config entry for agents
// built without registry info. Zero value when unset.
func selfCompactSettings(info *agentregistry.AgentInfo) config.SelfCompact {
	if info != nil && info.SelfCompact != nil {
		return *info.SelfCompact
	}
	if cfg := config.Get(); cfg != nil && info != nil {
		if sc := cfg.Agents[config.AgentName(info.ID)].SelfCompact; sc != nil {
			return *sc
		}
	}
	return config.SelfCompact{}
}

// pendingSelfCompactionNotes reports whether msgs (the history from the
// current summary on) hold a stored compact_session request, and returns the
// requests' keep notes in order, without exact duplicates.
func pendingSelfCompactionNotes(msgs []message.Message) (bool, []string) {
	pending := false
	var notes []string
	for _, m := range msgs {
		if m.Role != message.Tool {
			continue
		}
		for _, r := range m.ToolResults() {
			if r.Name != tools.CompactSessionToolName || r.IsError {
				continue
			}
			req, ok := tools.ParseCompactSessionRequest(r.Metadata)
			if !ok {
				continue
			}
			pending = true
			if req.Keep != "" && !slices.Contains(notes, req.Keep) {
				notes = append(notes, req.Keep)
			}
		}
	}
	return pending, notes
}

// withCarryForward appends the agent's keep notes to the compaction prompt.
func withCarryForward(prompt message.Message, notes []string) message.Message {
	if len(notes) == 0 {
		return prompt
	}
	text := prompt.Content().Text + "\n\nThe agent asked for this compaction. Carry these forward verbatim:\n\n" + strings.Join(notes, "\n\n")
	return message.Message{Role: prompt.Role, Parts: []message.ContentPart{message.TextContent{Text: text}}}
}

// sessionContextTokens is compact_session's counter: the session's history
// from the current summary on, counted by countContextTokens, the same
// count every auto-compaction decision uses.
func (a *agent) sessionContextTokens(ctx context.Context, sessionID string) (int64, error) {
	msgs, _, err := a.historyAfterCompaction(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	tokens, _ := a.countContextTokens(ctx, sessionID, a.resolveCompactionThreshold(RunOptions{}), msgs, a.resolveTools())
	return tokens, nil
}

// runSelfCompaction compacts the session for a pending request. msgs is the
// history from the current summary on. It honours the auto-compaction
// backoff, since a pending request is retried without the agent asking
// again, and records its outcome there. afterSeq is the last message the
// turn knew of: synthetic messages written after it (a background task's
// completion) that the new summary would hide are moved after it. Returns
// true when it compacted.
func (a *agent) runSelfCompaction(ctx context.Context, sessionID, trigger string, msgs []message.Message, afterSeq int64) bool {
	toolSet := a.resolveTools()
	threshold := a.resolveCompactionThreshold(RunOptions{})
	before, _ := a.countContextTokens(ctx, sessionID, threshold, msgs, toolSet)
	if !a.compactionAllowed(sessionID, before) {
		return false
	}
	logging.Info("self-compaction requested by agent: compacting",
		"session_id", sessionID,
		"agent", a.agentID,
		"trigger", trigger,
		"tokens_before", before,
	)
	if err := a.autoCompact(ctx, sessionID, trigger, before); err != nil {
		return false
	}
	if err := a.moveLateMessagesAfterSummary(ctx, sessionID, afterSeq); err != nil {
		logging.Warn("self-compaction: moving messages written during the compaction after the summary failed", "session_id", sessionID, "error", err)
	}
	after := int64(-1)
	if reloaded, _, err := a.historyAfterCompaction(ctx, sessionID); err == nil {
		after, _ = a.countContextTokens(ctx, sessionID, threshold, reloaded, toolSet)
	}
	logging.Info("self-compaction requested by agent: completed",
		"session_id", sessionID,
		"agent", a.agentID,
		"trigger", trigger,
		"tokens_before", before,
		"tokens_after", after,
	)
	return true
}

// lastSeq is the sequence number of the newest message in msgs, 0 when
// there is none.
func lastSeq(msgs []message.Message) int64 {
	var seq int64
	for _, m := range msgs {
		seq = max(seq, m.Seq)
	}
	return seq
}

// moveLateMessagesAfterSummary re-creates, after the session's summary, the
// synthetic messages written after afterSeq and before the summary, and
// deletes the originals. A background task's completion pair is written
// without the session's run slot, so it can land while a compaction holds
// the slot; the summary is then written after it and every later reload
// would drop it. A tool call and its result are re-created as one pair, so a
// completion arriving meanwhile cannot split them.
func (a *agent) moveLateMessagesAfterSummary(ctx context.Context, sessionID string, afterSeq int64) error {
	all, err := a.messages.List(ctx, sessionID)
	if err != nil {
		return err
	}
	sess, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	summaryAt := slices.IndexFunc(all, func(m message.Message) bool { return m.ID == sess.SummaryMessageID })
	if summaryAt < 0 {
		return nil
	}
	var late []message.Message
	for _, m := range all[:summaryAt] {
		if m.Synthetic && m.Seq > afterSeq {
			late = append(late, m)
		}
	}
	for i := 0; i < len(late); i++ {
		m := late[i]
		if m.Role == message.Assistant && i+1 < len(late) && late[i+1].Role == message.Tool {
			if _, _, err := a.messages.CreatePair(ctx, sessionID, recreateParams(m), recreateParams(late[i+1])); err != nil {
				return err
			}
			if err := a.messages.Delete(ctx, m.ID); err != nil {
				return err
			}
			i++
			if err := a.messages.Delete(ctx, late[i].ID); err != nil {
				return err
			}
			continue
		}
		if _, err := a.messages.Create(ctx, sessionID, recreateParams(m)); err != nil {
			return err
		}
		if err := a.messages.Delete(ctx, m.ID); err != nil {
			return err
		}
	}
	if len(late) > 0 {
		logging.Info("self-compaction: moved messages written during the compaction after the summary", "session_id", sessionID, "messages", len(late))
	}
	return nil
}

// recreateParams copies m for re-creation. The store appends a finish part
// to every non-assistant message it creates, so m's own is dropped.
func recreateParams(m message.Message) message.CreateMessageParams {
	parts := m.Parts
	if m.Role != message.Assistant {
		parts = slices.DeleteFunc(slices.Clone(parts), func(p message.ContentPart) bool {
			_, isFinish := p.(message.Finish)
			return isFinish
		})
	}
	return message.CreateMessageParams{Role: m.Role, Parts: parts, Model: m.Model, Synthetic: m.Synthetic}
}

// postTurnCompactionTimeout bounds a post-turn compaction, which runs
// detached from the caller's ctx. A var so tests can shorten it.
var postTurnCompactionTimeout = 10 * time.Minute

// compactAfterTurn serves a request the agent stored during the turn that
// just ended. RunWith calls it after the turn's terminal event is sent and
// before the events channel closes, still holding the session's run slot:
// no other run, Summarize or SummarizeSync of the session can interleave,
// and a caller draining the channel (the bridge dispatcher) holds its next
// message until the compaction is done. When the caller marked the context
// with WithTurnDelivery, the compaction waits until the caller says the
// terminal event is delivered. A failed or cancelled turn leaves the request
// pending for the next turn's pre-turn check.
//
// It runs on runCtx's values but not its cancellation: a caller may stop
// listening as soon as it has the terminal event. Only stop (the agent's
// own Cancel) and postTurnCompactionTimeout end it. A flow step's run is
// skipped: the flow runner reuses the session as soon as it has the
// terminal event (a retry, a re-prompt, the next step), and a slot held past
// the turn would refuse that run as busy. Its request is served before the
// session's next turn.
//
// The returned func, when not nil, is for RunWith to call once the slot is
// released: it starts the auto-resume of a background-task completion that
// arrived while the session was held for the compaction.
func (a *agent) compactAfterTurn(runCtx, stop context.Context, sessionID string, result AgentEvent) (afterRelease func()) {
	defer logging.RecoverPanic("agent.compactAfterTurn", nil)
	if !a.selfCompact.Enabled || result.Error != nil || stop.Err() != nil {
		return nil
	}
	if tools.StepScopedContext(runCtx) != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(runCtx), postTurnCompactionTimeout)
	defer cancel()
	defer context.AfterFunc(stop, cancel)()

	msgs, _, err := a.historyAfterCompaction(ctx, sessionID)
	if err != nil {
		logging.Warn("self-compaction: reading the session failed; the request stays pending", "session_id", sessionID, "error", err)
		return nil
	}
	if pending, _ := pendingSelfCompactionNotes(msgs); !pending {
		return nil
	}
	// From here on the session stays held well past its turn. A skipped
	// resume recorded before now was the turn's own to react to.
	task.TakeSkippedResume(sessionID)
	afterRelease = func() {
		if r, ok := task.TakeSkippedResume(sessionID); ok {
			logging.Info("self-compaction: resuming for a background completion that arrived during the compaction", "session_id", sessionID)
			task.ResumeIfIdle(sessionID, r)
		}
	}
	if !waitTurnDelivered(ctx, sessionID) {
		return afterRelease
	}
	a.runSelfCompaction(ctx, sessionID, compactionTriggerSelfAfterTurn, msgs, lastSeq(msgs))
	return afterRelease
}

type turnDeliveryKey struct{}

// turnDeliveryWait bounds how long a post-turn compaction waits for the
// caller's delivery signal. A var so tests can shorten it.
var turnDeliveryWait = 2 * time.Minute

// WithTurnDelivery marks ctx for a Run whose caller delivers the turn's
// terminal event somewhere slow, such as a chat post. A compaction the agent
// requested during the turn then starts only once the returned func is
// called (or after a bounded wait). Calling the func more than once is safe.
func WithTurnDelivery(ctx context.Context) (context.Context, func()) {
	ch := make(chan struct{})
	var once sync.Once
	return context.WithValue(ctx, turnDeliveryKey{}, (<-chan struct{})(ch)), func() { once.Do(func() { close(ch) }) }
}

// TurnDelivery returns the channel WithTurnDelivery's func closes, or nil
// when ctx carries none.
func TurnDelivery(ctx context.Context) <-chan struct{} {
	ch, _ := ctx.Value(turnDeliveryKey{}).(<-chan struct{})
	return ch
}

// waitTurnDelivered blocks until the caller's delivery signal, when ctx
// carries one. False when ctx ended first.
func waitTurnDelivered(ctx context.Context, sessionID string) bool {
	ch := TurnDelivery(ctx)
	if ch == nil {
		return true
	}
	timer := time.NewTimer(turnDeliveryWait)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		logging.Warn("self-compaction: the turn's delivery was not confirmed in time; compacting anyway", "session_id", sessionID, "waited", turnDeliveryWait)
		return true
	}
}
