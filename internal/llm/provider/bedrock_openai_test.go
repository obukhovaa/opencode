package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

func TestBedrockOpenAIModels(t *testing.T) {
	tests := []struct {
		id                    models.ModelID
		apiModel              string
		in, write, out, read  float64
		xhigh, max            bool
		effort                string
		attachments           bool
		contextWindow, output int64
	}{
		{models.BedrockKimiK31, "global.moonshotai.kimi-k3.1", 4, 5, 20, 0.20, true, true, "max", true, 1_048_576, 131_072},
		{models.BedrockUSKimiK31, "us.moonshotai.kimi-k3.1", 4.4, 5.5, 22, 0.22, true, true, "max", true, 1_048_576, 131_072},
		{models.BedrockGPT61Sol, "global.openai.gpt-6.1-sol", 2, 2.5, 10, 0.10, true, true, "medium", false, 1_050_000, 128_000},
		{models.BedrockUSGPT61Sol, "us.openai.gpt-6.1-sol", 2.2, 2.75, 11, 0.11, true, true, "medium", false, 1_050_000, 128_000},
		{models.BedrockGPT61Terra, "global.openai.gpt-6.1-terra", 1, 1.25, 5, 0.05, false, false, "medium", false, 1_050_000, 128_000},
		{models.BedrockUSGPT61Terra, "us.openai.gpt-6.1-terra", 1.1, 1.375, 5.5, 0.055, false, false, "medium", false, 1_050_000, 128_000},
	}
	for _, tt := range tests {
		t.Run(string(tt.id), func(t *testing.T) {
			m, ok := models.SupportedModels[tt.id]
			if !ok {
				t.Fatalf("%s not registered", tt.id)
			}
			if m.Provider != models.ProviderBedrock || m.APIModel != tt.apiModel || !m.BedrockOpenAIChat() {
				t.Fatalf("identity mismatch: %+v", m)
			}
			got := []float64{m.CostPer1MIn, m.CostPer1MInCached, m.CostPer1MOut, m.CostPer1MOutCached}
			want := []float64{tt.in, tt.write, tt.out, tt.read}
			for i := range want {
				if diff := got[i] - want[i]; diff > 1e-9 || diff < -1e-9 {
					t.Fatalf("costs = %v, want %v", got, want)
				}
			}
			if !m.CanReason || m.SupportsAdaptiveThinking || m.SupportsTaskBudget || m.SupportsToolSearch {
				t.Fatalf("an OpenAI-wire model reasons through reasoning_effort only: %+v", m)
			}
			if m.SupportsXHighThinking != tt.xhigh || m.SupportsMaximumThinking != tt.max {
				t.Fatalf("xhigh/max = %v/%v, want %v/%v", m.SupportsXHighThinking, m.SupportsMaximumThinking, tt.xhigh, tt.max)
			}
			if m.DefaultReasoningEffort != tt.effort || m.SupportsAttachments != tt.attachments {
				t.Fatalf("default effort %q attachments %v, want %q %v", m.DefaultReasoningEffort, m.SupportsAttachments, tt.effort, tt.attachments)
			}
			if m.ContextWindow != tt.contextWindow || m.DefaultMaxTokens != tt.output {
				t.Fatalf("window/output = %d/%d", m.ContextWindow, m.DefaultMaxTokens)
			}
		})
	}
	for _, id := range []models.ModelID{models.BedrockGPT61Sol, models.BedrockUSGPT61Terra} {
		m := models.SupportedModels[id]
		if m.LongContextThreshold != 272_000 || m.LongContextCostMultiplier != 2 || m.LongContextOutputCostMultiplier != 1.5 {
			t.Fatalf("%s long-context tier = %d %v %v", id, m.LongContextThreshold, m.LongContextCostMultiplier, m.LongContextOutputCostMultiplier)
		}
	}
	if !models.IsKimiK3(models.BedrockKimiK31) || !models.IsKimiK3(models.BedrockUSKimiK31) || models.IsKimiK3(models.BedrockGPT61Sol) {
		t.Fatal("IsKimiK3 must cover K3.1 on Bedrock and nothing else new")
	}
}

