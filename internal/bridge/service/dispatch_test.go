package service

import (
	"errors"
	"strings"
	"testing"

	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
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

// TestRunFailureMessage_BusyDoesNotAdviseAbort was converted: ErrSessionBusy
// no longer reaches runFailureMessage — the retry loop in handleInbound handles
// it first. This test now verifies that:
//
//  1. runFailureMessage still works for non-busy errors (regression guard).
//  2. When given ErrSessionBusy directly (which can happen in edge cases),
//     the generic message does NOT contain "resend" or abort-dangerous advice.
//     (The prior version had a special "wait and resend" arm; removing it
//     ensures the generic recovery hint is returned instead.)
//
// The full retry behavioral test lives in dispatch_busy_retry_test.go as
// TestHandleInbound_BusyRetryPreservesContent.
func TestRunFailureMessage_BusyDoesNotAdviseAbort(t *testing.T) {
	// Verify ErrSessionBusy now produces the generic error message.
	// Previously it returned a special "resend" message; now it is the
	// same as any other error (the retry loop prevents it from reaching here).
	msg := runFailureMessage(agentpkg.ErrSessionBusy, "flow-x-step1")
	if msg == "" {
		t.Fatal("runFailureMessage returned empty string")
	}
	// The generic message MUST contain the recovery hint (not suppress it).
	if !strings.Contains(msg, "/abort") || !strings.Contains(msg, "/reset") {
		t.Errorf("generic runFailureMessage missing recovery hint: %q", msg)
	}
	// The "resend" wording was the old ErrSessionBusy-specific text. It must
	// NOT appear — after the retry fix, if ErrSessionBusy somehow reaches here
	// the user gets the generic escape hatch, not a misleading "resend" prompt.
	if strings.Contains(msg, "resend") {
		t.Errorf("generic message should not say \"resend\" (old busy-specific text): %q", msg)
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
