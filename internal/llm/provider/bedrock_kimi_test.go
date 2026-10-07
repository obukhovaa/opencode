package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream/eventstreamapi"
	"github.com/openai/openai-go"
	"github.com/tidwall/gjson"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
)

// newBedrockKimiTestClient builds the Bedrock provider client for a Kimi model
// against srv, mounted under a /bedrock prefix like the LiteLLM passthrough.
func newBedrockKimiTestClient(t *testing.T, srv *httptest.Server, id models.ModelID, openaiOpts ...OpenAIOption) BedrockClient {
	t.Helper()
	loadConfigIn(t, t.TempDir())
	return newBedrockClient(providerClientOptions{
		apiKey:        "test-key",
		baseURL:       srv.URL + "/bedrock",
		model:         models.SupportedModels[id],
		maxTokens:     1024,
		openaiOptions: openaiOpts,
	})
}

var bedrockKimiPrompt = []message.Message{
	{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "weather in Oslo?"}}},
}

func TestNewBedrockClientRoutesKimiToOpenAI(t *testing.T) {
	tests := []struct {
		name       string
		model      models.ModelID
		wantOpenAI bool
	}{
		{"global kimi", models.BedrockKimiK3, true},
		{"us kimi", models.BedrockUSKimiK3, true},
		{"claude", models.BedrockOpus48, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBedrockClient(providerClientOptions{
				apiKey:    "test-key",
				model:     models.SupportedModels[tt.model],
				maxTokens: 1024,
			}).(*bedrockClient)
			child, isOpenAI := b.childProvider.(*openaiClient)
			if isOpenAI != tt.wantOpenAI {
				t.Fatalf("child provider = %T, want openai = %v", b.childProvider, tt.wantOpenAI)
			}
			if isOpenAI && !child.options.useBedrock {
				t.Fatal("kimi openai child is missing the bedrock middleware option")
			}
		})
	}
}

func TestBedrockKimiModels(t *testing.T) {
	tests := []struct {
		id       models.ModelID
		apiModel string
		premium  float64
	}{
		{models.BedrockKimiK3, "global.moonshotai.kimi-k3", 1},
		{models.BedrockUSKimiK3, "us.moonshotai.kimi-k3", 1.1},
	}
	for _, tt := range tests {
		t.Run(string(tt.id), func(t *testing.T) {
			m, ok := models.SupportedModels[tt.id]
			if !ok {
				t.Fatalf("%s not registered in SupportedModels", tt.id)
			}
			if !models.IsBedrockKimi(tt.id) {
				t.Fatalf("IsBedrockKimi(%s) = false", tt.id)
			}
			if _, ok := models.BedrockAnthropicModels[tt.id]; ok {
				t.Fatalf("%s must not be an Anthropic Bedrock model", tt.id)
			}
			if m.Provider != models.ProviderBedrock || m.APIModel != tt.apiModel {
				t.Fatalf("identity mismatch: %+v", m)
			}
			if !m.CanReason || m.SupportsAdaptiveThinking {
				t.Fatalf("kimi on bedrock takes reasoning_effort, not adaptive thinking: %+v", m)
			}
			// K3's reasoning_effort levels are low|high|max: max is admitted
			// (it is the model's default), xhigh is not a K3 level.
			if !m.SupportsMaximumThinking || m.SupportsXHighThinking {
				t.Fatalf("kimi K3 on bedrock must admit max but not xhigh: %+v", m)
			}
			if !models.IsKimiK3(tt.id) {
				t.Fatalf("IsKimiK3(%s) = false", tt.id)
			}
			want := []float64{3.0 * tt.premium, 3.75 * tt.premium, 15.0 * tt.premium, 0.30 * tt.premium}
			got := []float64{m.CostPer1MIn, m.CostPer1MInCached, m.CostPer1MOut, m.CostPer1MOutCached}
			for i := range want {
				if diff := got[i] - want[i]; diff > 1e-9 || diff < -1e-9 {
					t.Fatalf("costs = %v, want %v", got, want)
				}
			}
		})
	}
	if models.IsBedrockKimi(models.BedrockOpus48) {
		t.Fatal("IsBedrockKimi must be false for Claude on Bedrock")
	}
	if !models.IsKimiK3(models.KimiK3) || models.IsKimiK3(models.KimiK27Code) || models.IsKimiK3(models.BedrockOpus48) {
		t.Fatal("IsKimiK3 must cover exactly the K3 deployments")
	}
}

