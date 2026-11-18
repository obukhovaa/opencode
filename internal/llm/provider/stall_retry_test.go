package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"google.golang.org/genai"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
)

// A stalled HTTP stream belongs to the agent's resend budget, including when
// the provider has not emitted any output yet. Replaying here multiplies it.
func TestProviderStreamStallBubblesWithoutRetry(t *testing.T) {
	for _, provider := range []string{"anthropic", "openai", "gemini"} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/partial=%t", provider, partial), func(t *testing.T) {
				loadConfigIn(t, t.TempDir())
				// The environment timeout is resolved at package initialization.
				// Override its cached value for this serial test.
				previousTimeout := streamInactivityTimeout
				streamInactivityTimeout = 50 * time.Millisecond
				t.Cleanup(func() { streamInactivityTimeout = previousTimeout })
				var requests atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					if partial {
						switch provider {
						case "anthropic":
							fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
							fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
							fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n")
						case "openai":
							fmt.Fprint(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
						case "gemini":
							fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial\"}]}}]}\n\n")
						}
					}
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				}))
				defer srv.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				opts := providerClientOptions{apiKey: "test", baseURL: srv.URL, maxTokens: 128,
					model: models.Model{ID: "test", APIModel: "test"}}
				var client ProviderClient
				switch provider {
				case "anthropic":
					client = newAnthropicClient(opts)
				case "openai":
					client = newOpenAIClient(opts)
				case "gemini":
					sdk, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: "test", Backend: genai.BackendGeminiAPI,
						HTTPOptions: genai.HTTPOptions{BaseURL: srv.URL}})
					if err != nil {
						t.Fatal(err)
					}
					client = &geminiClient{providerOptions: opts, client: sdk}
				}
				prompt := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}}
				var errorCount int
				var content strings.Builder
				for ev := range client.stream(ctx, prompt, nil) {
					switch ev.Type {
					case EventContentDelta:
						content.WriteString(ev.Content)
					case EventError:
						errorCount++
						if !errors.Is(ev.Error, ErrStreamStalled) {
							t.Errorf("error = %v, want ErrStreamStalled", ev.Error)
						}
					case EventComplete:
						t.Error("stalled stream must not complete successfully")
					}
				}
				if errorCount != 1 {
					t.Errorf("error events = %d, want 1", errorCount)
				}
				if got := requests.Load(); got != 1 {
					t.Errorf("HTTP requests = %d, want 1", got)
				}
				if partial && content.String() != "partial" {
					t.Errorf("content = %q, want partial", content.String())
				}
			})
		}
	}
}

// Keep the existing HTTP retry gates independent of the smaller stall budget.
// attempts is one-based: eight retries are permitted, the ninth failure stops.
func TestProviderHTTPRetryBudgetUnchanged(t *testing.T) {
	openaiErr := func(status int) error {
		return &openai.Error{StatusCode: status, Response: &http.Response{Header: http.Header{}}}
	}
	cases := []struct {
		name string
		gate func(int, error) (bool, int64, error)
		err  error
	}{
		{"anthropic/429", (&anthropicClient{}).shouldRetry, newAPIErrWithStatus(429, nil)},
		{"anthropic/503", (&anthropicClient{}).shouldRetry, newAPIErrWithStatus(503, nil)},
		{"anthropic/529", (&anthropicClient{}).shouldRetry, newAPIErrWithStatus(529, nil)},
		{"openai/429", (&openaiClient{}).shouldRetry, openaiErr(429)},
		{"openai/500", (&openaiClient{}).shouldRetry, openaiErr(500)},
		{"gemini/429", (&geminiClient{}).shouldRetry, genai.APIError{Code: 429, Message: "quota exceeded", Status: "RESOURCE_EXHAUSTED"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for attempt := 1; attempt <= 8; attempt++ {
				retry, delay, err := tc.gate(attempt, tc.err)
				if !retry || delay <= 0 || err != nil {
					t.Fatalf("attempt %d: retry=%v delay=%d err=%v", attempt, retry, delay, err)
				}
			}
			retry, _, err := tc.gate(9, tc.err)
			if retry || err == nil || !strings.Contains(err.Error(), "maximum retry attempts") {
				t.Fatalf("ninth failure: retry=%v err=%v", retry, err)
			}
		})
	}
	// Gemini's existing policy recognizes quota messages, not generic 5xx errors.
	err := genai.APIError{Code: 503, Message: "unavailable", Status: "UNAVAILABLE"}
	retry, _, got := (&geminiClient{}).shouldRetry(1, err)
	if retry || got == nil {
		t.Fatalf("Gemini 503: retry=%v err=%v", retry, got)
	}
}
