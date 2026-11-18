package agent

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/message"
)

func TestTrackUsageCountsCachedReadsAsInput(t *testing.T) {
	ctx := context.Background()
	a := &agent{sessions: &memSessions{}}
	model := models.Model{ContextWindow: 10000}
	usage := provider.TokenUsage{InputTokens: 100, CacheCreationTokens: 200, CacheReadTokens: 700, OutputTokens: 50}
	for i := 0; i < 2; i++ {
		if err := a.TrackUsage(ctx, "s", model, usage); err != nil {
			t.Fatal(err)
		}
	}
	sess, err := a.sessions.Get(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if sess.PromptTokens != 1000 || sess.CompletionTokens != 50 || sess.TotalPromptTokens != 2000 || sess.TotalCompletionTokens != 100 {
		t.Fatalf("cached input misclassified or totals replaced: %+v", sess)
	}
	usage.LastPass = &provider.TokenUsage{InputTokens: 20, CacheCreationTokens: 30, CacheReadTokens: 400, OutputTokens: 10}
	if err := a.TrackUsage(ctx, "s", model, usage); err != nil {
		t.Fatal(err)
	}
	sess, _ = a.sessions.Get(ctx, "s")
	if sess.PromptTokens != 450 || sess.CompletionTokens != 10 || sess.TotalPromptTokens != 3000 || sess.TotalCompletionTokens != 150 {
		t.Fatalf("last context confused with multi-pass billing: %+v", sess)
	}
}

func TestSessionContextUsageMatchesCompactionWithoutProviderRequest(t *testing.T) {
	ctx := context.Background()
	p := &compactionProvider{name: "main", window: 10000, log: &[]string{}}
	a := newLoopAgentWithTools(t, p, nil)
	if got, err := a.ContextUsage(ctx, "s"); err != nil || got.Reported {
		t.Fatalf("fresh context=%+v error=%v", got, err)
	}
	seedHistory(t, a, "s", textMsg(message.User, "question"), textMsg(message.Assistant, "answer"))
	if err := a.TrackUsage(ctx, "s", p.Model(), provider.TokenUsage{InputTokens: 100, CacheReadTokens: 900, OutputTokens: 50}); err != nil {
		t.Fatal(err)
	}
	got, err := a.ContextUsage(ctx, "s")
	if err != nil || got.Tokens != 1050 || !got.Reported || got.Estimated {
		t.Fatalf("reported context=%+v error=%v", got, err)
	}
	seedHistory(t, a, "s", textMsg(message.User, "new input after the model call"))
	got, err = a.ContextUsage(ctx, "s")
	if err != nil || got.Tokens <= 1050 || !got.Estimated {
		t.Fatalf("unsent delta missing: %+v error=%v", got, err)
	}
	history, err := a.messages.List(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	count, _ := a.countContextTokens(ctx, "s", 0.95, history, nil)
	if got.Tokens != count || p.CountCalls() != 0 {
		t.Fatalf("display=%d compaction=%d provider calls=%d", got.Tokens, count, p.CountCalls())
	}
}
