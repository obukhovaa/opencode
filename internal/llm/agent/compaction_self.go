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

// compactionTraceInput names a synchronous compaction's summarizer trace, so
// one the agent asked for is told apart from an automatic one.
func compactionTraceInput(trigger string, messages int) string {
	if trigger == compactionTriggerSelfAfterTurn || trigger == compactionTriggerSelfPreTurn {
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
// again, and records its outcome there. Returns true when it compacted.
func (a *agent) runSelfCompaction(ctx context.Context, sessionID, trigger string, msgs []message.Message) bool {
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

// compactAfterTurn serves a request the agent stored during the turn that
// just ended. RunWith calls it after the turn's terminal event is sent and
// before the events channel closes, still holding the session's run slot:
// no other run, Summarize or SummarizeSync of the session can interleave,
// and a caller draining the channel (the bridge dispatcher) holds its next
// message until the compaction is done. When the caller marked the context
// with WithTurnDelivery, the compaction waits until the caller says the
// terminal event is delivered. A failed or cancelled turn leaves the request
// pending for the next turn's pre-turn check.
func (a *agent) compactAfterTurn(ctx context.Context, sessionID string, result AgentEvent) {
	if !a.selfCompact.Enabled || result.Error != nil || ctx.Err() != nil {
		return
	}
	msgs, _, err := a.historyAfterCompaction(ctx, sessionID)
	if err != nil {
		logging.Warn("self-compaction: reading the session failed; the request stays pending", "session_id", sessionID, "error", err)
		return
	}
	if pending, _ := pendingSelfCompactionNotes(msgs); !pending {
		return
	}
	if !waitTurnDelivered(ctx, sessionID) {
		return
	}
	a.runSelfCompaction(ctx, sessionID, compactionTriggerSelfAfterTurn, msgs)
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
