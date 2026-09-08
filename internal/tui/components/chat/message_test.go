package chat

import (
	"strings"
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

func TestCollapseSkillBlocks(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "no block is byte-identical",
			text: "just a message\nwith two lines\n",
			want: "just a message\nwith two lines\n",
		},
		{
			name: "single block collapses",
			text: "<skill_content name=\"reviewer\">\nline one\nline two\n</skill_content>",
			want: "⚡ skill:reviewer · 2 lines",
		},
		{
			name: "one-line body is singular",
			text: "<skill_content name=\"reviewer\">\nonly line\n</skill_content>",
			want: "⚡ skill:reviewer · 1 line",
		},
		{
			name: "empty body counts zero",
			text: "<skill_content name=\"reviewer\">\n\n</skill_content>",
			want: "⚡ skill:reviewer · 0 lines",
		},
		{
			name: "two blocks keep the prose between them",
			text: "<skill_content name=\"a\">\nx\n</skill_content>\nmind the tests\n<skill_content name=\"b\">\ny\nz\n</skill_content>",
			want: "⚡ skill:a · 1 line\nmind the tests\n⚡ skill:b · 2 lines",
		},
		{
			name: "prose before and after is preserved",
			text: "before\n<skill_content name=\"a\">\nx\n</skill_content>\nafter",
			want: "before\n⚡ skill:a · 1 line\nafter",
		},
		{
			// Better to show the raw text than to swallow the rest of the message.
			name: "unterminated opening tag is left verbatim",
			text: "<skill_content name=\"a\">\nx\ny",
			want: "<skill_content name=\"a\">\nx\ny",
		},
		{
			name: "a body that mentions the tag does not terminate early",
			text: "<skill_content name=\"a\">\nwrap in <skill_content name=\"x\"> tags\ndone\n</skill_content>",
			want: "⚡ skill:a · 2 lines",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := collapseSkillBlocks(tt.text); got != tt.want {
				t.Errorf("collapseSkillBlocks() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

// TestRenderUserMessageLeavesStoredContentIntact: collapsing is a display
// transform, so the message the session stores (and the model receives) must be
// untouched by rendering it.
func TestRenderUserMessageLeavesStoredContentIntact(t *testing.T) {
	content := "<skill_content name=\"reviewer\">\nReview the diff.\n</skill_content>\nmind the tests"
	msg := message.Message{
		ID:   "m1",
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: content},
		},
	}

	rendered := renderUserMessage(msg, false, 80, 0)
	if !strings.Contains(rendered.content, "skill:reviewer") {
		t.Errorf("rendered message does not show the collapsed summary:\n%s", rendered.content)
	}
	if strings.Contains(rendered.content, "Review the diff.") {
		t.Errorf("rendered message still shows the skill body:\n%s", rendered.content)
	}
	if msg.Content().String() != content {
		t.Errorf("stored content changed to %q", msg.Content().String())
	}
}
