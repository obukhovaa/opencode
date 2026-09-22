package provider

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

// newForceTestClient builds an Anthropic client on Claude 4.6 Opus — a model
// with adaptive thinking ON and x-high thinking OFF, so a normal user turn
// requests thinking + a temperature. The forcing wrap-up turn must strip all
// of that (the Anthropic API rejects a forced tool_choice while thinking is on).
func newForceTestClient(t *testing.T) *anthropicClient {
	t.Helper()
	c, ok := newAnthropicClient(providerClientOptions{
		apiKey: "test-key",
		model:  models.SupportedModels[models.Claude46Opus],
	}).(*anthropicClient)
	if !ok {
		t.Fatal("newAnthropicClient did not return *anthropicClient")
	}
	return c
}

func TestPreparedMessages_ForceStructOutput(t *testing.T) {
	a := newForceTestClient(t)
	msgs := a.convertMessages([]message.Message{{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "do it"}},
	}})

	t.Run("forced: tool_choice set, thinking and temperature dropped", func(t *testing.T) {
		ctx := WithForcedTool(context.Background(), tools.StructOutputToolName)
		p := a.preparedMessages(ctx, msgs, nil)

		if p.ToolChoice.OfTool == nil || p.ToolChoice.OfTool.Name != tools.StructOutputToolName {
			t.Fatalf("expected forced ToolChoice=%q, got %+v", tools.StructOutputToolName, p.ToolChoice)
		}
		if p.Thinking.OfAdaptive != nil || p.Thinking.OfEnabled != nil {
			t.Fatalf("forced turn must disable thinking, got %+v", p.Thinking)
		}
		if p.OutputConfig.Effort != "" {
			t.Fatalf("forced turn must omit OutputConfig, got effort %q", p.OutputConfig.Effort)
		}
		if p.Temperature.Valid() {
			t.Fatalf("forced turn must omit temperature (Opus 4.7+ rejects non-default), got a set value")
		}
	})

	t.Run("not forced: no tool_choice, thinking preserved", func(t *testing.T) {
		p := a.preparedMessages(context.Background(), msgs, nil)

		if p.ToolChoice.OfTool != nil {
			t.Fatalf("expected no forced ToolChoice without the signal, got %+v", p.ToolChoice)
		}
		if p.Thinking.OfAdaptive == nil {
			t.Fatalf("adaptive thinking must be preserved for a normal Claude 4.6 Opus user turn, got %+v", p.Thinking)
		}
		if p.OutputConfig.Effort == "" {
			t.Fatalf("expected OutputConfig effort set on a normal thinking turn")
		}
	})
}

// TestPreparedMessages_ForceStructOutputRejectedByModel: Claude Opus 5.5 and
// Claude Fable 5.1 answer tool_choice "tool"/"any" with a 400, so the forcing
// signal must degrade to a normal auto turn on every provider that serves
// them rather than guarantee a failed request.
func TestPreparedMessages_ForceStructOutputRejectedByModel(t *testing.T) {
	for _, id := range []models.ModelID{
		models.Claude55Opus,
		models.BedrockOpus55,
		models.BedrockEUOpus55,
		models.VertexAIOpus55,
		models.ClaudeFable51,
		models.BedrockFable51,
		models.BedrockEUFable51,
		models.VertexAIFable51,
	} {
		t.Run(string(id), func(t *testing.T) {
			a, ok := newAnthropicClient(providerClientOptions{
				apiKey: "test-key",
				model:  models.SupportedModels[id],
			}).(*anthropicClient)
			if !ok {
				t.Fatal("newAnthropicClient did not return *anthropicClient")
			}
			msgs := a.convertMessages([]message.Message{{
				Role:  message.User,
				Parts: []message.ContentPart{message.TextContent{Text: "do it"}},
			}})

			ctx := WithForcedTool(context.Background(), tools.StructOutputToolName)
			p := a.preparedMessages(ctx, msgs, nil)

			if p.ToolChoice.OfTool != nil || p.ToolChoice.OfAny != nil {
				t.Fatalf("forced tool_choice must be dropped for %s, got %+v", id, p.ToolChoice)
			}
			if p.Thinking.OfAdaptive == nil {
				t.Fatalf("degraded forcing turn must match a normal turn (adaptive thinking), got %+v", p.Thinking)
			}
			if p.OutputConfig.Effort == "" {
				t.Fatalf("degraded forcing turn must keep the effort setting")
			}
		})
	}
}
