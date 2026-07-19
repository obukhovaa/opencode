package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
)

func TestNewProviderKimiDefaultBaseURL(t *testing.T) {
	p, err := NewProvider(models.ProviderKimi,
		WithModel(models.KimiModels[models.KimiK3]),
		WithAPIKey("test-key"),
		WithMaxTokens(1024),
	)
	if err != nil {
		t.Fatalf("NewProvider(kimi): %v", err)
	}
	bp, ok := p.(*baseProvider[AnthropicClient])
	if !ok {
		t.Fatalf("kimi provider is not anthropic-client-backed: %T", p)
	}
	if bp.options.baseURL != "https://api.moonshot.ai/anthropic" {
		t.Fatalf("default baseURL = %q", bp.options.baseURL)
	}
}

func TestNewProviderKimiBaseURLOverride(t *testing.T) {
	p, err := NewProvider(models.ProviderKimi,
		WithModel(models.KimiModels[models.KimiK3]),
		WithAPIKey("test-key"),
		WithBaseURL("http://localhost:9999/anthropic"),
	)
	if err != nil {
		t.Fatalf("NewProvider(kimi): %v", err)
	}
	bp := p.(*baseProvider[AnthropicClient])
	if bp.options.baseURL != "http://localhost:9999/anthropic" {
		t.Fatalf("baseURL override not honored: %q", bp.options.baseURL)
	}
}

func TestKimiModelCapabilities(t *testing.T) {
	m, ok := models.SupportedModels[models.KimiK3]
	if !ok {
		t.Fatal("kimi.kimi-k3 not registered in SupportedModels")
	}
	if m.Provider != models.ProviderKimi || m.APIModel != "kimi-k3" {
		t.Fatalf("identity mismatch: %+v", m)
	}
	if m.ContextWindow != 1_000_000 {
		t.Fatalf("context window = %d, want 1M", m.ContextWindow)
	}
	if !m.CanReason || !m.SupportsAdaptiveThinking || !m.SupportsMaximumThinking {
		t.Fatalf("reasoning capabilities wrong: %+v", m)
	}
	if !m.SupportsAttachments {
		t.Fatal("kimi-k3 must support attachments (native vision)")
	}
	if m.SupportsTaskBudget || m.SupportsXHighThinking {
		t.Fatalf("anthropic-only capabilities must stay off: %+v", m)
	}
}

// TestCountTokensDisablesAfter404 covers the kimi-provider requirement that
// an Anthropic-dialect endpoint without count_tokens (404/405) is probed at
// most once: the first failure latches, later calls short-circuit with
// ErrUnsupported and the provider layer falls back to local estimation.
func TestCountTokensDisablesAfter404(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"not_found_error","message":"count_tokens not implemented"}}`))
	}))
	defer srv.Close()

	client, ok := newAnthropicClient(providerClientOptions{
		apiKey:  "test-key",
		baseURL: srv.URL,
		model:   models.KimiModels[models.KimiK3],
	}).(*anthropicClient)
	if !ok {
		t.Fatal("newAnthropicClient did not return *anthropicClient")
	}

	_, err := client.countTokens(context.Background(), nil, nil)
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("first call: want ErrUnsupported, got %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("expected exactly 1 HTTP probe, got %d", got)
	}

	_, err = client.countTokens(context.Background(), nil, nil)
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("second call: want ErrUnsupported, got %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("second call must not hit the endpoint, probes = %d", got)
	}
}

// TestCountTokensSuccessPathUnchanged confirms a working endpoint is used
// normally and never latches the unsupported flag.
func TestCountTokensSuccessPathUnchanged(t *testing.T) {
	var hits atomic.Int32
	var authHeader atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		authHeader.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens": 42}`))
	}))
	defer srv.Close()

	client := newAnthropicClient(providerClientOptions{
		apiKey:  "test-key",
		baseURL: srv.URL,
		model:   models.KimiModels[models.KimiK3],
	}).(*anthropicClient)

	for i := range 2 {
		n, err := client.countTokens(context.Background(), nil, nil)
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		if n != 42 {
			t.Fatalf("call %d: tokens = %d, want 42", i+1, n)
		}
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("expected 2 endpoint hits, got %d", got)
	}
	// Base-URL-configured clients authenticate with a Bearer token — the
	// auth shape Moonshot's Anthropic-compatible endpoint expects.
	if got, _ := authHeader.Load().(string); got != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want Bearer test-key", got)
	}
}

// TestKimiRequestShape pins the request contract Moonshot documents for K3:
// adaptive thinking with output_config.effort=max and temperature 1.
func TestKimiRequestShape(t *testing.T) {
	client := newAnthropicClient(providerClientOptions{
		apiKey:           "test-key",
		baseURL:          "https://api.moonshot.ai/anthropic",
		model:            models.KimiModels[models.KimiK3],
		maxTokens:        1024,
		anthropicOptions: []AnthropicOption{WithAnthropicReasoningEffort("max")},
	}).(*anthropicClient)

	msgs := client.convertMessages([]message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}},
	})
	params := client.preparedMessages(context.Background(), msgs, nil)

	if params.Thinking.OfAdaptive == nil {
		t.Fatalf("expected adaptive thinking config, got %+v", params.Thinking)
	}
	if got := string(params.OutputConfig.Effort); got != "max" {
		t.Fatalf("output_config.effort = %q, want max", got)
	}
	if !params.Temperature.Valid() || params.Temperature.Value != 1 {
		t.Fatalf("temperature = %+v, want 1 (K3's locked sampling)", params.Temperature)
	}
}

// TestCountTokensTransientErrorDoesNotLatch ensures only 404/405 latch the
// unsupported flag — a 500/429 must stay retryable on later iterations.
func TestCountTokensTransientErrorDoesNotLatch(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"blip"}}`))
	}))
	defer srv.Close()

	client := newAnthropicClient(providerClientOptions{
		apiKey:  "test-key",
		baseURL: srv.URL,
		model:   models.KimiModels[models.KimiK3],
	}).(*anthropicClient)

	for i := range 2 {
		_, err := client.countTokens(context.Background(), nil, nil)
		if err == nil {
			t.Fatalf("call %d: expected error", i+1)
		}
		if errors.Is(err, errors.ErrUnsupported) {
			t.Fatalf("call %d: 500 must not be classified unsupported", i+1)
		}
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("500s must keep probing (no latch), probes = %d", got)
	}
}
