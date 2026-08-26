package provider

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildGenerationInput(t *testing.T) {
	msgs := []message.Message{
		{
			Role:  message.User,
			Parts: []message.ContentPart{message.TextContent{Text: "fix the bug"}},
		},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: "looking"},
				message.ToolCall{ID: "tc1", Name: "bash", Input: `{"command":"go test"}`},
			},
		},
		{
			Role: message.Tool,
			Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "tc1", Name: "bash", Content: "ok"},
				message.ToolResult{ToolCallID: "tc2", Name: "read", Content: "boom", IsError: true},
			},
		},
	}

	got := buildGenerationInput("you are a coder", msgs)

	require.Len(t, got, 5)
	assert.Equal(t, genChatMessage{Role: "system", Content: "you are a coder"}, got[0])
	assert.Equal(t, genChatMessage{Role: "user", Content: "fix the bug"}, got[1])
	assert.Equal(t, "assistant", got[2].Role)
	assert.Equal(t, "looking", got[2].Content)
	require.Len(t, got[2].ToolCalls, 1)
	assert.Equal(t, genToolCall{ID: "tc1", Name: "bash", Arguments: `{"command":"go test"}`}, got[2].ToolCalls[0])
	assert.Equal(t, genChatMessage{Role: "tool", ToolCallID: "tc1", Name: "bash", Content: "ok"}, got[3])
	assert.Equal(t, genChatMessage{Role: "tool", ToolCallID: "tc2", Name: "read", Content: "[error] boom"}, got[4])
}

func TestBuildGenerationInputNoSystem(t *testing.T) {
	got := buildGenerationInput("", []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}},
	})
	require.Len(t, got, 1)
	assert.Equal(t, "user", got[0].Role)
}

func TestBuildGenerationInputBinarySummarized(t *testing.T) {
	got := buildGenerationInput("", []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "see attachment"},
				message.BinaryContent{MIMEType: "image/png", Data: make([]byte, 1024)},
			},
		},
	})
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Content, "see attachment")
	assert.Contains(t, got[0].Content, "[binary attachment: image/png, 1024 bytes]")
	assert.NotContains(t, got[0].Content, "AAAA") // no base64 payload
}

func TestBuildGenerationOutput(t *testing.T) {
	resp := &ProviderResponse{
		Content: "done",
		Reasoning: []message.ReasoningContent{
			{Thinking: "step one"},
			{Redacted: true, Data: "opaque"}, // redacted blocks are skipped
			{Thinking: "step two"},
		},
		ToolCalls:    []message.ToolCall{{ID: "tc1", Name: "edit", Input: `{"path":"a.go"}`}},
		FinishReason: message.FinishReasonEndTurn,
	}

	got := buildGenerationOutput(resp)

	require.NotNil(t, got)
	assert.Equal(t, "assistant", got.Role)
	assert.Equal(t, "done", got.Content)
	assert.Equal(t, "step one\nstep two", got.Reasoning)
	require.Len(t, got.ToolCalls, 1)
	assert.Equal(t, genToolCall{ID: "tc1", Name: "edit", Arguments: `{"path":"a.go"}`}, got.ToolCalls[0])
	assert.Equal(t, string(message.FinishReasonEndTurn), got.FinishReason)
}

func TestBuildGenerationOutputNil(t *testing.T) {
	assert.Nil(t, buildGenerationOutput(nil))
}
