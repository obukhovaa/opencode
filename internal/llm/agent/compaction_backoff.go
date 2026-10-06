package agent

import (
	"context"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/logging"
)

const (
	// compactionBackoffBase is the wait after the first failed auto-compaction;
	// it doubles with each further failure up to compactionBackoffMax.
	compactionBackoffBase = time.Minute
	compactionBackoffMax  = 30 * time.Minute
	// compactionBackoffGrowth is the share of the context window the session
	// has to grow by since the last failure to retry before the wait is over.
	// A session that keeps growing toward the window must not wait out the
	// full backoff and overflow.
	compactionBackoffGrowth = 0.10
)

// compactionBackoff is one session's auto-compaction failure state. Without
// it, a summarizer that keeps failing is retried before every model call: the
// session stays over the threshold because only a successful compaction
// lowers its token count, so each step pays a failing summarizer call first.
type compactionBackoff struct {
	failures      int
	lastFailureAt time.Time
	tokensAtFail  int64
	// failTurn is the turn the last failure happened in. A turn that saw a
	// failure does not retry, however short the wait: the backoff is in
	// minutes, and a daemon turn rarely lasts one.
	failTurn uint64
}

// compactionBackoffs holds the per-session state in memory. A long-lived
// process (daemon, server) keeps it across turns; a restart starts clean,
// which costs at most one extra attempt.
type compactionBackoffs struct {
	mu       sync.Mutex
	sessions map[string]*compactionBackoff
	turns    map[string]uint64
	now      func() time.Time
}

func (b *compactionBackoffs) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

func (b *compactionBackoffs) state(sessionID string) *compactionBackoff {
	if b.sessions == nil {
		b.sessions = map[string]*compactionBackoff{}
	}
	st, ok := b.sessions[sessionID]
	if !ok {
		st = &compactionBackoff{}
		b.sessions[sessionID] = st
	}
	return st
}

// startTurn marks the start of a turn for the session.
func (b *compactionBackoffs) startTurn(sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.turns == nil {
		b.turns = map[string]uint64{}
	}
	b.turns[sessionID]++
}

// allow reports whether an auto-compaction may run now, and when not, why
// and when the next one may. tokens is the session's current context count
// and window the main model's context window.
func (b *compactionBackoffs) allow(sessionID string, tokens, window int64) (ok bool, reason string, retryAt time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.state(sessionID)
	if st.failures == 0 {
		return true, "", time.Time{}
	}
	// Growth beats every wait: a session heading for its window has to get
	// another try before it overflows.
	if window > 0 && tokens-st.tokensAtFail >= int64(float64(window)*compactionBackoffGrowth) {
		return true, "", time.Time{}
	}
	retryAt = st.lastFailureAt.Add(compactionBackoffDelay(st.failures))
	if st.failTurn == b.turns[sessionID] {
		return false, "compaction already failed this turn", retryAt
	}
	if b.clock().Before(retryAt) {
		return false, "backing off after a failed compaction", retryAt
	}
	return true, "", time.Time{}
}

// failed records a failed compaction and returns the failure count and when
// the next attempt may run.
func (b *compactionBackoffs) failed(sessionID string, tokens int64) (int, time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.state(sessionID)
	st.failures++
	st.lastFailureAt = b.clock()
	st.tokensAtFail = tokens
	st.failTurn = b.turns[sessionID]
	return st.failures, st.lastFailureAt.Add(compactionBackoffDelay(st.failures))
}

// succeeded clears the failure state.
func (b *compactionBackoffs) succeeded(sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.state(sessionID)
	st.failures = 0
	st.lastFailureAt = time.Time{}
	st.tokensAtFail = 0
}

// failureCount returns the session's current consecutive failure count.
func (b *compactionBackoffs) failureCount(sessionID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state(sessionID).failures
}

func compactionBackoffDelay(failures int) time.Duration {
	if failures < 1 {
		return 0
	}
	d := compactionBackoffBase
	for i := 1; i < failures && d < compactionBackoffMax; i++ {
		d *= 2
	}
	return min(d, compactionBackoffMax)
}

// What started a compaction, as recorded on the summarizer generation.
const (
	compactionTriggerPreTurn = "pre_turn"
	compactionTriggerInLoop  = "in_loop"
	compactionTriggerManual  = "manual"
)

// compactionAllowed reports whether an auto-compaction may run for the
// session now. A skip logs at debug: the backoff exists so a failing
// summarizer stops costing every step, and a warn per skipped step would
// trade that noise for log noise.
func (a *agent) compactionAllowed(sessionID string, tokens int64) bool {
	ok, reason, retryAt := a.compactionBackoff.allow(sessionID, tokens, a.provider.Model().ContextWindow)
	if !ok {
		logging.Debug("auto-compaction skipped",
			"session_id", sessionID,
			"reason", reason,
			"token_count", tokens,
			"retry_at", retryAt,
		)
	}
	return ok
}

// autoCompact runs one auto-compaction and records its outcome in the
// session's backoff. Callers check compactionAllowed first.
func (a *agent) autoCompact(ctx context.Context, sessionID, trigger string, tokens int64) error {
	if err := a.performSynchronousCompaction(ctx, sessionID, trigger); err != nil {
		failures, retryAt := a.compactionBackoff.failed(sessionID, tokens)
		logging.Warn("auto-compaction failed; backing off",
			"session_id", sessionID,
			"trigger", trigger,
			"failures", failures,
			"next_attempt_after", retryAt,
			"token_count", tokens,
			"error", err,
		)
		return err
	}
	a.compactionBackoff.succeeded(sessionID)
	return nil
}

// compactionMetadata is what the summarizer generation records about its
// input, so a failure can be diagnosed without logging the input itself.
func (a *agent) compactionMetadata(sessionID, trigger string, stats compactionInputStats) map[string]any {
	return map[string]any{
		"compaction.trigger":                 trigger,
		"compaction.estimated_input_tokens":  stats.estimatedTokens,
		"compaction.messages":                stats.messages,
		"compaction.trimmed_messages":        stats.trimmedMessages,
		"compaction.truncated_tool_payloads": stats.truncatedToolPayloads,
		"compaction.failures":                a.compactionBackoff.failureCount(sessionID),
	}
}
