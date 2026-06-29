package agent

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/message"
)

// TestFilterEmptyUserMessages_AutoRecoveryFromCorruption pins the fix for
// the regression where older builds called createUserMessage on auto-resume
// with empty content, persisting a `user(text="")` row that made every
// subsequent agent.Run on the session fail with HTTP 400 from Vertex/
// Bedrock proxies (`messages: text content blocks must be non-empty`).
//
// The filter drops those rows from the history we send upstream so the
// session continues to work without manual DB intervention.
func TestFilterEmptyUserMessages_AutoRecoveryFromCorruption(t *testing.T) {
	// message.Service.Create appends a Finish marker to every non-assistant
	// message before persisting, so persisted user messages always carry
	// [TextContent, Finish] — not just [TextContent]. The corrupted row
	// observed in MySQL had {text: ""} + Finish; the filter must treat
	// Finish as metadata and still drop the message.
	corrupted := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "what's the time?"},
			message.Finish{Reason: "stop"},
		}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "tc1", Name: "bash", Input: `{"command":"date"}`, Finished: true},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "tc1", Name: "bash", Content: "Mon Jun 28 23:00:00 UTC 2026"},
			message.Finish{Reason: "stop"},
		}},
		// The bad row — what older auto-resume builds persisted, in the
		// exact shape observed in the messages table.
		{Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: ""},
			message.Finish{Reason: "stop"},
		}},
	}

	filtered := filterEmptyUserMessages(corrupted)

	if len(filtered) != 3 {
		t.Fatalf("expected the empty user row to be dropped, got %d messages", len(filtered))
	}
	last := filtered[len(filtered)-1]
	if last.Role != message.Tool {
		t.Errorf("after filter, last message should be the Tool result; got role=%v", last.Role)
	}
}

// TestFilterEmptyUserMessages_KeepsTryAgain pins that the persisted
// `{text: "try again", finish}` shape — a legitimate one-word retry the
// user typed — is NOT mistaken for the empty-content corruption.
func TestFilterEmptyUserMessages_KeepsTryAgain(t *testing.T) {
	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "try again"},
			message.Finish{Reason: "stop"},
		}},
	}
	if got := filterEmptyUserMessages(msgs); len(got) != 1 {
		t.Errorf("real user message 'try again' was filtered, got %d remaining", len(got))
	}
}

func TestFilterEmptyUserMessages_KeepsLegitimate(t *testing.T) {
	cases := []struct {
		name string
		msg  message.Message
	}{
		{
			name: "user with non-empty text",
			msg: message.Message{
				Role:  message.User,
				Parts: []message.ContentPart{message.TextContent{Text: "hello"}},
			},
		},
		{
			name: "user with text + attachment",
			msg: message.Message{
				Role: message.User,
				Parts: []message.ContentPart{
					message.TextContent{Text: ""},
					message.BinaryContent{MIMEType: "image/png", Data: []byte{0x89, 0x50}},
				},
			},
		},
		{
			name: "tool result message (Role=Tool, never filtered even if text-empty looking)",
			msg: message.Message{
				Role:  message.Tool,
				Parts: []message.ContentPart{message.ToolResult{ToolCallID: "x", Name: "bash", Content: ""}},
			},
		},
		{
			name: "assistant message",
			msg: message.Message{
				Role:  message.Assistant,
				Parts: []message.ContentPart{message.TextContent{Text: ""}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filtered := filterEmptyUserMessages([]message.Message{tc.msg})
			if len(filtered) != 1 {
				t.Errorf("legitimate message was filtered: %+v", tc.msg)
			}
		})
	}
}

func TestFilterEmptyUserMessages_WhitespaceOnlyDropped(t *testing.T) {
	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "   \t\n  "}}},
	}
	if got := filterEmptyUserMessages(msgs); len(got) != 0 {
		t.Errorf("whitespace-only user message should be dropped, got %d remaining", len(got))
	}
}
