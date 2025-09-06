package provider

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	mock_tools "github.com/opencode-ai/opencode/internal/llm/tools/mocks"
	"github.com/opencode-ai/opencode/internal/message"
	"go.uber.org/mock/gomock"
)

func TestAnthropicCountTokens(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Skip test if no API key is available
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create a mock anthropic client for testing
	client := &anthropicClient{
		providerOptions: providerClientOptions{
			model: models.Model{
				APIModel: "claude-3-haiku-20240307",
			},
			systemMessage: "You are a helpful assistant.",
		},
	}

	// Test with simple messages
	messages := []message.Message{
		{
			Role:  message.User,
			Parts: []message.ContentPart{message.TextContent{Text: "Hello"}},
		},
		{
			Role:  message.Assistant,
			Parts: []message.ContentPart{message.TextContent{Text: "Hi there! How can I help you?"}},
		},
	}

	mockTool := mock_tools.NewMockBaseTool(ctrl)
	mockTool.EXPECT().Info().
		Return(tools.ToolInfo{Name: "Mocky", Description: "Do some dirty", Parameters: map[string]any{}, Required: []string{}})

	tools := []tools.BaseTool{mockTool}

	// Test the conversion logic (without making actual API call)
	anthropicMessages := client.convertMessages(messages)
	if len(anthropicMessages) != 2 {
		t.Errorf("Expected 2 converted messages, got %d", len(anthropicMessages))
	}

	// Test that the method signature is correct and doesn't panic
	ctx := context.Background()
	tokens, err := client.countTokens(ctx, messages, tools)
	if tokens != 0 {
		t.Errorf("Expect 0 tokens since error expected, actual %d", tokens)
	}

	// We expect an error since we don't have a real client configured
	// but the method should not panic and should return a proper error
	if err == nil {
		t.Log("CountTokens succeeded (likely with real API key)")
	} else {
		t.Logf("CountTokens failed as expected without API key: %v", err)
	}
}

func TestAnthropicCountTokensEmptyMessages(t *testing.T) {
	client := &anthropicClient{
		providerOptions: providerClientOptions{
			model: models.Model{
				APIModel: "claude-3-haiku-20240307",
			},
		},
	}

	// Test with empty messages
	messages := []message.Message{}
	anthropicMessages := client.convertMessages(messages)

	if len(anthropicMessages) != 0 {
		t.Errorf("Expected 0 converted messages, got %d", len(anthropicMessages))
	}
}

