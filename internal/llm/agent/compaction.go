package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
)

// resolveCompactionThreshold picks the threshold for one Run. Precedence:
// the flow step's compact.threshold (RunOptions) > the agent's own
// compactionThreshold > AutoCompactionThreshold. The agent value is read from
// the registry-merged field first; the config lookup covers agents built
// without registry info.
func (a *agent) resolveCompactionThreshold(opts RunOptions) float64 {
	if opts.CompactionThreshold > 0 {
		return effectiveCompactionThreshold(opts.CompactionThreshold)
	}
	if a.compactionThreshold > 0 {
		return effectiveCompactionThreshold(a.compactionThreshold)
	}
	if cfg := config.Get(); cfg != nil {
		if agentCfg, ok := cfg.Agents[a.agentID]; ok && agentCfg.CompactionThreshold > 0 {
			return effectiveCompactionThreshold(agentCfg.CompactionThreshold)
		}
	}
	return AutoCompactionThreshold
}

// countContextTokens is the context size every auto-compaction decision uses:
// the provider estimate, floored by what the provider actually reported for
// the session's last call. PromptTokens+CompletionTokens (see TrackUsage) is
// that call's full prompt plus output, i.e. the history up to and including
// the last assistant message; only the messages after it still need a local
// estimate. The provider's own hit flag is recomputed against the floored
// value.
func (a *agent) countContextTokens(ctx context.Context, sessionID string, threshold float64, msgs []message.Message, toolSet []tools.BaseTool) (int64, bool) {
	estimated, providerHit := a.provider.CountTokens(ctx, threshold, msgs, toolSet)
	sess, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return estimated, providerHit
	}
	final := estimated
	if lastAssistant := lastAssistantIndex(msgs); lastAssistant >= 0 {
		reported := sess.PromptTokens + sess.CompletionTokens
		tail := message.EstimateTokens(msgs[lastAssistant+1:], nil, message.BytesPerTokenEta)
		floor := reported + tail
		if floor > final {
			if float64(floor) > float64(estimated)*1.1 {
				logging.Info("token estimate corrected by reported usage",
					"session_id", sessionID,
					"estimate", estimated,
					"reported", reported,
					"tail", tail,
				)
			}
			final = floor
		}
	}
	window := a.provider.Model().ContextWindow
	hit := window > 0 && final >= int64(float64(window)*threshold)
	return final, hit
}

func lastAssistantIndex(msgs []message.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == message.Assistant {
			return i
		}
	}
	return -1
}

// syntheticTail returns a copy of the run of synthetic messages that ends the
// history, starting on an assistant message so no tool result is carried
// without its tool call. On an auto-resume turn that run is the completion
// pair(s) task.EnqueueTaskCompletion wrote: the input the model has to react
// to. A compaction lands its summary after them, so the reload drops them;
// callers re-append this tail in memory, as preserveTail does in the loop.
func syntheticTail(msgs []message.Message) []message.Message {
	start := len(msgs)
	for start > 0 && msgs[start-1].Synthetic {
		start--
	}
	for start < len(msgs) && msgs[start].Role != message.Assistant {
		start++
	}
	return slices.Clone(msgs[start:])
}

// Bounds on how long a turn waits for an in-flight Summarize of its session.
// Vars so tests can shorten them.
var (
	summarizeWaitTimeout = 2 * time.Minute
	summarizeWaitPoll    = 100 * time.Millisecond
)

// waitForSummarize blocks while an async Summarize of the session runs on
// this agent (the TUI starts one after a turn that ended near the window). It
// does not take the session slot, so without the wait the pre-turn gate would
// compact the same history concurrently and write a second summary. Returns
// true when it waited and the history must be reloaded; false when nothing
// was in flight or ctx ended first.
func (a *agent) waitForSummarize(ctx context.Context, sessionID string) bool {
	key := sessionID + "-summarize"
	if _, busy := a.activeRequests.Load(key); !busy {
		return false
	}
	logging.Info("Waiting for in-flight summarization before turn", "session_id", sessionID)
	deadline := time.NewTimer(summarizeWaitTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(summarizeWaitPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			logging.Warn("In-flight summarization still running; continuing the turn", "session_id", sessionID, "waited", summarizeWaitTimeout)
			return true
		case <-ticker.C:
			if _, busy := a.activeRequests.Load(key); !busy {
				return true
			}
		}
	}
}

