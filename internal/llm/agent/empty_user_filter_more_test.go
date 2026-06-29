package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/task"
)

// fakeMsgService implements message.Service minimally — just enough for
// injectWaitTimeoutNote to write a synthetic Assistant message.
type fakeMsgService struct {
	message.Service
	created []message.CreateMessageParams
}

func (f *fakeMsgService) Create(_ context.Context, _ string, params message.CreateMessageParams) (message.Message, error) {
	f.created = append(f.created, params)
	return message.Message{
		ID:        "fake-id",
		Role:      params.Role,
		Parts:     params.Parts,
		Synthetic: params.Synthetic,
	}, nil
}

// TestInjectWaitTimeoutNote_WritesSyntheticAssistantText pins the
// observable contract for the timeout note: Role=Assistant + a single
// TextContent + Synthetic=true, with the enumerated still-pending tasks
// in the body.
func TestInjectWaitTimeoutNote_WritesSyntheticAssistantText(t *testing.T) {
	msgs := &fakeMsgService{}
	a := &agent{messages: msgs}

	pending := []*task.Task{
		{
			ID:          "shell_ABC",
			SessionID:   "S1",
			Kind:        task.KindBash,
			StartedAt:   time.Date(2026, 6, 29, 0, 0, 0, 0, time.UTC),
			OutputPath:  "/tmp/.opencode/tasks/shell_ABC.out",
			Description: "wait for build",
		},
		{
			ID:         "monitor_XYZ",
			SessionID:  "S1",
			Kind:       task.KindMonitor,
			StartedAt:  time.Date(2026, 6, 29, 0, 0, 5, 0, time.UTC),
			OutputPath: "/tmp/.opencode/tasks/monitor_XYZ.out",
		},
	}

	a.injectWaitTimeoutNote(context.Background(), "S1", pending, context.DeadlineExceeded)

	if len(msgs.created) != 1 {
		t.Fatalf("want 1 message written, got %d", len(msgs.created))
	}
	got := msgs.created[0]
	if got.Role != message.Assistant {
		t.Errorf("Role: want Assistant, got %v", got.Role)
	}
	if !got.Synthetic {
		t.Errorf("Synthetic flag: want true, got false")
	}
	if len(got.Parts) != 1 {
		t.Fatalf("Parts: want 1, got %d", len(got.Parts))
	}
	text, ok := got.Parts[0].(message.TextContent)
	if !ok {
		t.Fatalf("Parts[0]: want TextContent, got %T", got.Parts[0])
	}
	body := text.Text
	for _, sub := range []string{
		"[wait-timeout]",
		"shell_ABC",
		"monitor_XYZ",
		"shell_ABC.out",
		"monitor_XYZ.out",
		"context deadline exceeded",
		"wait for build", // description preserved
	} {
		if !strings.Contains(body, sub) {
			t.Errorf("timeout note missing substring %q; body=%q", sub, body)
		}
	}
}
