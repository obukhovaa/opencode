package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

func TestCompactionBackoffDelay(t *testing.T) {
	want := []time.Duration{0, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for failures, w := range want {
		if got := compactionBackoffDelay(failures); got != w {
			t.Errorf("compactionBackoffDelay(%d) = %v, want %v", failures, got, w)
		}
	}
}

func TestCompactionBackoff(t *testing.T) {
	const sess, window = "s", int64(1_000_000)
	now := time.Date(2026, 10, 6, 6, 0, 0, 0, time.UTC)
	b := &compactionBackoffs{now: func() time.Time { return now }}
	allowed := func(tokens int64) bool {
		ok, _, _ := b.allow(sess, tokens, window)
		return ok
	}

	b.startTurn(sess)
	if !allowed(500_000) {
		t.Fatal("no failure yet: want allowed")
	}
	b.failed(sess, 500_000)
	if allowed(500_000) {
		t.Error("same turn after a failure: want skipped")
	}

	b.startTurn(sess)
	now = now.Add(30 * time.Second)
	if allowed(520_000) {
		t.Error("next turn inside the 1 min wait, little growth: want skipped")
	}
	if !allowed(600_000) {
		t.Error("10% of the window grown since the failure: want allowed despite the wait")
	}
	now = now.Add(31 * time.Second)
	if !allowed(520_000) {
		t.Error("wait over: want allowed")
	}

	b.failed(sess, 520_000) // second failure: 2 min
	b.startTurn(sess)
	now = now.Add(90 * time.Second)
	if allowed(520_000) {
		t.Error("second failure waits 2 min: want skipped at 90 s")
	}
	now = now.Add(31 * time.Second)
	if !allowed(520_000) {
		t.Error("second failure's wait over: want allowed")
	}

	b.succeeded(sess)
	if b.failureCount(sess) != 0 || !allowed(520_000) {
		t.Error("success resets the backoff")
	}
}

// A summarizer that keeps failing is tried once, not before every model call
// of the turn, and not again on the next turn while the backoff runs.
func TestProcessGeneration_FailedCompactionBacksOff(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, true)
	a, main, summarizer, _ := newCompactingAgent(t, 960, []tools.BaseTool{noopTool{}})
	summarizer.fail = errors.New("stream error: stream ID 1; INTERNAL_ERROR; received from peer")

	calls := 0
	main.respond = func([]message.Message) *provider.ProviderResponse {
		calls++
		if calls <= 3 {
			return &provider.ProviderResponse{
				ToolCalls:    []message.ToolCall{{ID: fmt.Sprintf("call-%d", calls), Name: "noop", Input: "{}", Finished: true}},
				FinishReason: message.FinishReasonToolUse,
			}
		}
		return &provider.ProviderResponse{Content: "done", FinishReason: message.FinishReasonEndTurn}
	}

	const sess = "sess-backoff"
	seedHistory(t, a, sess, textMsg(message.User, "q"), textMsg(message.Assistant, "a"))
	ctx := context.Background()
	if res := a.processGeneration(ctx, sess, "go", 0, nil, RunOptions{}); res.Error != nil {
		t.Fatalf("processGeneration: %v", res.Error)
	}
	if got := len(summarizer.requests); got != 1 {
		t.Fatalf("summarizer calls in a 4-call turn = %d, want 1", got)
	}
	if res := a.processGeneration(ctx, sess, "again", 0, nil, RunOptions{}); res.Error != nil {
		t.Fatalf("processGeneration: %v", res.Error)
	}
	if got := len(summarizer.requests); got != 1 {
		t.Errorf("summarizer calls after the next turn = %d, want still 1 inside the backoff", got)
	}

	// A manual compaction ignores the backoff, and its success clears it.
	summarizer.fail = nil
	if err := a.SummarizeSync(ctx, sess); err != nil {
		t.Fatalf("SummarizeSync: %v", err)
	}
	if a.compactionBackoff.failureCount(sess) != 0 {
		t.Error("a successful manual compaction must reset the backoff")
	}
}
