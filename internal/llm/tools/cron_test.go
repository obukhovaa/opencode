package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// mockCronService implements CronToolService for testing.
type mockCronService struct {
	createFunc func(ctx context.Context, params CronCreateInput) (CronJobInfo, error)
	deleteFunc func(ctx context.Context, id string) error
	listFunc   func(ctx context.Context, sessionID string) ([]CronJobInfo, error)
}

func (m *mockCronService) Create(ctx context.Context, params CronCreateInput) (CronJobInfo, error) {
	if m.createFunc != nil {
		return m.createFunc(ctx, params)
	}
	return CronJobInfo{ID: "cron_test", NextRunAt: 1717776000}, nil
}

func (m *mockCronService) Delete(ctx context.Context, id string) error {
	if m.deleteFunc != nil {
		return m.deleteFunc(ctx, id)
	}
	return nil
}

func (m *mockCronService) List(ctx context.Context, sessionID string) ([]CronJobInfo, error) {
	if m.listFunc != nil {
		return m.listFunc(ctx, sessionID)
	}
	return nil, nil
}

type mockSchedHelper struct{}

func (m *mockSchedHelper) CronToHuman(expr string) string {
	return expr
}

func TestCronCreateToolValidation(t *testing.T) {
	svc := &mockCronService{}
	helper := &mockSchedHelper{}
	tool := NewCronCreateTool(svc, helper)

	tests := []struct {
		name    string
		input   map[string]any
		wantErr bool
	}{
		{
			name:    "missing schedule",
			input:   map[string]any{"prompt": "check build", "subagent_type": "explorer", "task_title": "check"},
			wantErr: true,
		},
		{
			name:    "missing prompt",
			input:   map[string]any{"schedule": "*/5 * * * *", "subagent_type": "explorer", "task_title": "check"},
			wantErr: true,
		},
		{
			name:    "missing subagent_type",
			input:   map[string]any{"schedule": "*/5 * * * *", "prompt": "check build", "task_title": "check"},
			wantErr: true,
		},
		{
			name:    "missing task_title",
			input:   map[string]any{"schedule": "*/5 * * * *", "prompt": "check build", "subagent_type": "explorer"},
			wantErr: true,
		},
		{
			name:    "valid params",
			input:   map[string]any{"schedule": "*/5 * * * *", "prompt": "check build", "subagent_type": "explorer", "task_title": "check build"},
			wantErr: false,
		},
		{
			name:    "valid with is_recurring false",
			input:   map[string]any{"schedule": "*/5 * * * *", "prompt": "check build", "subagent_type": "explorer", "task_title": "check", "is_recurring": false},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON, _ := json.Marshal(tt.input)
			ctx := context.WithValue(context.Background(), SessionIDContextKey, "sess_123")
			ctx = context.WithValue(ctx, MessageIDContextKey, "msg_123")

			resp, err := tool.Run(ctx, ToolCall{ID: "call_1", Input: string(inputJSON)})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr && !resp.IsError {
				t.Error("expected error response, got success")
			}
			if !tt.wantErr && resp.IsError {
				t.Errorf("expected success, got error: %s", resp.Content)
			}
		})
	}
}

func TestCronCreateToolServiceError(t *testing.T) {
	svc := &mockCronService{
		createFunc: func(_ context.Context, _ CronCreateInput) (CronJobInfo, error) {
			return CronJobInfo{}, fmt.Errorf("too many scheduled jobs (max 50). Cancel one first")
		},
	}
	helper := &mockSchedHelper{}
	tool := NewCronCreateTool(svc, helper)

	input := map[string]any{
		"schedule":      "*/5 * * * *",
		"prompt":        "check build",
		"subagent_type": "explorer",
		"task_title":    "check build",
	}
	inputJSON, _ := json.Marshal(input)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "sess_123")
	ctx = context.WithValue(ctx, MessageIDContextKey, "msg_123")

	resp, err := tool.Run(ctx, ToolCall{ID: "call_1", Input: string(inputJSON)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Error("expected error response for max jobs")
	}
}

