package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/task"
)

func TestYieldReportsNoPendingWork(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	// A sibling session's work must not make this session appear busy.
	if err := reg.Register(&task.Task{ID: "other", SessionID: "other", Kind: task.KindTask}); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "current")
	resp, err := NewYieldTool().Run(ctx, ToolCall{Input: `{}`})
	if err != nil || resp.IsError || !strings.Contains(resp.Content, "No active background tasks") {
		t.Fatalf("idle yield: %+v, %v", resp, err)
	}
	if err := reg.Register(&task.Task{ID: "own", SessionID: "current", Kind: task.KindTask}); err != nil {
		t.Fatal(err)
	}
	resp, err = NewYieldTool().Run(ctx, ToolCall{Input: `{}`})
	if err != nil || resp.IsError || strings.Contains(resp.Content, "No active background tasks") {
		t.Fatalf("pending yield: %+v, %v", resp, err)
	}
}
