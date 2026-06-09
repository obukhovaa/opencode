package service

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/message"
)

func TestAgentMessageTextConcatenates(t *testing.T) {
	t.Parallel()
	m := message.Message{
		Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "internal CoT, MUST NOT surface"},
			message.TextContent{Text: "Looks good"},
			message.ToolCall{ID: "t1", Name: "edit"},
			message.TextContent{Text: ". Shipping it."},
		},
	}
	got := agentMessageText(m)
	want := "Looks good\n. Shipping it."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAgentMessageTextEmptyWhenNoTextParts(t *testing.T) {
	t.Parallel()
	m := message.Message{
		Parts: []message.ContentPart{
			message.ToolCall{ID: "t1", Name: "edit"},
		},
	}
	if got := agentMessageText(m); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestAgentMessageTextSingleTextPart(t *testing.T) {
	t.Parallel()
	m := message.Message{
		Parts: []message.ContentPart{
			message.TextContent{Text: "alone"},
		},
	}
	if got := agentMessageText(m); got != "alone" {
		t.Errorf("got %q", got)
	}
}
