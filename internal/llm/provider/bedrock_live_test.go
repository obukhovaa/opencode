//go:build bedrock_live

package provider

// Live tool-call round trip for the OpenAI-wire Bedrock models through the
// real Bedrock client, the counterpart of scripts/probe_bedrock_tools.sh:
//
//	LOCAL_ENDPOINT=https://litellm.example LOCAL_ENDPOINT_API_KEY=... \
//	  go test -tags bedrock_live -run TestBedrockLiveToolRoundTrip ./internal/llm/provider/
//
// It skips without the two variables, so the tag alone never fails CI.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

func TestBedrockLiveToolRoundTrip(t *testing.T) {
	endpoint, key := os.Getenv("LOCAL_ENDPOINT"), os.Getenv("LOCAL_ENDPOINT_API_KEY")
	if endpoint == "" || key == "" {
		t.Skip("LOCAL_ENDPOINT and LOCAL_ENDPOINT_API_KEY are required")
	}
	ids := []models.ModelID{
		models.BedrockKimiK31, models.BedrockUSKimiK31,
		models.BedrockGPT61Sol, models.BedrockUSGPT61Sol,
		models.BedrockGPT61Terra, models.BedrockUSGPT61Terra,
	}
	toolSet := []tools.BaseTool{weatherTool{}}
	question := []message.Message{{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "What is the weather in Oslo right now? Use the get_weather tool."}},
	}}
	// followUp answers the tool call and returns the second turn's history.
	followUp := func(call message.ToolCall) []message.Message {
		return append(append([]message.Message(nil), question...),
			message.Message{Role: message.Assistant, Parts: []message.ContentPart{call}},
			message.Message{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{
				ToolCallID: call.ID, Name: call.Name, Content: `{"temperature_c": 7, "conditions": "light rain"}`,
			}}},
		)
	}

	for _, id := range ids {
		model := models.SupportedModels[id]
		newClient := func(t *testing.T) BedrockClient {
			loadConfigIn(t, t.TempDir())
			return newBedrockClient(providerClientOptions{
				apiKey:        key,
				baseURL:       strings.TrimSuffix(endpoint, "/") + "/bedrock",
				model:         model,
				maxTokens:     4096,
				systemMessage: "Answer weather questions with the get_weather tool.",
			})
		}
		toolCall := func(t *testing.T, resp *ProviderResponse) message.ToolCall {
			t.Helper()
			if resp == nil || len(resp.ToolCalls) == 0 || resp.ToolCalls[0].Name != "get_weather" {
				t.Fatalf("no get_weather call: %+v", resp)
			}
			return resp.ToolCalls[0]
		}

		t.Run(string(id)+"/send", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			client := newClient(t)
			first, err := client.send(ctx, question, toolSet)
			if err != nil {
				t.Fatalf("turn 1: %v", err)
			}
			second, err := client.send(ctx, followUp(toolCall(t, first)), toolSet)
			if err != nil {
				t.Fatalf("turn 2: %v", err)
			}
			if strings.TrimSpace(second.Content) == "" {
				t.Fatalf("turn 2 has no answer: %+v", second)
			}
		})

		t.Run(string(id)+"/stream", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			client := newClient(t)
			collect := func(t *testing.T, history []message.Message) *ProviderResponse {
				t.Helper()
				var final *ProviderResponse
				for event := range client.stream(ctx, history, toolSet) {
					switch event.Type {
					case EventComplete:
						final = event.Response
					case EventError:
						t.Fatalf("stream error: %v", event.Error)
					}
				}
				return final
			}
			first := collect(t, question)
			second := collect(t, followUp(toolCall(t, first)))
			if second == nil || strings.TrimSpace(second.Content) == "" {
				t.Fatalf("turn 2 has no answer: %+v", second)
			}
		})
	}
}