// TestBedrockOpenAIReasoningEffortParam pins the reasoning_effort sent to the
// OpenAI-wire Bedrock models: an unset effort sends the model's
// DefaultReasoningEffort (or nothing when it has none, Kimi K3), xhigh goes
// out as xhigh where the model takes it and as high where it does not.
func TestBedrockOpenAIReasoningEffortParam(t *testing.T) {
	tests := []struct {
		model  models.ModelID
		effort string
		want   string // "" = field absent
	}{
		{models.BedrockGPT61Sol, "", "medium"},
		{models.BedrockUSGPT61Terra, "", "medium"},
		{models.BedrockKimiK31, "", "max"},
		{models.BedrockKimiK3, "", ""},
		{models.BedrockGPT61Sol, "xhigh", "xhigh"},
		{models.BedrockUSKimiK31, "xhigh", "xhigh"},
		{models.BedrockGPT61Terra, "xhigh", "high"},
		{models.BedrockGPT61Terra, "low", "low"},
	}
	for _, tt := range tests {
		t.Run(string(tt.model)+"/"+tt.effort, func(t *testing.T) {
			srv, sent := captureReasoningEffort(t)
			client := newBedrockKimiTestClient(t, srv, tt.model, WithReasoningEffort(tt.effort))
			if _, err := client.send(context.Background(), bedrockKimiPrompt, nil); err != nil {
				t.Fatalf("send: %v", err)
			}
			if present, got := sent(); present != (tt.want != "") || got != tt.want {
				t.Errorf("reasoning_effort present=%v value=%q, want %q", present, got, tt.want)
			}
		})
	}
}

// weatherTool is a minimal tool for request-shaping tests.
type weatherTool struct{}

func (weatherTool) Info() tools.ToolInfo {
	return tools.ToolInfo{
		Name:        "get_weather",
		Description: "Current weather for a city",
		Parameters:  map[string]any{"city": map[string]any{"type": "string"}},
		Required:    []string{"city"},
	}
}

func (weatherTool) Run(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
	return tools.NewTextResponse("sunny"), nil
}

func (weatherTool) AllowParallelism(tools.ToolCall, []tools.ToolCall) bool { return true }

func (weatherTool) IsBaseline() bool { return true }

// TestBedrockGPTToolRoundTrip drives GPT-6.1 through the InvokeModel routes
// on both paths: send carries the tool definition and parses the tool call,
// stream re-encodes the EventStream reply and surfaces the call too.
func TestBedrockGPTToolRoundTrip(t *testing.T) {
	const toolCallReply = `{"id":"c1","object":"chat.completion","created":1,"model":"openai.gpt-6.1-sol",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Oslo\"}"}}]},"finish_reason":"tool_calls"}],` +
		`"usage":{"prompt_tokens":80,"completion_tokens":12,"total_tokens":92}}`
	chunks := []string{
		`{"id":"c2","object":"chat.completion.chunk","created":1,"model":"openai.gpt-6.1-terra","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_2","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`,
		`{"id":"c2","object":"chat.completion.chunk","created":1,"model":"openai.gpt-6.1-terra","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"Oslo\"}"}}]},"finish_reason":null}]}`,
		`{"id":"c2","object":"chat.completion.chunk","created":1,"model":"openai.gpt-6.1-terra","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"c2","object":"chat.completion.chunk","created":1,"model":"openai.gpt-6.1-terra","choices":[],"usage":{"prompt_tokens":80,"completion_tokens":12,"total_tokens":92}}`,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if name := gjson.GetBytes(body, "tools.0.function.name").String(); name != "get_weather" {
			t.Errorf("tool definition missing from the request: %s", body)
		}
		switch r.URL.Path {
		case "/bedrock/model/global.openai.gpt-6.1-sol/invoke":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(toolCallReply))
		case "/bedrock/model/us.openai.gpt-6.1-terra/invoke-with-response-stream":
			w.Header()["Content-Type"] = nil
			for _, c := range chunks {
				_, _ = w.Write(encodeBedrockChunk(t, c))
			}
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	checkCall := func(t *testing.T, resp *ProviderResponse, id string) {
		t.Helper()
		if resp == nil || resp.FinishReason != message.FinishReasonToolUse || len(resp.ToolCalls) != 1 {
			t.Fatalf("response = %+v", resp)
		}
		call := resp.ToolCalls[0]
		var args map[string]string
		if call.ID != id || call.Name != "get_weather" || json.Unmarshal([]byte(call.Input), &args) != nil || args["city"] != "Oslo" {
			t.Fatalf("tool call = %+v", call)
		}
	}

	t.Run("send", func(t *testing.T) {
		client := newBedrockKimiTestClient(t, srv, models.BedrockGPT61Sol)
		resp, err := client.send(context.Background(), bedrockKimiPrompt, []tools.BaseTool{weatherTool{}})
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		checkCall(t, resp, "call_1")
	})
	t.Run("stream", func(t *testing.T) {
		client := newBedrockKimiTestClient(t, srv, models.BedrockUSGPT61Terra)
		var final *ProviderResponse
		for event := range client.stream(context.Background(), bedrockKimiPrompt, []tools.BaseTool{weatherTool{}}) {
			switch event.Type {
			case EventComplete:
				final = event.Response
			case EventError:
				t.Fatalf("stream error: %v", event.Error)
			}
		}
		checkCall(t, final, "call_2")
	})
}