// TestBedrockKimiStream drives the full streaming path: the chat-completions
// request is rewritten onto invoke-with-response-stream, the binary
// EventStream reply is re-encoded as SSE, and reasoning, text, tool calls and
// the usage chunk (cache writes included) all reach the provider events.
func TestBedrockKimiStream(t *testing.T) {
	chunks := []string{
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"moonshotai.kimi-k3","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"Checking the "},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"moonshotai.kimi-k3","choices":[{"index":0,"delta":{"reasoning_content":"weather."},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"moonshotai.kimi-k3","choices":[{"index":0,"delta":{"content":"Let me look."},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"moonshotai.kimi-k3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"get_weather_0","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"moonshotai.kimi-k3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"Oslo\"}"}}]},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"moonshotai.kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"moonshotai.kimi-k3","choices":[],"usage":{"prompt_tokens":1200,"completion_tokens":40,"total_tokens":1240,"prompt_tokens_details":{"cached_tokens":800,"cache_write_tokens":300}}}`,
	}

	tests := []struct {
		name        string
		contentType string // "" mimics LiteLLM, which forwards none
	}{
		{"litellm passthrough without content type", ""},
		{"bedrock eventstream content type", "application/vnd.amazon.eventstream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/bedrock/model/global.moonshotai.kimi-k3/invoke-with-response-stream" {
					t.Errorf("path = %q", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
					t.Errorf("authorization = %q", got)
				}
				body, _ := io.ReadAll(r.Body)
				// Bedrock sends the trailing usage chunk only when asked.
				for path, want := range map[string]string{
					"model":                        "global.moonshotai.kimi-k3",
					"stream":                       "true",
					"stream_options.include_usage": "true",
				} {
					if got := gjson.GetBytes(body, path).String(); got != want {
						t.Errorf("body %s = %q, want %q", path, got, want)
					}
				}
				// No effort configured: the field stays out so K3's own
				// default (max) applies rather than the OpenAI client's medium.
				if gjson.GetBytes(body, "reasoning_effort").Exists() {
					t.Errorf("reasoning_effort sent without a configured effort: %s", body)
				}

				if tt.contentType == "" {
					w.Header()["Content-Type"] = nil // stop net/http sniffing one
				} else {
					w.Header().Set("Content-Type", tt.contentType)
				}
				for _, c := range chunks {
					_, _ = w.Write(encodeBedrockChunk(t, c))
				}
			}))
			defer srv.Close()

			client := newBedrockKimiTestClient(t, srv, models.BedrockKimiK3)
			var thinking, content string
			var final *ProviderResponse
			for event := range client.stream(context.Background(), bedrockKimiPrompt, nil) {
				switch event.Type {
				case EventThinkingDelta:
					thinking += event.Thinking
				case EventContentDelta:
					content += event.Content
				case EventComplete:
					final = event.Response
				case EventError:
					t.Fatalf("stream error: %v", event.Error)
				}
			}

			if thinking != "Checking the weather." {
				t.Errorf("thinking = %q", thinking)
			}
			if content != "Let me look." {
				t.Errorf("content = %q", content)
			}
			if final == nil {
				t.Fatal("stream ended without EventComplete")
			}
			if final.FinishReason != message.FinishReasonToolUse {
				t.Errorf("finish reason = %q", final.FinishReason)
			}
			if len(final.ToolCalls) != 1 {
				t.Fatalf("tool calls = %+v", final.ToolCalls)
			}
			call := final.ToolCalls[0]
			if call.ID != "get_weather_0" || call.Name != "get_weather" || call.Input != `{"city":"Oslo"}` {
				t.Errorf("tool call = %+v", call)
			}
			want := TokenUsage{InputTokens: 100, OutputTokens: 40, CacheCreationTokens: 300, CacheReadTokens: 800}
			if final.Usage != want {
				t.Errorf("usage = %+v, want %+v", final.Usage, want)
			}
		})
	}
}