// forgetDeferredAnnouncements clears the session's in-memory set of announced
// deferred tools. Called after a compaction: the delta message that announced
// them now sits before the summary, out of the model's view, so the next turn
// has to announce them again.
func (a *agent) forgetDeferredAnnouncements(sessionID string) {
	a.deferredAnnouncedMu.Lock()
	defer a.deferredAnnouncedMu.Unlock()
	a.deferredAnnounced.Delete(sessionID)
}

// pendingUserMessage mirrors what createUserMessage persists, without
// persisting it, so the pre-turn gate can count the incoming turn.
func pendingUserMessage(content string, attachmentParts []message.ContentPart) message.Message {
	parts := []message.ContentPart{message.TextContent{Text: content}}
	parts = append(parts, attachmentParts...)
	return message.Message{Role: message.User, Parts: parts}
}

// historyAfterCompaction reloads the session and its history from the new
// summary onwards. The in-loop caller still re-injects the struct_output
// schema envelope and the task-budget ctx.
func (a *agent) historyAfterCompaction(ctx context.Context, sessionID string) ([]message.Message, session.Session, error) {
	msgs, err := a.messages.List(ctx, sessionID)
	if err != nil {
		return nil, session.Session{}, fmt.Errorf("failed to reload messages after compaction: %w", err)
	}
	sess, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, session.Session{}, fmt.Errorf("failed to get session after compaction: %w", err)
	}
	msgs = a.filterMessagesFromSummary(msgs, sess.SummaryMessageID)
	msgs = filterEmptyUserMessages(msgs)
	return msgs, sess, nil
}

// withTaskBudgetRemaining carries the task budget across compaction: the
// provider is told how much of it remains.
func (a *agent) withTaskBudgetRemaining(ctx context.Context, sess session.Session) context.Context {
	cfg := config.Get()
	if cfg == nil {
		return ctx
	}
	agentCfg, ok := cfg.Agents[a.agentID]
	if !ok || agentCfg.TaskBudget <= 0 {
		return ctx
	}
	remaining := max(agentCfg.TaskBudget-sess.TotalCompletionTokens, 0)
	return provider.TaskBudgetRemainingContext(ctx, remaining)
}

// summarizerInput builds what the summarizer is sent: the history since the
// previous summary (kept as the head, so its knowledge carries forward), cut
// from the oldest end until it fits the summarizer's window, then the
// compaction prompt. The cut keeps the turn's prompt (turnPromptIndex): a
// flow step's task is the oldest message of its turn, so it would go first,
// and the in-loop rebuild does not re-append it, so a summary written without
// it loses the task for the rest of the step. Sending the raw message log
// instead grows without bound across compactions, and a session that already
// overflowed could never compact its way out.
func (a *agent) summarizerInput(ctx context.Context, sessionID string, prompt message.Message) ([]message.Message, error) {
	msgs, err := a.messages.List(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to list messages: %w", err)
	}
	sess, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}
	msgs = a.filterMessagesFromSummary(msgs, sess.SummaryMessageID)
	msgs = filterEmptyUserMessages(msgs)
	if len(msgs) == 0 {
		return nil, errNoMessagesToSummarize
	}

	window := a.summarizeProvider.Model().ContextWindow
	if window > 0 {
		fixed := message.EstimateTokens([]message.Message{prompt}, nil, message.BytesPerTokenEta)
		if sp, ok := a.summarizeProvider.(interface{ SystemMessage() string }); ok {
			fixed += int64(len(sp.SystemMessage()) / message.BytesPerTokenEta)
		}
		budget := int64(float64(window) * summarizerWindowFraction)
		// The trim works in local-estimate units, and the 4 B/token estimate
		// can undercount a session badly. When the provider reported more for
		// the history up to the last assistant message than the estimate
		// gives, shrink the budget by the same ratio. The reported value also
		// covers the main agent's system prompt and tools, so this errs
		// toward trimming more.
		if la := lastAssistantIndex(msgs); la >= 0 {
			reported := sess.PromptTokens + sess.CompletionTokens
			if local := message.EstimateTokens(msgs[:la+1], nil, message.BytesPerTokenEta); local > 0 && reported > local {
				budget = budget * local / reported
			}
		}
		keepHead := sess.SummaryMessageID != "" && msgs[0].ID == sess.SummaryMessageID
		kept, dropped, estimated := trimSummarizerInput(msgs, keepHead, turnPromptIndex(msgs), fixed, budget)
		if dropped > 0 {
			logging.Warn("compaction input exceeded summarizer window; dropped oldest messages",
				"session_id", sessionID,
				"dropped", dropped,
				"kept", len(kept),
				"estimated_tokens", estimated,
				"budget", budget,
				"window", window,
			)
			msgs = kept
		}
	}
	return append(slices.Clip(msgs), prompt), nil
}

