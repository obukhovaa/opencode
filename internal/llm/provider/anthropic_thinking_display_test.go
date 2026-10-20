package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
)

// Opus 4.7 and later and the Claude 5 family return each thinking block
// signed but empty unless the request asks for display "summarized" (probed
// on the LiteLLM Bedrock route). Moonshot does not document the field, so a
// Kimi request keeps the bare adaptive config.
func TestPreparedMessages_ThinkingDisplay(t *testing.T) {
	cases := []struct {
		name  string
		model models.Model
		want  string
	}{
		{"anthropic sonnet 5.5", models.AnthropicModels[models.Claude55Sonnet], "summarized"},
		{"anthropic opus 4.6", models.AnthropicModels[models.Claude46Opus], "summarized"},
		{"bedrock eu sonnet 5.5", models.SupportedModels[models.BedrockEUSonnet55], "summarized"},
		{"vertex sonnet 5.5", models.SupportedModels[models.VertexAISonnet55], "summarized"},
		{"kimi k3", models.KimiModels[models.KimiK3], ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newAnthropicClient(providerClientOptions{apiKey: "test-key", model: tc.model, maxTokens: 1024}).(*anthropicClient)
			msgs := c.convertMessagesUnfiltered([]message.Message{
				{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}},
			})
			body, err := json.Marshal(c.preparedMessages(context.Background(), msgs, nil))
			if err != nil {
				t.Fatalf("marshal params: %v", err)
			}
			thinking := gjson.GetBytes(body, "thinking")
			if got := thinking.Get("type").String(); got != "adaptive" {
				t.Fatalf("thinking.type = %q, want adaptive (thinking = %s)", got, thinking.Raw)
			}
			if got := thinking.Get("display").String(); got != tc.want {
				t.Errorf("thinking.display = %q, want %q (thinking = %s)", got, tc.want, thinking.Raw)
			}
		})
	}
}

// The wire body Bedrock receives: the display rides inside thinking, and a
// comma-joined anthropic-beta header becomes one array element per beta.
func TestBedrockRequestBody_ThinkingDisplayAndBetas(t *testing.T) {
	loadConfigIn(t, t.TempDir())
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", bedrockEventStreamContentType)
		for _, e := range []string{
			`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":5,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		} {
			_, _ = w.Write(encodeBedrockChunk(t, e))
		}
	}))
	t.Cleanup(srv.Close)

	client := newBedrockClient(providerClientOptions{
		apiKey:    "test-key",
		baseURL:   srv.URL + "/bedrock",
		model:     models.SupportedModels[models.BedrockEUSonnet55],
		maxTokens: 1024,
		headers:   map[string]string{"anthropic-beta": "context-1m-2025-08-07, interleaved-thinking-2025-05-14"},
	})
	prompt := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}
	for ev := range client.stream(context.Background(), prompt, nil) {
		if ev.Type == EventError {
			t.Fatalf("stream error: %v", ev.Error)
		}
	}

	if got := gjson.GetBytes(body, "thinking.display").String(); got != "summarized" {
		t.Errorf("thinking.display = %q, want summarized (body thinking = %s)", got, gjson.GetBytes(body, "thinking").Raw)
	}
	var betas []string
	for _, b := range gjson.GetBytes(body, "anthropic_beta").Array() {
		betas = append(betas, b.String())
	}
	want := []string{"context-1m-2025-08-07", "interleaved-thinking-2025-05-14"}
	if len(betas) != len(want) || betas[0] != want[0] || betas[1] != want[1] {
		t.Errorf("anthropic_beta = %q, want %q", betas, want)
	}
}

// Summarized thinking streams in tiny deltas and each EventThinkingDelta is a
// message-store write, so the stream coalesces them: the block's first delta
// at once, the rest in thinkingFlushBytes batches, the tail at block stop,
// with the text intact end to end.
func TestAnthropicStream_CoalescesThinkingDeltas(t *testing.T) {
	loadConfigIn(t, t.TempDir())
	const chunk = "abcdefghij"
	events := []string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":5,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
	}
	for i := 0; i < 60; i++ {
		events = append(events, `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"`+chunk+`"}}`)
	}
	events = append(events,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig1"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ok"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":200,"output_tokens_details":{"thinking_tokens":150}}}`,
		`{"type":"message_stop"}`,
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", gjson.Get(e, "type").String(), e)
		}
	}))
	t.Cleanup(srv.Close)

	client := newAnthropicClient(providerClientOptions{apiKey: "test-key", baseURL: srv.URL,
		model: models.AnthropicModels[models.Claude55Sonnet], maxTokens: 1024})
	prompt := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}}
	var deltas []string
	var final *ProviderResponse
	for ev := range client.stream(context.Background(), prompt, nil) {
		switch ev.Type {
		case EventThinkingDelta:
			deltas = append(deltas, ev.Thinking)
		case EventComplete:
			final = ev.Response
		case EventError:
			t.Fatalf("stream error: %v", ev.Error)
		}
	}

	full := strings.Repeat(chunk, 60)
	// 10 at once, then 260 + 260 at the threshold, then the 70 left at block stop.
	wantLens := []int{10, 260, 260, 70}
	if len(deltas) != len(wantLens) {
		t.Fatalf("got %d thinking deltas, want %d (lens %v)", len(deltas), len(wantLens), lens(deltas))
	}
	for i, d := range deltas {
		if len(d) != wantLens[i] {
			t.Errorf("delta %d has %d bytes, want %d", i, len(d), wantLens[i])
		}
	}
	if got := strings.Join(deltas, ""); got != full {
		t.Errorf("streamed thinking = %q, want the %d-byte original", got, len(full))
	}
	if final == nil || len(final.Reasoning) != 1 || final.Reasoning[0].Thinking != full || final.Reasoning[0].Signature != "sig1" {
		t.Fatalf("final reasoning = %+v, want one signed block with the full text", final)
	}
}

func lens(ss []string) []int {
	out := make([]int, len(ss))
	for i, s := range ss {
		out[i] = len(s)
	}
	return out
}