func TestBedrockKimiSend(t *testing.T) {
	tests := []struct {
		model    models.ModelID
		wantPath string
	}{
		{models.BedrockKimiK3, "/bedrock/model/global.moonshotai.kimi-k3/invoke"},
		{models.BedrockUSKimiK3, "/bedrock/model/us.moonshotai.kimi-k3/invoke"},
	}
	for _, tt := range tests {
		t.Run(string(tt.model), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.wantPath {
					t.Errorf("path = %q, want %q", r.URL.Path, tt.wantPath)
				}
				body, _ := io.ReadAll(r.Body)
				if gjson.GetBytes(body, "stream").Bool() {
					t.Error("non-streaming request asked for a stream")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"c2","object":"chat.completion","created":1,"model":"moonshotai.kimi-k3",` +
					`"choices":[{"index":0,"message":{"role":"assistant","content":"pong","reasoning_content":"The user said ping."},"finish_reason":"stop"}],` +
					`"usage":{"prompt_tokens":50,"completion_tokens":5,"total_tokens":55,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":40}}}`))
			}))
			defer srv.Close()

			client := newBedrockKimiTestClient(t, srv, tt.model)
			resp, err := client.send(context.Background(), bedrockKimiPrompt, nil)
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if resp.Content != "pong" || resp.FinishReason != message.FinishReasonEndTurn {
				t.Errorf("response = %+v", resp)
			}
			want := TokenUsage{InputTokens: 10, OutputTokens: 5, CacheCreationTokens: 40}
			if resp.Usage != want {
				t.Errorf("usage = %+v, want %+v", resp.Usage, want)
			}
		})
	}
}

// TestBedrockKimiStreamErrors checks that both failure shapes surface as an
// EventError: a mid-stream exception frame (error comes out of the SSE
// reader) and an HTTP error reply (left as JSON for the SDK's error path).
func TestBedrockKimiStreamErrors(t *testing.T) {
	exception := func(t *testing.T) []byte {
		var buf bytes.Buffer
		err := eventstream.NewEncoder().Encode(&buf, eventstream.Message{
			Headers: eventstream.Headers{
				{Name: eventstreamapi.MessageTypeHeader, Value: eventstream.StringValue(eventstreamapi.ExceptionMessageType)},
				{Name: eventstreamapi.ExceptionTypeHeader, Value: eventstream.StringValue("ValidationException")},
			},
			Payload: []byte(`{"message":"bad input"}`),
		})
		if err != nil {
			t.Fatalf("encode frame: %v", err)
		}
		return buf.Bytes()
	}

	tests := []struct {
		name    string
		handler func(t *testing.T, w http.ResponseWriter)
		check   func(t *testing.T, err error)
	}{
		{
			name: "exception frame",
			handler: func(t *testing.T, w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
				_, _ = w.Write(exception(t))
			},
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "ValidationException") || !strings.Contains(err.Error(), "bad input") {
					t.Errorf("error lost detail: %v", err)
				}
			},
		},
		{
			name: "http error reply",
			handler: func(t *testing.T, w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"max_tokens too large","type":"invalid_request_error"}}`))
			},
			check: func(t *testing.T, err error) {
				var apiErr *openai.Error
				if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
					t.Fatalf("error = %v, want a 400 *openai.Error", err)
				}
				if !strings.Contains(err.Error(), "max_tokens too large") {
					t.Errorf("error lost detail: %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tt.handler(t, w)
			}))
			defer srv.Close()

			client := newBedrockKimiTestClient(t, srv, models.BedrockKimiK3)
			var streamErr error
			for event := range client.stream(context.Background(), bedrockKimiPrompt, nil) {
				switch event.Type {
				case EventError:
					streamErr = event.Error
				case EventComplete:
					t.Fatalf("stream completed despite the error: %+v", event.Response)
				}
			}
			if streamErr == nil {
				t.Fatal("expected an EventError")
			}
			tt.check(t, streamErr)
		})
	}
}

func TestOpenAIUsageCacheTokens(t *testing.T) {
	tests := []struct {
		name  string
		usage string
		want  TokenUsage
	}{
		{
			name:  "no details",
			usage: `{"prompt_tokens":50,"completion_tokens":5,"total_tokens":55}`,
			want:  TokenUsage{InputTokens: 50, OutputTokens: 5},
		},
		{
			name:  "openai cache reads only",
			usage: `{"prompt_tokens":1000,"completion_tokens":10,"total_tokens":1010,"prompt_tokens_details":{"cached_tokens":600}}`,
			want:  TokenUsage{InputTokens: 400, OutputTokens: 10, CacheReadTokens: 600},
		},
		{
			name:  "bedrock cache writes and reads",
			usage: `{"prompt_tokens":1200,"completion_tokens":40,"total_tokens":1240,"prompt_tokens_details":{"cached_tokens":800,"cache_write_tokens":300}}`,
			want:  TokenUsage{InputTokens: 100, OutputTokens: 40, CacheCreationTokens: 300, CacheReadTokens: 800},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var usage openai.CompletionUsage
			if err := json.Unmarshal([]byte(tt.usage), &usage); err != nil {
				t.Fatalf("unmarshal usage: %v", err)
			}
			if got := (&openaiClient{}).usage(usage); got != tt.want {
				t.Errorf("usage = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReasoningContentDelta(t *testing.T) {
	tests := []struct {
		name  string
		delta string
		want  string
	}{
		{"reasoning", `{"reasoning_content":"thinking"}`, "thinking"},
		{"content only", `{"content":"hi"}`, ""},
		{"null reasoning", `{"reasoning_content":null}`, ""},
		{"empty reasoning", `{"reasoning_content":""}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var delta openai.ChatCompletionChunkChoiceDelta
			if err := json.Unmarshal([]byte(tt.delta), &delta); err != nil {
				t.Fatalf("unmarshal delta: %v", err)
			}
			if got := reasoningContentDelta(delta); got != tt.want {
				t.Errorf("reasoningContentDelta = %q, want %q", got, tt.want)
			}
		})
	}
}