// turnPromptIndex returns the index of the latest user message that is not
// synthetic and has text, or -1: the turn's prompt, or on an auto-resume turn
// the last one before it. Synthetic user messages (the schema envelope, the
// deferred-tools delta) are skipped.
func turnPromptIndex(msgs []message.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if m := msgs[i]; m.Role == message.User && !m.Synthetic && strings.TrimSpace(m.Content().Text) != "" {
			return i
		}
	}
	return -1
}

// trimSummarizerInput drops messages from the oldest end until the estimate
// plus fixed (system prompt, compaction prompt) is under budget. keepHead
// protects msgs[0] — the previous summary — while anything else remains to
// drop. prompt is the index of the turn's prompt, or -1: a cut that passes it
// keeps it, right after the head, and it stays counted in the budget. A
// prompt that alone takes more than half the space left after the head and
// fixed is dropped like any other message, and so is one whose keeping would
// leave nothing after the cut or the input over budget. A cut never leaves a tool result at the front without the
// assistant tool call it answers: dropping only removes a prefix, so the one
// way to split a pair is an orphaned Tool message right after the cut, and
// those go too. When anything was dropped, a note saying so is inserted where
// the cut is, which also keeps the input from starting on an assistant turn.
// Returns the kept messages, the number dropped, and the final estimate.
func trimSummarizerInput(msgs []message.Message, keepHead bool, prompt int, fixed, budget int64) ([]message.Message, int, int64) {
	sizes := make([]int64, len(msgs))
	var total int64
	for i := range msgs {
		sizes[i] = message.EstimateTokens(msgs[i:i+1], nil, message.BytesPerTokenEta)
		total += sizes[i]
	}
	if total+fixed < budget {
		return msgs, 0, total + fixed
	}

	start := 0
	if keepHead {
		start = 1
	}
	avail := budget - fixed
	if keepHead {
		avail -= sizes[0]
	}
	if prompt < start || prompt >= len(msgs) || sizes[prompt] > avail/2 {
		prompt = -1
	}
	cut := start
	for cut < len(msgs)-1 && total+fixed >= budget {
		if cut != prompt {
			total -= sizes[cut]
		}
		cut++
	}
	for cut < len(msgs) && msgs[cut].Role == message.Tool {
		total -= sizes[cut]
		cut++
	}
	keptPrompt := prompt >= 0 && prompt < cut
	if keptPrompt && (total+fixed >= budget || cut >= len(msgs)) {
		// Keeping the prompt left nothing of the recent history, or still
		// does not fit: trim as if it were any other message.
		return trimSummarizerInput(msgs, keepHead, -1, fixed, budget)
	}
	dropped := cut - start
	if keptPrompt {
		dropped--
	}
	if dropped == 0 {
		return msgs, 0, total + fixed
	}

	note := message.Message{
		Role: message.User,
		Parts: []message.ContentPart{message.TextContent{Text: fmt.Sprintf(
			"[%d earlier messages were omitted from this compaction input to fit the summarizer's context window.]", dropped)}},
	}
	kept := make([]message.Message, 0, start+2+len(msgs)-cut)
	kept = append(kept, msgs[:start]...)
	if keptPrompt {
		kept = append(kept, msgs[prompt])
	}
	kept = append(kept, note)
	kept = append(kept, msgs[cut:]...)
	total += message.EstimateTokens([]message.Message{note}, nil, message.BytesPerTokenEta)
	return kept, dropped, total + fixed
}

// likelyContextOverflow reports whether a failed model call was sent with a
// context close enough to the window that the failure is probably an overflow.
func likelyContextOverflow(estimated, window int64) bool {
	return window > 0 && float64(estimated) >= contextOverflowWarnRatio*float64(window)
}

func (a *agent) warnIfLikelyContextOverflow(sessionID string, estimated int64, err error) {
	window := a.provider.Model().ContextWindow
	if !likelyContextOverflow(estimated, window) {
		return
	}
	logging.Warn("model call failed with context near the window; likely context overflow — compact or reset the session",
		"session_id", sessionID,
		"agent", a.agentID,
		"estimated_tokens", estimated,
		"context_window", window,
		"ratio", float64(estimated)/float64(window),
		"error", err,
	)
}
