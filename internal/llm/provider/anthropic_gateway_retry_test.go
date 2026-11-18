package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/tidwall/gjson"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
)

// nginx502 is the page an nginx ingress answers with when it cannot
// reach its upstream: what LiteLLM's ingress returned for one model call
// of a daemon run on 2026-11-18, which then failed without a retry.
const nginx502 = "<html>\r\n<head><title>502 Bad Gateway</title></head>\r\n<body>\r\n" +
	"<center><h1>502 Bad Gateway</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n"

const anthropicAPIError = `{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`

// newGatewayErr builds the *anthropic.Error the SDK returns for a non-2xx
// answer with the given body.
func newGatewayErr(status int, body string) error {
	req, _ := http.NewRequest(http.MethodPost, "https://gw.example/bedrock/v1/messages", nil)
	e := &anthropic.Error{
		StatusCode: status,
		Request:    req,
		Response:   &http.Response{StatusCode: status, Header: http.Header{}},
	}
	_ = e.UnmarshalJSON([]byte(body))
	return e
}

func TestIsGatewayBlip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nginx 502 page", newGatewayErr(502, nginx502), true},
		{"504 page is not retried: the upstream may still be generating", newGatewayErr(504, "<html><body>504 Gateway Time-out</body></html>"), false},
		{"502 with no body", newGatewayErr(502, ""), true},
		{"502 plain text", newGatewayErr(502, "upstream connect error or disconnect/reset before headers"), true},
		{"wrapped by the agent loop", fmt.Errorf("failed to process events: %w", newGatewayErr(502, nginx502)), true},
		{"Anthropic JSON 502 is the provider's", newGatewayErr(502, anthropicAPIError), false},
		{"Bedrock JSON 504 is the provider's", newGatewayErr(504, `{"message":"Model timed out"}`), false},
		{"LiteLLM JSON 502 is the provider's", newGatewayErr(502, `{"error":{"message":"upstream failed","code":"502"}}`), false},
		{"HTML 500 is not a gateway status", newGatewayErr(500, nginx502), false},
		{"HTML 503 belongs to shouldRetry", newGatewayErr(503, nginx502), false},
		{"not an API error", errors.New("502 Bad Gateway"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isGatewayBlip(tt.err); got != tt.want {
				t.Errorf("isGatewayBlip() = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestGatewayRetryDelay(t *testing.T) {
	t.Parallel()
	blip := newGatewayErr(502, nginx502)
	tests := []struct {
		retries   int
		err       error
		wantDelay time.Duration
		wantRetry bool
	}{
		{0, blip, gatewayRetryBaseDelay, true},
		{1, blip, 2 * gatewayRetryBaseDelay, true},
		{maxGatewayRetries, blip, 0, false},
		{0, newGatewayErr(502, anthropicAPIError), 0, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("retries=%d", tt.retries), func(t *testing.T) {
			t.Parallel()
			delay, retry := gatewayRetryDelay(tt.retries, tt.err)
			if delay != tt.wantDelay || retry != tt.wantRetry {
				t.Errorf("gatewayRetryDelay() = (%v, %v); want (%v, %v)", delay, retry, tt.wantDelay, tt.wantRetry)
			}
		})
	}
}

// TestGatewayStatusesStayOutOfShouldRetry pins the split: 502/504 are not
// added to retryableHTTPStatuses, so shouldRetry still refuses them and
// countTokensShapeRejection still classifies them as shape rejections.
func TestGatewayStatusesStayOutOfShouldRetry(t *testing.T) {
	t.Parallel()
	for _, status := range []int{502, 504} {
		err := newGatewayErr(status, nginx502)
		if retry, _, _ := (&anthropicClient{}).shouldRetry(1, err); retry {
			t.Errorf("shouldRetry retries %d; the gateway retry is separate", status)
		}
		if got, rejected := countTokensShapeRejection(err); got != status || !rejected {
			t.Errorf("countTokensShapeRejection(%d) = (%d, %v); want (%d, true)", status, got, rejected, status)
		}
	}
}

func TestGatewayRetryExhaustedError(t *testing.T) {
	t.Parallel()
	blip := newGatewayErr(502, nginx502)
	if got := gatewayRetryExhaustedError(0, blip); got != blip {
		t.Errorf("no retries: got %v; want the original error", got)
	}
	jsonErr := newGatewayErr(502, anthropicAPIError)
	if got := gatewayRetryExhaustedError(2, jsonErr); got != jsonErr {
		t.Errorf("not a blip: got %v; want the original error", got)
	}
	got := gatewayRetryExhaustedError(2, blip)
	var apierr *anthropic.Error
	if !errors.As(got, &apierr) || apierr.StatusCode != 502 {
		t.Errorf("wrapped error lost the *anthropic.Error: %v", got)
	}
	if !strings.HasPrefix(got.Error(), gatewayRetriesExhausted+" after 2 retries: ") {
		t.Errorf("wrapped error = %q", got)
	}
}

var okStreamEvents = []string{
	`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`,
	`{"type":"message_stop"}`,
}

const okMessageJSON = `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

// gatewayServer fails the first `failures` requests with status and body,
// then answers normally, streaming or not as the request asked.
func gatewayServer(t *testing.T, failures int32, status int, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) <= failures {
			if strings.HasPrefix(body, "{") {
				w.Header().Set("Content-Type", "application/json")
			} else {
				w.Header().Set("Content-Type", "text/html")
			}
			w.WriteHeader(status)
			fmt.Fprint(w, body)
			return
		}
		reqBody, _ := io.ReadAll(r.Body)
		if gjson.GetBytes(reqBody, "stream").Bool() {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, e := range okStreamEvents {
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", gjson.Get(e, "type").String(), e)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, okMessageJSON)
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

// TestAnthropicStream_GatewayBlipRetry drives the real client against a
// server that answers like a flapping ingress. Not parallel: it shrinks
// gatewayRetryBaseDelay.
func TestAnthropicStream_GatewayBlipRetry(t *testing.T) {
	prev := gatewayRetryBaseDelay
	gatewayRetryBaseDelay = time.Millisecond
	t.Cleanup(func() { gatewayRetryBaseDelay = prev })

	tests := []struct {
		name         string
		failures     int32
		status       int
		body         string
		wantRequests int32
		wantOK       bool
		wantExhaust  bool
	}{
		{name: "one HTML 502 is retried", failures: 1, status: 502, body: nginx502, wantRequests: 2, wantOK: true},
		{name: "two HTML 502s are retried", failures: 2, status: 502, body: nginx502, wantRequests: 3, wantOK: true},
		{name: "an HTML 504 is not retried", failures: 100, status: 504, body: "<html>504</html>", wantRequests: 1},
		{name: "a persisting blip surfaces after two retries", failures: 100, status: 502, body: nginx502, wantRequests: 3, wantExhaust: true},
		{name: "a JSON 502 is not retried", failures: 100, status: 502, body: anthropicAPIError, wantRequests: 1},
		{name: "a JSON 500 is not retried", failures: 100, status: 500, body: anthropicAPIError, wantRequests: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loadConfigIn(t, t.TempDir())
			srv, requests := gatewayServer(t, tt.failures, tt.status, tt.body)
			client := newAnthropicClient(providerClientOptions{apiKey: "test-key", baseURL: srv.URL,
				model: models.AnthropicModels[models.Claude55Sonnet], maxTokens: 128})
			prompt := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			var complete *ProviderResponse
			var streamErr error
			var starts, deltas, completes int
			for ev := range client.stream(ctx, prompt, nil) {
				switch ev.Type {
				case EventContentStart:
					starts++
				case EventContentDelta:
					deltas++
				case EventComplete:
					completes++
					complete = ev.Response
				case EventError:
					streamErr = ev.Error
				}
			}
			if got := requests.Load(); got != tt.wantRequests {
				t.Errorf("requests = %d; want %d", got, tt.wantRequests)
			}
			if tt.wantOK {
				if streamErr != nil || complete == nil || complete.Content != "ok" {
					t.Fatalf("want a completed response after the retry; got complete=%v err=%v", complete, streamErr)
				}
				// The failed attempts emitted nothing, so the consumer sees
				// the answer exactly once: no replayed block, no second
				// completion appended to the assistant message.
				if starts != 1 || deltas != 1 || completes != 1 {
					t.Errorf("events: %d content starts, %d deltas, %d completes; want 1 each", starts, deltas, completes)
				}
				return
			}
			if starts+deltas+completes != 0 {
				t.Errorf("a failed call emitted output: %d starts, %d deltas, %d completes", starts, deltas, completes)
			}
			if streamErr == nil {
				t.Fatal("want the error surfaced; got none")
			}
			var apierr *anthropic.Error
			if !errors.As(streamErr, &apierr) || apierr.StatusCode != tt.status {
				t.Errorf("surfaced error %v does not carry the HTTP %d", streamErr, tt.status)
			}
			exhausted := strings.Contains(streamErr.Error(), gatewayRetriesExhausted)
			if exhausted != tt.wantExhaust {
				t.Errorf("exhaustion marker present = %v; want %v (err %v)", exhausted, tt.wantExhaust, streamErr)
			}
			if IsRetriedTransientError(streamErr) != tt.wantExhaust {
				t.Errorf("IsRetriedTransientError = %v; want %v", !tt.wantExhaust, tt.wantExhaust)
			}
		})
	}
}

// TestAnthropicSend_GatewayBlipRetry: the non-streaming call (summaries,
// titles) gets the same bounded retry.
func TestAnthropicSend_GatewayBlipRetry(t *testing.T) {
	prev := gatewayRetryBaseDelay
	gatewayRetryBaseDelay = time.Millisecond
	t.Cleanup(func() { gatewayRetryBaseDelay = prev })

	loadConfigIn(t, t.TempDir())
	srv, requests := gatewayServer(t, 1, 502, nginx502)
	client := newAnthropicClient(providerClientOptions{apiKey: "test-key", baseURL: srv.URL,
		model: models.AnthropicModels[models.Claude55Sonnet], maxTokens: 128})
	prompt := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}
	resp, err := client.send(context.Background(), prompt, nil)
	if err != nil || resp == nil || resp.Content != "ok" {
		t.Fatalf("send after one 502 = (%v, %v); want the retried answer", resp, err)
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("requests = %d; want 2", got)
	}
}

// TestGatewayRetry_CancelDuringBackoff: an /abort (or shutdown) that lands
// while the client waits to retry a gateway blip ends the call at once with
// the cancellation, instead of sleeping out the backoff and replaying the
// request. Not parallel: it changes gatewayRetryBaseDelay.
func TestGatewayRetry_CancelDuringBackoff(t *testing.T) {
	prev := gatewayRetryBaseDelay
	gatewayRetryBaseDelay = time.Minute
	t.Cleanup(func() { gatewayRetryBaseDelay = prev })

	prompt := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}
	for _, mode := range []string{"stream", "send"} {
		t.Run(mode, func(t *testing.T) {
			loadConfigIn(t, t.TempDir())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprint(w, nginx502)
				// Cancel once the blip is answered: the client is then in,
				// or about to enter, its one-minute backoff.
				go func() { time.Sleep(20 * time.Millisecond); cancel() }()
			}))
			t.Cleanup(srv.Close)
			client := newAnthropicClient(providerClientOptions{apiKey: "test-key", baseURL: srv.URL,
				model: models.AnthropicModels[models.Claude55Sonnet], maxTokens: 128})

			start := time.Now()
			var err error
			if mode == "stream" {
				for ev := range client.stream(ctx, prompt, nil) {
					if ev.Type == EventError {
						err = ev.Error
					}
				}
			} else {
				_, err = client.send(ctx, prompt, nil)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("call returned after %v; want it to stop on cancel, not wait out the backoff", elapsed)
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v; want context.Canceled", err)
			}
			if got := requests.Load(); got != 1 {
				t.Errorf("requests = %d; want 1 (no replay after cancel)", got)
			}
		})
	}
}