func TestCronCreateToolNoSession(t *testing.T) {
	svc := &mockCronService{}
	helper := &mockSchedHelper{}
	tool := NewCronCreateTool(svc, helper)

	input := map[string]any{
		"schedule":      "*/5 * * * *",
		"prompt":        "check build",
		"subagent_type": "explorer",
		"task_title":    "check build",
	}
	inputJSON, _ := json.Marshal(input)

	// No session in context
	resp, err := tool.Run(context.Background(), ToolCall{ID: "call_1", Input: string(inputJSON)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Error("expected error response when no session context")
	}
}

func TestCronDeleteTool(t *testing.T) {
	deleted := false
	svc := &mockCronService{
		deleteFunc: func(_ context.Context, id string) error {
			if id == "cron_123" {
				deleted = true
				return nil
			}
			return fmt.Errorf("not found")
		},
	}
	tool := NewCronDeleteTool(svc)

	t.Run("valid delete", func(t *testing.T) {
		input := map[string]any{"id": "cron_123"}
		inputJSON, _ := json.Marshal(input)
		resp, err := tool.Run(context.Background(), ToolCall{ID: "call_1", Input: string(inputJSON)})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.IsError {
			t.Errorf("expected success, got error: %s", resp.Content)
		}
		if !deleted {
			t.Error("expected delete to be called")
		}
	})

	t.Run("missing id", func(t *testing.T) {
		input := map[string]any{}
		inputJSON, _ := json.Marshal(input)
		resp, err := tool.Run(context.Background(), ToolCall{ID: "call_2", Input: string(inputJSON)})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.IsError {
			t.Error("expected error for missing id")
		}
	})
}

func TestCronListTool(t *testing.T) {
	svc := &mockCronService{
		listFunc: func(_ context.Context, sessionID string) ([]CronJobInfo, error) {
			if sessionID == "sess_123" {
				return []CronJobInfo{
					{ID: "cron_1", Schedule: "*/5 * * * *", Status: "active", RunCount: 3, SubagentType: "explorer", Prompt: "check build"},
					{ID: "cron_2", Schedule: "0 9 * * *", Status: "done", RunCount: 1, SubagentType: "workhorse", Prompt: "run tests"},
				}, nil
			}
			return nil, nil
		},
	}
	helper := &mockSchedHelper{}
	tool := NewCronListTool(svc, helper)

	t.Run("with jobs", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), SessionIDContextKey, "sess_123")
		ctx = context.WithValue(ctx, MessageIDContextKey, "msg_123")
		resp, err := tool.Run(ctx, ToolCall{ID: "call_1", Input: "{}"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.IsError {
			t.Errorf("unexpected error: %s", resp.Content)
		}
		if len(resp.Content) == 0 {
			t.Error("expected non-empty response")
		}
	})

	t.Run("empty", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), SessionIDContextKey, "other_sess")
		ctx = context.WithValue(ctx, MessageIDContextKey, "msg_123")
		resp, err := tool.Run(ctx, ToolCall{ID: "call_2", Input: "{}"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.IsError {
			t.Errorf("unexpected error: %s", resp.Content)
		}
	})
}

func TestCronToolTruncateStr(t *testing.T) {
	tests := []struct {
		input  string
		max    int
		expect string
	}{
		{"short", 10, "short"},
		{"this is a long string", 10, "this is a ..."},
		{"exact10chr", 10, "exact10chr"},
	}
	for _, tt := range tests {
		got := truncateStr(tt.input, tt.max)
		if got != tt.expect {
			t.Errorf("truncateStr(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.expect)
		}
	}
}

func TestCronCreateToolTitleTruncation(t *testing.T) {
	svc := &mockCronService{
		createFunc: func(_ context.Context, params CronCreateInput) (CronJobInfo, error) {
			if len(params.TaskTitle) > 80 {
				t.Errorf("task title should be truncated to 80 chars, got %d", len(params.TaskTitle))
			}
			return CronJobInfo{ID: "cron_test", NextRunAt: 1717776000}, nil
		},
	}
	helper := &mockSchedHelper{}
	tool := NewCronCreateTool(svc, helper)

	longTitle := ""
	for i := 0; i < 100; i++ {
		longTitle += "x"
	}
	input := map[string]any{
		"schedule":      "*/5 * * * *",
		"prompt":        "check build",
		"subagent_type": "explorer",
		"task_title":    longTitle,
	}
	inputJSON, _ := json.Marshal(input)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "sess_123")
	ctx = context.WithValue(ctx, MessageIDContextKey, "msg_123")

	resp, err := tool.Run(ctx, ToolCall{ID: "call_1", Input: string(inputJSON)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Errorf("unexpected error: %s", resp.Content)
	}
}