const chatCompletionPong = `{"id":"c3","object":"chat.completion","created":1,"model":"m",` +
	`"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`

// captureReasoningEffort serves one chat completion and records whether the
// request body carried reasoning_effort, and with which value.
func captureReasoningEffort(t *testing.T) (*httptest.Server, func() (bool, string)) {
	t.Helper()
	var present bool
	var value string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		res := gjson.GetBytes(body, "reasoning_effort")
		present, value = res.Exists(), res.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionPong))
	}))
	t.Cleanup(srv.Close)
	return srv, func() (bool, string) { return present, value }
}

// TestBedrockKimiReasoningEffortParam pins the reasoning_effort wire value
// for Kimi on Bedrock: a configured level is sent as-is — including max, K3's
// documented top level and default, which Bedrock accepts and OpenAI lacks —
// while no configured effort leaves the field out so the model's own default
// applies instead of the OpenAI client's medium.
func TestBedrockKimiReasoningEffortParam(t *testing.T) {
	tests := []struct {
		name string
		opts []OpenAIOption
		want string // "" = field absent
	}{
		{"unset omits the field", nil, ""},
		{"empty omits the field", []OpenAIOption{WithReasoningEffort("")}, ""},
		{"max passes through", []OpenAIOption{WithReasoningEffort("max")}, "max"},
		{"high passes through", []OpenAIOption{WithReasoningEffort("high")}, "high"},
		{"case-folded", []OpenAIOption{WithReasoningEffort("Low")}, "low"},
		{"unknown falls back to the model default", []OpenAIOption{WithReasoningEffort("extreme")}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, sent := captureReasoningEffort(t)
			client := newBedrockKimiTestClient(t, srv, models.BedrockKimiK3, tt.opts...)
			if _, err := client.send(context.Background(), bedrockKimiPrompt, nil); err != nil {
				t.Fatalf("send: %v", err)
			}
			if present, got := sent(); present != (tt.want != "") || got != tt.want {
				t.Errorf("reasoning_effort present=%v value=%q, want %q", present, got, tt.want)
			}
		})
	}
}

// TestOpenAIReasoningEffortDefault locks the OpenAI-proper behaviour the
// Bedrock change must not disturb: with no configured effort the client
// still sends medium, and a configured level is sent lower-cased.
func TestOpenAIReasoningEffortDefault(t *testing.T) {
	tests := []struct {
		name string
		opts []OpenAIOption
		want string
	}{
		{"unset sends medium", nil, "medium"},
		{"empty sends medium", []OpenAIOption{WithReasoningEffort("")}, "medium"},
		{"unknown sends medium", []OpenAIOption{WithReasoningEffort("extreme")}, "medium"},
		{"high passes through", []OpenAIOption{WithReasoningEffort("High")}, "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loadConfigIn(t, t.TempDir())
			srv, sent := captureReasoningEffort(t)
			client := newOpenAIClient(providerClientOptions{
				apiKey:        "test-key",
				baseURL:       srv.URL,
				model:         models.SupportedModels[models.O3],
				maxTokens:     256,
				openaiOptions: tt.opts,
			})
			if _, err := client.send(context.Background(), bedrockKimiPrompt, nil); err != nil {
				t.Fatalf("send: %v", err)
			}
			if present, got := sent(); !present || got != tt.want {
				t.Errorf("reasoning_effort present=%v value=%q, want %q", present, got, tt.want)
			}
		})
	}
}
