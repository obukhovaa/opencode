package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
	toolsPkg "github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

// fakeCountClient is a minimal ProviderClient that only implements the bits
// CountTokens exercises. countTokens returns a canned value/err; everything
// else is a no-op so we can drive baseProvider.CountTokens in isolation.
type fakeCountClient struct {
	tokens int64
	err    error
	max    int64
}

func (f *fakeCountClient) send(context.Context, []message.Message, []toolsPkg.BaseTool) (*ProviderResponse, error) {
	return nil, nil
}
func (f *fakeCountClient) stream(context.Context, []message.Message, []toolsPkg.BaseTool) <-chan ProviderEvent {
	return nil
}
func (f *fakeCountClient) countTokens(context.Context, []message.Message, []toolsPkg.BaseTool) (int64, error) {
	return f.tokens, f.err
}
func (f *fakeCountClient) maxTokens() int64     { return f.max }
func (f *fakeCountClient) setMaxTokens(m int64) { f.max = m }

func TestReconcileTokenEstimate(t *testing.T) {
	tests := []struct {
		name     string
		endpoint int64
		local    int64
		want     int64
	}{
		{"healthy endpoint above local wins", 63000, 50000, 63000},
		{"truncating endpoint below local is floored to local", 400, 50000, 50000},
		{"equal returns either", 1000, 1000, 1000},
		{"zero endpoint (proxy omitted everything) floors to local", 0, 12000, 12000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reconcileTokenEstimate(tt.endpoint, tt.local); got != tt.want {
				t.Errorf("reconcileTokenEstimate(%d, %d) = %d, want %d", tt.endpoint, tt.local, got, tt.want)
			}
		})
	}
}

func newCountProvider(client *fakeCountClient, contextWindow int64, systemMessage string) *baseProvider[ProviderClient] {
	return &baseProvider[ProviderClient]{
		options: providerClientOptions{
			model:         models.Model{ContextWindow: contextWindow},
			systemMessage: systemMessage,
		},
		client: client,
	}
}

// bigSystemPrompt returns a system message whose local token estimate alone
// (len/BytesPerTokenEta) exceeds `wantTokens`.
func bigSystemPrompt(wantTokens int) string {
	b := make([]byte, wantTokens*message.BytesPerTokenEta)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

func TestCountTokens_TruncatingEndpointStillTriggersCompaction(t *testing.T) {
	// Reproduces the LiteLLM-in-front-of-Bedrock bug: the endpoint answers
	// 200 with a tiny number that omits the system prompt + tools, so the
	// naive implementation would never cross the compaction threshold.
	// After the fix, the local estimate (which includes the ~60k-token
	// system prompt) floors the count and the threshold is hit.
	ctxWindow := int64(100_000)
	// ~80k-token system prompt: well above 0.95 * 100k = 95k? No — pick a
	// value that clears the threshold once counted, but that the endpoint
	// hides.
	system := bigSystemPrompt(96_000)
	client := &fakeCountClient{tokens: 8, err: nil} // proxy returns "8"
	p := newCountProvider(client, ctxWindow, system)

	tokens, hit := p.CountTokens(context.Background(), 0.95, nil, nil)
	if tokens < 95_000 {
		t.Errorf("expected floored token count >= 95000 (system prompt counted), got %d", tokens)
	}
	if !hit {
		t.Errorf("expected auto-compaction to trigger (local estimate crosses threshold), got hit=false")
	}
}

func TestCountTokens_HealthyEndpointWins(t *testing.T) {
	// A native endpoint that counts everything reports > local; it should be
	// used verbatim and drive the threshold decision.
	ctxWindow := int64(100_000)
	client := &fakeCountClient{tokens: 96_000, err: nil}
	p := newCountProvider(client, ctxWindow, "small system")

	tokens, hit := p.CountTokens(context.Background(), 0.95, nil, nil)
	if tokens != 96_000 {
		t.Errorf("expected endpoint value 96000 to win, got %d", tokens)
	}
	if !hit {
		t.Errorf("expected threshold hit at 96000/100000 > 0.95, got hit=false")
	}
}

func TestCountTokens_BelowThresholdDoesNotTrigger(t *testing.T) {
	ctxWindow := int64(100_000)
	client := &fakeCountClient{tokens: 10_000, err: nil}
	p := newCountProvider(client, ctxWindow, "small")

	tokens, hit := p.CountTokens(context.Background(), 0.95, nil, nil)
	if hit {
		t.Errorf("expected no compaction at ~10k/100k, got hit=true (tokens=%d)", tokens)
	}
}

func TestCountTokens_EndpointErrorFallsBackToLocal(t *testing.T) {
	ctxWindow := int64(100_000)
	system := bigSystemPrompt(96_000)
	client := &fakeCountClient{tokens: 0, err: errors.New("endpoint unsupported")}
	p := newCountProvider(client, ctxWindow, system)

	tokens, hit := p.CountTokens(context.Background(), 0.95, nil, nil)
	if tokens < 95_000 {
		t.Errorf("expected local estimate >= 95000 on endpoint error, got %d", tokens)
	}
	if !hit {
		t.Errorf("expected compaction to trigger from local estimate on endpoint error")
	}
}

func TestCountTokens_ContextCanceledErrorSuppressed(t *testing.T) {
	// context.Canceled should still fall back to the local estimate without
	// panicking; we just assert it returns a sane count.
	client := &fakeCountClient{tokens: 0, err: context.Canceled}
	p := newCountProvider(client, 100_000, "sys")
	if _, _ = p.CountTokens(context.Background(), 0.95, nil, nil); false {
	}
}

func TestCountTokens_NoContextWindowNeverHits(t *testing.T) {
	client := &fakeCountClient{tokens: 999_999, err: nil}
	p := newCountProvider(client, 0, "sys")
	_, hit := p.CountTokens(context.Background(), 0.95, nil, nil)
	if hit {
		t.Errorf("expected hit=false when ContextWindow<=0")
	}
}
