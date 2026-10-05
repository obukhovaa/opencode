package message

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/tools"
	mock_tools "github.com/opencode-ai/opencode/internal/llm/tools/mocks"
	"go.uber.org/mock/gomock"
)

func TestAnthropicCountTokens(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	// Test with simple messages
	messages := []Message{
		{
			Role:  User,
			Parts: []ContentPart{TextContent{Text: "Hello"}},
		},
		{
			Role:  Assistant,
			Parts: []ContentPart{TextContent{Text: "Hi there! How can I help you?"}},
		},
	}

	mockTool := mock_tools.NewMockBaseTool(ctrl)
	mockTool.EXPECT().Info().
		Return(tools.ToolInfo{Name: "Mocky", Description: "Do some dirty", Parameters: map[string]any{}, Required: []string{}})
	tools := []tools.BaseTool{mockTool}

	tokens := EstimateTokens(messages, tools, BytesPerTokenEta)
	if tokens != 63 {
		t.Errorf("Expect 12 tokens, actual %d", tokens)
	}
}

func TestEstimateTokensCountsToolAndReasoningPayloads(t *testing.T) {
	cases := []struct {
		name string
		part ContentPart
		want int64
	}{
		{"text", TextContent{Text: "abcdefgh"}, (8 + 100) / BytesPerTokenEta},
		{"tool call", ToolCall{Name: "bash", Input: `{"cmd":"ls"}`}, (4 + 12 + 100) / BytesPerTokenEta},
		{"tool result", ToolResult{Content: "0123456789012345"}, (16 + 100) / BytesPerTokenEta},
		{"reasoning", ReasoningContent{Thinking: "thinking"}, (8 + 100) / BytesPerTokenEta},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EstimateTokens([]Message{{Role: Assistant, Parts: []ContentPart{tc.part}}}, nil, BytesPerTokenEta)
			if got != tc.want {
				t.Errorf("EstimateTokens = %d, want %d", got, tc.want)
			}
		})
	}
}
