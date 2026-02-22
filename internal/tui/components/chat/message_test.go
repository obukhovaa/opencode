package chat

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/message"
)

func TestRenderAssistantMessage_EmptyContentNoTextBlock(t *testing.T) {
	tests := []struct {
		name             string
		parts            []message.ContentPart
		wantTextMessage  bool
		wantToolMessages int
	}{
		{
			name: "tool call only, no text",
			parts: []message.ContentPart{
				message.ToolCall{ID: "1", Name: "bash", Input: `{"command":"ls"}`, Finished: true},
			},
			wantTextMessage:  false,
			wantToolMessages: 1,
		},
		{
			name: "empty string text + tool call",
			parts: []message.ContentPart{
				message.TextContent{Text: ""},
				message.ToolCall{ID: "1", Name: "bash", Input: `{"command":"ls"}`, Finished: true},
			},
			wantTextMessage:  false,
			wantToolMessages: 1,
		},
		{
			name: "whitespace-only text + tool call",
			parts: []message.ContentPart{
				message.TextContent{Text: "\n"},
				message.ToolCall{ID: "1", Name: "bash", Input: `{"command":"ls"}`, Finished: true},
			},
			wantTextMessage:  false,
			wantToolMessages: 1,
		},
		{
			name: "spaces-only text + tool call",
			parts: []message.ContentPart{
				message.TextContent{Text: "   "},
				message.ToolCall{ID: "1", Name: "bash", Input: `{"command":"ls"}`, Finished: true},
			},
			wantTextMessage:  false,
			wantToolMessages: 1,
		},
		{
			name: "real text + tool call",
			parts: []message.ContentPart{
				message.TextContent{Text: "Let me check that for you."},
				message.ToolCall{ID: "1", Name: "bash", Input: `{"command":"ls"}`, Finished: true},
			},
			wantTextMessage:  true,
			wantToolMessages: 1,
		},
		{
			name: "finished without output",
			parts: []message.ContentPart{
				message.Finish{Reason: message.FinishReasonEndTurn},
			},
			wantTextMessage: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := message.Message{
				ID:    "test-msg",
				Role:  message.Assistant,
				Parts: tt.parts,
			}

			results := renderAssistantMessage(msg, 0, nil, nil, "", false, 80, 0)

			var textMessages, toolMessages int
			for _, r := range results {
				switch r.messageType {
				case assistantMessageType:
					textMessages++
				case toolMessageType:
					toolMessages++
				}
			}

			if tt.wantTextMessage && textMessages == 0 {
				t.Errorf("expected an assistant text message, got none")
			}
			if !tt.wantTextMessage && textMessages > 0 {
				t.Errorf("expected no assistant text message, got %d", textMessages)
			}
			if toolMessages != tt.wantToolMessages {
				t.Errorf("expected %d tool messages, got %d", tt.wantToolMessages, toolMessages)
			}
		})
	}
}
