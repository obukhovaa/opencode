// Command compaction-e2e is a black-box driver that reproduces the
// count_tokens auto-compaction bug end-to-end and verifies the fix.
//
// Background
//
//	Some proxies (observed: LiteLLM in front of AWS Bedrock) answer HTTP 200
//	from POST /v1/messages/count_tokens but silently omit the system prompt
//	AND the tool schemas from the returned count — reporting a tiny number
//	(e.g. 8) for a request whose real input is tens of thousands of tokens.
//	The agent loop trusts this number to decide when to auto-compact, so the
//	truncation made compaction fire late or never, risking a hard
//	context-overflow error mid-run.
//
// What it exercises
//
//	It stands up a real Anthropic provider (via provider.NewProvider) pointed
//	at an in-process httptest server that mimics the truncating proxy: its
//	/v1/messages/count_tokens handler ALWAYS returns {"input_tokens": 8}
//	regardless of the (large) system prompt + tool schemas in the request.
//	The driver then calls provider.CountTokens with a system prompt big
//	enough to cross the compaction threshold and asserts:
//
//	 1. the endpoint really was hit (truncating handler observed the request),
//	 2. the reconciled token count reflects the true (local) footprint, not 8,
//	 3. auto-compaction is reported as required (hit == true).
//
//	It also asserts the healthy-endpoint path is unaffected: when the server
//	returns a correct large count, that value is used verbatim.
//
// Usage: invoked from scripts/test/compaction.sh.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/provider"
)

type result struct {
	// truncating-proxy scenario
	CountTokensEndpointHit   bool  `json:"count_tokens_endpoint_hit"`
	TruncatedEndpointValue   int64 `json:"truncated_endpoint_value"`
	ReconciledTokens         int64 `json:"reconciled_tokens"`
	CompactionTriggered      bool  `json:"compaction_triggered"`
	SystemPromptTokensApprox int64 `json:"system_prompt_tokens_approx"`

	// healthy-proxy scenario
	HealthyEndpointValue int64 `json:"healthy_endpoint_value"`
	HealthyReported      int64 `json:"healthy_reported"`
	HealthyTriggered     bool  `json:"healthy_triggered"`

	Errors []string `json:"errors"`
}

// bigSystem returns a system prompt whose local heuristic estimate
// (len / 4 bytes-per-token) is approximately wantTokens.
func bigSystem(wantTokens int) string {
	return strings.Repeat("x", wantTokens*4)
}

func newProviderAgainst(baseURL, system string, ctxWindow int64) (provider.Provider, error) {
	return provider.NewProvider(
		models.ProviderAnthropic,
		provider.WithAPIKey("test-key"),
		provider.WithBaseURL(baseURL),
		provider.WithModel(models.Model{
			ID:            "test-model",
			APIModel:      "claude-test",
			Provider:      models.ProviderAnthropic,
			ContextWindow: ctxWindow,
		}),
		provider.WithSystemMessage(system),
		provider.WithMaxTokens(4096),
	)
}

func main() {
	res := result{}
	ctxWindow := int64(100_000)

	// ── Scenario 1: truncating proxy ────────────────────────────────
	// System prompt sized to ~96k tokens so the true footprint clears the
	// 0.95 * 100k = 95k threshold, but the endpoint hides it behind "8".
	system := bigSystem(96_000)
	res.SystemPromptTokensApprox = int64(len(system) / 4)

	var endpointHit atomic.Bool
	truncSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/count_tokens") {
			endpointHit.Store(true)
			w.Header().Set("Content-Type", "application/json")
			// The bug: report a tiny count that omits system + tools.
			_ = json.NewEncoder(w).Encode(map[string]any{"input_tokens": 8})
			return
		}
		// Any other call is unexpected in this test.
		w.WriteHeader(http.StatusNotImplemented)
	}))
	defer truncSrv.Close()

	p, err := newProviderAgainst(truncSrv.URL, system, ctxWindow)
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("build truncating provider: %v", err))
		emit(res)
		return
	}
	tokens, hit := p.CountTokens(context.Background(), 0.95, nil, nil)
	res.CountTokensEndpointHit = endpointHit.Load()
	res.TruncatedEndpointValue = 8
	res.ReconciledTokens = tokens
	res.CompactionTriggered = hit

	// ── Scenario 2: healthy proxy ───────────────────────────────────
	// A correct endpoint reports the full count; it must be used verbatim.
	healthyVal := int64(96_000)
	healthySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/count_tokens") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"input_tokens": healthyVal})
			return
		}
		w.WriteHeader(http.StatusNotImplemented)
	}))
	defer healthySrv.Close()

	// Small system prompt so the local estimate stays below the endpoint
	// value — proving the endpoint (not the local floor) drives the result.
	hp, err := newProviderAgainst(healthySrv.URL, "small system prompt", ctxWindow)
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("build healthy provider: %v", err))
		emit(res)
		return
	}
	htokens, hhit := hp.CountTokens(context.Background(), 0.95, nil, nil)
	res.HealthyEndpointValue = healthyVal
	res.HealthyReported = htokens
	res.HealthyTriggered = hhit

	emit(res)
}

func emit(r result) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(r)
}
