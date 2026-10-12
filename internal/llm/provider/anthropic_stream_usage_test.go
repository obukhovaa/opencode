package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/tidwall/gjson"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
)

// searchThenToolUse is a streamed response that ran one tool search and then
// called a client tool: two sampling passes.
var searchThenToolUse = []string{
	`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"need weather"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig1"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"tool_search_tool_regex","input":{}}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"pattern\":\"weather\"}"}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"get_weather"}]}}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`,
	`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Oslo\"}"}}`,
	`{"type":"content_block_stop","index":3}`,
}

func searchStream(startUsage, deltaUsage string) []string {
	events := []string{`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":` + startUsage + `}}`}
	events = append(events, searchThenToolUse...)
	if deltaUsage == "" { // cut off before message_delta
		return events
	}
	return append(events,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":`+deltaUsage+`}`,
		`{"type":"message_stop"}`,
	)
}

// streamComplete streams events through the real client, over Anthropic's SSE
// or the Bedrock event stream, and returns the EventComplete response.
func streamComplete(t *testing.T, bedrock bool, events []string) *ProviderResponse {
	t.Helper()
	loadConfigIn(t, t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bedrock {
			w.Header().Set("Content-Type", bedrockEventStreamContentType)
			for _, e := range events {
				_, _ = w.Write(encodeBedrockChunk(t, e))
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", gjson.Get(e, "type").String(), e)
		}
	}))
	t.Cleanup(srv.Close)
	var client ProviderClient
	if bedrock {
		client = newBedrockClient(providerClientOptions{apiKey: "test-key", baseURL: srv.URL + "/bedrock",
			model: models.SupportedModels[models.BedrockEUSonnet5], maxTokens: 1024})
	} else {
		client = newAnthropicClient(providerClientOptions{apiKey: "test-key", baseURL: srv.URL,
			model: models.AnthropicModels[models.Claude55Sonnet], maxTokens: 1024})
	}
	prompt := []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "weather in Oslo?"}}}}
	var final *ProviderResponse
	for ev := range client.stream(context.Background(), prompt, nil) {
		switch ev.Type {
		case EventComplete:
			final = ev.Response
		case EventError:
			t.Fatalf("stream error: %v", ev.Error)
		}
	}
	if final == nil {
		t.Fatal("no EventComplete")
	}
	return final
}

// GENAI-450: the streamed usage of a tool-search response names its last
// pass only when message_delta reported the cumulative input counts. A
// stream cut off before message_delta, or a message_delta carrying output
// only, leaves message_start's counts: one pass's prompt, not to be halved.
func TestAnthropicStreamUsage_LastPass(t *testing.T) {
	const start = `{"input_tokens":170,"cache_creation_input_tokens":9612,"cache_read_input_tokens":0,"output_tokens":1}`
	cases := []struct {
		name  string
		delta string
		want  *TokenUsage
	}{
		{
			name:  "cumulative message_delta: halved to the last pass",
			delta: `{"input_tokens":355,"cache_creation_input_tokens":9612,"cache_read_input_tokens":9612,"output_tokens":108}`,
			want:  &TokenUsage{InputTokens: 177, CacheCreationTokens: 4806, CacheReadTokens: 4806, OutputTokens: 108},
		},
		{
			name:  "per-pass iterations on message_delta",
			delta: `{"input_tokens":355,"cache_creation_input_tokens":9612,"cache_read_input_tokens":9612,"output_tokens":108,"iterations":[{"type":"message","input_tokens":170,"cache_creation_input_tokens":9612,"cache_read_input_tokens":0,"output_tokens":40},{"type":"message","input_tokens":185,"cache_creation_input_tokens":0,"cache_read_input_tokens":9612,"output_tokens":68}]}`,
			want:  &TokenUsage{InputTokens: 185, CacheReadTokens: 9612, OutputTokens: 68},
		},
		{name: "message_delta with the output only", delta: `{"output_tokens":108}`},
		{name: "stream cut off before message_delta"},
	}
	for _, bedrock := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("bedrock=%v/%s", bedrock, tc.name), func(t *testing.T) {
				got := streamComplete(t, bedrock, searchStream(start, tc.delta)).Usage.LastPass
				switch {
				case tc.want == nil && got != nil:
					t.Errorf("LastPass = %+v, want nil: the counts are one pass's", *got)
				case tc.want != nil && (got == nil || *got != *tc.want):
					t.Errorf("LastPass = %v, want %+v", got, *tc.want)
				}
			})
		}
	}
}

