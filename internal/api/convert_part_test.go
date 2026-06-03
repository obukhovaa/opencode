package api

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/message"
)

func TestConvertPartToolCallPending(t *testing.T) {
	got := ConvertPart("msg-1", "sess-1", message.ToolCall{
		ID:       "call-1",
		Name:     "bash",
		Input:    `{"cmd":"ls"}`,
		Finished: false,
	})
	if got.Type != "tool" || got.CallID != "call-1" || got.Tool != "bash" {
		t.Fatalf("envelope wrong: %+v", got)
	}
	if got.State == nil || got.State.Status != "pending" {
		t.Fatalf("status: want pending, got %+v", got.State)
	}
	if got.State.Input["cmd"] != "ls" {
		t.Fatalf("input parse: %+v", got.State.Input)
	}
	if got.MessageID != "msg-1" || got.SessionID != "sess-1" {
		t.Fatalf("ids: %+v", got)
	}
	if got.ID != "tool-call-1" {
		t.Fatalf("id derivation: %s", got.ID)
	}
}

func TestConvertPartToolCallRunning(t *testing.T) {
	got := ConvertPart("msg-1", "sess-1", message.ToolCall{
		ID:       "call-1",
		Finished: true,
	})
	if got.State.Status != "running" {
		t.Fatalf("status: want running, got %s", got.State.Status)
	}
}

func TestConvertPartToolResultCompleted(t *testing.T) {
	got := ConvertPart("msg-1", "sess-1", message.ToolResult{
		ToolCallID: "call-1",
		Name:       "bash",
		Content:    "file1\nfile2",
		Metadata:   `{"lines":2}`,
		IsError:    false,
	})
	if got.State.Status != "completed" {
		t.Fatalf("status: want completed, got %s", got.State.Status)
	}
	if got.State.Output != "file1\nfile2" {
		t.Fatalf("output: %s", got.State.Output)
	}
	if got.State.Error != "" {
		t.Fatalf("error should be empty: %s", got.State.Error)
	}
	if got.State.Metadata["lines"].(float64) != 2 {
		t.Fatalf("metadata: %+v", got.State.Metadata)
	}
	if got.ID != "tool-call-1" || got.CallID != "call-1" {
		t.Fatalf("ids: %+v", got)
	}
}

func TestConvertPartToolResultError(t *testing.T) {
	got := ConvertPart("msg-1", "sess-1", message.ToolResult{
		ToolCallID: "call-1",
		Name:       "bash",
		Content:    "Permission denied",
		IsError:    true,
	})
	if got.State.Status != "error" {
		t.Fatalf("status: want error, got %s", got.State.Status)
	}
	if got.State.Error != "Permission denied" {
		t.Fatalf("error: %s", got.State.Error)
	}
	if got.State.Output != "" {
		t.Fatalf("output should be empty on error: %s", got.State.Output)
	}
}

func TestConvertPartUnknownPart(t *testing.T) {
	// Text/reasoning/file are not emitted today; ConvertPart returns a
	// stub envelope. Verifies the function stays total.
	got := ConvertPart("msg-1", "sess-1", message.TextContent{Text: "hi"})
	if got.MessageID != "msg-1" || got.SessionID != "sess-1" {
		t.Fatalf("envelope: %+v", got)
	}
	if got.Type != "" {
		t.Fatalf("expected empty Type for non-tool part, got %q", got.Type)
	}
}
