package agent

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

func toolMsgWith(parts ...message.ContentPart) *message.Message {
	return &message.Message{Role: message.Tool, Parts: parts}
}

func structResult(content string, isErr bool) message.ToolResult {
	return message.ToolResult{
		Name:    tools.StructOutputToolName,
		Content: content,
		IsError: isErr,
	}
}

// The (structOutput, isErr) pair drives the finish-on-struct_output short
// circuit in processGeneration: a run finishes without the wrap-up provider
// round-trip as soon as a NON-error struct_output is captured. These cases
// pin the capture/latch semantics that decision depends on.
func TestCaptureStructOutput(t *testing.T) {
	success := structResult(`{"status":"done"}`, false)
	rejected := structResult(`schema validation failed`, true)

	t.Run("no struct_output and nil current stays err-nil", func(t *testing.T) {
		out, isErr := captureStructOutput(toolMsgWith(message.ToolResult{Name: "bash", Content: "ok"}), nil, true)
		if out != nil || !isErr {
			t.Fatalf("expected (nil, true), got (%v, %v)", out, isErr)
		}
	})

	t.Run("error result is captured as fallback", func(t *testing.T) {
		out, isErr := captureStructOutput(toolMsgWith(rejected), nil, true)
		if out == nil || !out.IsError || !isErr {
			t.Fatalf("expected error fallback captured, got (%v, %v)", out, isErr)
		}
	})

	t.Run("success result flips isErr false", func(t *testing.T) {
		out, isErr := captureStructOutput(toolMsgWith(success), nil, true)
		if out == nil || out.Content != success.Content || isErr {
			t.Fatalf("expected success captured, got (%v, %v)", out, isErr)
		}
	})

	t.Run("success replaces earlier error fallback", func(t *testing.T) {
		prior := rejected
		out, isErr := captureStructOutput(toolMsgWith(success), &prior, true)
		if out == nil || out.IsError || isErr {
			t.Fatalf("expected success to replace error fallback, got (%v, %v)", out, isErr)
		}
	})

	t.Run("earlier error fallback survives a turn without struct_output", func(t *testing.T) {
		prior := rejected
		out, isErr := captureStructOutput(toolMsgWith(message.ToolResult{Name: "bash", Content: "ok"}), &prior, true)
		if out == nil || !out.IsError || !isErr {
			t.Fatalf("expected error fallback kept, got (%v, %v)", out, isErr)
		}
	})

	t.Run("later success replaces earlier success on the deferred-finish path", func(t *testing.T) {
		prior := success
		updated := structResult(`{"status":"done","task_results":3}`, false)
		out, isErr := captureStructOutput(toolMsgWith(updated), &prior, false)
		if out == nil || out.Content != updated.Content || isErr {
			t.Fatalf("expected updated success to win, got (%v, %v)", out, isErr)
		}
	})

	t.Run("captured success is not downgraded by a later rejected attempt", func(t *testing.T) {
		prior := success
		out, isErr := captureStructOutput(toolMsgWith(rejected), &prior, false)
		if out != &prior || isErr {
			t.Fatalf("expected success kept over later rejection, got (%v, %v)", out, isErr)
		}
	})
}