func TestSamplingPasses(t *testing.T) {
	cases := []struct {
		name  string
		types []string
		want  int64
	}{
		{"single pass", []string{"text"}, 1},
		{"empty", nil, 1},
		{"one search then a tool call", []string{"server_tool_use", "tool_search_tool_result", "tool_use"}, 2},
		{"two sequential web searches", []string{"text", "server_tool_use", "web_search_tool_result", "text", "server_tool_use", "web_search_tool_result", "text"}, 3},
		{"parallel searches grouped in one pass", []string{"server_tool_use", "server_tool_use", "tool_search_tool_result", "tool_search_tool_result", "tool_use"}, 2},
		{"a turn paused on a result", []string{"server_tool_use", "tool_search_tool_result"}, 1},
		{"redacted thinking around a search", []string{"redacted_thinking", "server_tool_use", "tool_search_tool_result", "redacted_thinking", "tool_use"}, 2},
		{"code execution", []string{"server_tool_use", "bash_code_execution_tool_result", "server_tool_use", "text_editor_code_execution_tool_result", "text"}, 3},
	}
	for _, tc := range cases {
		blocks := make([]anthropic.ContentBlockUnion, len(tc.types))
		for i, ty := range tc.types {
			blocks[i].Type = ty
		}
		if got := samplingPasses(blocks); got != tc.want {
			t.Errorf("%s: passes = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// AttachmentTokens prices an attachment as the converters send it.
func TestAttachmentTokens(t *testing.T) {
	text := message.BinaryContent{Path: "app.log", MIMEType: "Text/Plain; charset=utf-8", Data: []byte("0123456789abcdef")}
	audio := message.BinaryContent{Path: "voice.ogg", MIMEType: "audio/ogg", Data: make([]byte, 19_000_000)}
	cases := []struct {
		name string
		bc   message.BinaryContent
		want int64
	}{
		{"an image is one image", message.BinaryContent{MIMEType: "image/png", Data: make([]byte, 5_000_000)}, ImageTokenEstimate},
		{"a PDF by its size", message.BinaryContent{MIMEType: "application/pdf", Data: make([]byte, 400_000)}, 4_000},
		{"a small PDF at the floor", message.BinaryContent{MIMEType: "application/pdf", Data: make([]byte, 1_000)}, ImageTokenEstimate},
		{"text as the text it is inlined as", text, int64(len(inlineTextAttachment(text)) / message.BytesPerTokenEta)},
		{"audio as the note that replaces it", audio, int64(len(unsupportedAttachmentNote(audio)) / message.BytesPerTokenEta)},
	}
	for _, tc := range cases {
		if got := AttachmentTokens(tc.bc); got != tc.want {
			t.Errorf("%s: AttachmentTokens = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// Only the Anthropic client sends a view_image result as an image.
func TestSendsToolResultImages(t *testing.T) {
	cases := []struct {
		name     string
		provider models.ModelProvider
		model    models.ModelID
		want     bool
	}{
		{"anthropic", models.ProviderAnthropic, models.Claude55Sonnet, true},
		{"bedrock claude", models.ProviderBedrock, models.BedrockEUSonnet5, true},
		{"openai", models.ProviderOpenAI, models.GPT5, false},
	}
	for _, tc := range cases {
		p, err := NewProvider(tc.provider, WithAPIKey("k"), WithModel(models.SupportedModels[tc.model]), WithMaxTokens(1024))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := p.(interface{ SendsToolResultImages() bool }).SendsToolResultImages()
		if got != tc.want {
			t.Errorf("%s: SendsToolResultImages = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The Bedrock wrapper builds no request: its child does, from its own options,
// so max_tokens has to reach the child.
func TestBedrockMaxTokensReachTheChild(t *testing.T) {
	c := newBedrockClient(providerClientOptions{apiKey: "k", model: models.SupportedModels[models.BedrockEUSonnet5], maxTokens: 64_000}).(*bedrockClient)
	c.setMaxTokens(32_000)
	if got := c.childProvider.maxTokens(); got != 32_000 {
		t.Errorf("child max_tokens = %d, want 32000", got)
	}
	if got := c.maxTokens(); got != 32_000 {
		t.Errorf("max_tokens = %d, want 32000", got)
	}
}
