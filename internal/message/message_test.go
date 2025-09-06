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

	tokens := EstimateTokens(messages, tools)
	if tokens != 63 {
		t.Errorf("Expect 12 tokens, actual %d", tokens)
	}
}
