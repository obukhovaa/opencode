package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/agent"
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

// TestRunFailureMessage_BusyDoesNotAdviseAbort: with the process-global
// session-run ledger, a session running under another agent instance (a flow
// step's own agent) now reports busy at this callsite. The generic advice
// ("POST /abort to release the busy lock") would cancel that live run —
// Cancel's cross-instance fallback reaches it — so ErrSessionBusy must get
// its own wait-and-resend text.
func TestRunFailureMessage_BusyDoesNotAdviseAbort(t *testing.T) {
	msg := runFailureMessage(agent.ErrSessionBusy, "flow-x-step1")
	if strings.Contains(msg, "/abort") || strings.Contains(msg, "/reset") {
		t.Errorf("busy message must not advise abort/reset: %q", msg)
	}
	if !strings.Contains(msg, "resend") {
		t.Errorf("busy message should tell the reviewer to resend: %q", msg)
	}

	// Wrapped errors must take the same branch.
	wrapped := fmt.Errorf("starting run: %w", agent.ErrSessionBusy)
	if runFailureMessage(wrapped, "s") != msg {
		t.Error("wrapped ErrSessionBusy did not take the busy branch")
	}
}

// TestRunFailureMessage_OtherErrorsKeepRecoveryHint: a genuinely stuck session
// (e.g. shutting down) still needs the escape hatch surfaced.
func TestRunFailureMessage_OtherErrorsKeepRecoveryHint(t *testing.T) {
	msg := runFailureMessage(errors.New("provider unavailable"), "sess-9")
	for _, want := range []string{"provider unavailable", "/reset", "/session/sess-9/abort"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q: %q", want, msg)
		}
	}
}
