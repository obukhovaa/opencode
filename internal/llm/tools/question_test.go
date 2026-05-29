package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/question"
)

type mockQuestionService struct {
	askFn func(ctx context.Context, sessionID string, questions []question.Prompt) ([][]string, error)
}

func (m *mockQuestionService) Ask(ctx context.Context, sessionID string, questions []question.Prompt) ([][]string, error) {
	return m.askFn(ctx, sessionID, questions)
}

func (m *mockQuestionService) Reply(_ string, _ [][]string) error { return nil }
func (m *mockQuestionService) Reject(_ string) error              { return nil }
func (m *mockQuestionService) List() []question.Request           { return nil }
func (m *mockQuestionService) Subscribe(_ context.Context) <-chan pubsub.Event[question.Request] {
	return nil
}

func TestQuestionToolInfo(t *testing.T) {
	svc := &mockQuestionService{askFn: func(_ context.Context, _ string, _ []question.Prompt) ([][]string, error) {
		t.Fatal("unexpected Ask call")
		return nil, nil
	}}
	tool := NewQuestionTool(svc, nil)

	info := tool.Info()
	if info.Name != QuestionToolName {
		t.Errorf("expected name %q, got %q", QuestionToolName, info.Name)
	}
	if info.Description == "" {
		t.Error("expected non-empty description")
	}
}

func TestQuestionToolRun(t *testing.T) {
	svc := &mockQuestionService{
		askFn: func(_ context.Context, sessionID string, questions []question.Prompt) ([][]string, error) {
			if sessionID != "test-session" {
				t.Errorf("expected session test-session, got %s", sessionID)
			}
			if len(questions) != 1 {
				t.Errorf("expected 1 question, got %d", len(questions))
			}
			return [][]string{{"Option A"}}, nil
		},
	}
	tool := NewQuestionTool(svc, nil)

	input, _ := json.Marshal(questionParams{
		Questions: []question.Prompt{
			{
				Question: "Pick one",
				Options: []question.Option{
					{Label: "Option A", Description: "First option"},
					{Label: "Option B", Description: "Second option"},
				},
			},
		},
	})

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	resp, err := tool.Run(ctx, ToolCall{ID: "1", Name: QuestionToolName, Input: string(input)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("expected success, got error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "Option A") {
		t.Errorf("expected response to contain 'Option A', got: %s", resp.Content)
	}
}

func TestQuestionToolRunRejected(t *testing.T) {
	svc := &mockQuestionService{
		askFn: func(_ context.Context, _ string, _ []question.Prompt) ([][]string, error) {
			return nil, question.ErrQuestionRejected
		},
	}
	tool := NewQuestionTool(svc, nil)

	input, _ := json.Marshal(questionParams{
		Questions: []question.Prompt{
			{
				Question: "Pick one",
				Options:  []question.Option{{Label: "A", Description: "A"}},
			},
		},
	})

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	resp, err := tool.Run(ctx, ToolCall{ID: "1", Name: QuestionToolName, Input: string(input)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("expected error response for rejected question")
	}
	if !strings.Contains(resp.Content, "dismissed") {
		t.Errorf("expected dismissed message, got: %s", resp.Content)
	}
}

func TestQuestionToolRunEmptyQuestions(t *testing.T) {
	svc := &mockQuestionService{askFn: func(_ context.Context, _ string, _ []question.Prompt) ([][]string, error) {
		t.Fatal("unexpected Ask call")
		return nil, nil
	}}
	tool := NewQuestionTool(svc, nil)

	input, _ := json.Marshal(questionParams{Questions: []question.Prompt{}})

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	resp, err := tool.Run(ctx, ToolCall{ID: "1", Name: QuestionToolName, Input: string(input)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("expected error for empty questions")
	}
}

func TestQuestionToolRunNoSession(t *testing.T) {
	svc := &mockQuestionService{askFn: func(_ context.Context, _ string, _ []question.Prompt) ([][]string, error) {
		t.Fatal("unexpected Ask call")
		return nil, nil
	}}
	tool := NewQuestionTool(svc, nil)

	input, _ := json.Marshal(questionParams{
		Questions: []question.Prompt{
			{Question: "Q", Options: []question.Option{{Label: "A", Description: "A"}}},
		},
	})

	resp, err := tool.Run(context.Background(), ToolCall{ID: "1", Name: QuestionToolName, Input: string(input)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("expected error for no session")
	}
}

func TestQuestionToolParallelism(t *testing.T) {
	tool := NewQuestionTool(&mockQuestionService{}, nil)
	if tool.AllowParallelism(ToolCall{}, nil) {
		t.Error("question tool should not allow parallelism")
	}
}

func TestQuestionToolBaseline(t *testing.T) {
	tool := NewQuestionTool(&mockQuestionService{}, nil)
	if !tool.IsBaseline() {
		t.Error("question tool should be baseline")
	}
}

func TestQuestionToolRunNoOptionsNoCustom(t *testing.T) {
	svc := &mockQuestionService{askFn: func(_ context.Context, _ string, _ []question.Prompt) ([][]string, error) {
		t.Fatal("unexpected Ask call")
		return nil, nil
	}}
	tool := NewQuestionTool(svc, nil)

	customFalse := false
	input, _ := json.Marshal(questionParams{
		Questions: []question.Prompt{
			{
				Question: "Q",
				Options:  []question.Option{},
				Custom:   &customFalse,
			},
		},
	})

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	resp, err := tool.Run(ctx, ToolCall{ID: "1", Name: QuestionToolName, Input: string(input)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("expected error for no options and custom disabled")
	}
}

type mockPermissionService struct {
	autoApproved map[string]bool
}

func (m *mockPermissionService) Grant(_ permission.PermissionRequest)          {}
func (m *mockPermissionService) GrantPersistant(_ permission.PermissionRequest) {}
func (m *mockPermissionService) Deny(_ permission.PermissionRequest)            {}
func (m *mockPermissionService) Request(_ context.Context, _ permission.CreatePermissionRequest) bool {
	return false
}
func (m *mockPermissionService) AutoApproveSession(id string) {
	m.autoApproved[id] = true
}
func (m *mockPermissionService) RemoveAutoApproveSession(id string) {
	delete(m.autoApproved, id)
}
func (m *mockPermissionService) IsAutoApproveSession(id string) bool {
	return m.autoApproved[id]
}
func (m *mockPermissionService) Subscribe(_ context.Context) <-chan pubsub.Event[permission.PermissionRequest] {
	return nil
}

func TestQuestionToolAutoApprove(t *testing.T) {
	svc := &mockQuestionService{askFn: func(_ context.Context, _ string, _ []question.Prompt) ([][]string, error) {
		t.Fatal("Ask should not be called when auto-approve is active")
		return nil, nil
	}}
	perms := &mockPermissionService{autoApproved: map[string]bool{"test-session": true}}
	tool := NewQuestionTool(svc, perms)

	input, _ := json.Marshal(questionParams{
		Questions: []question.Prompt{
			{
				Question: "Pick one",
				Options: []question.Option{
					{Label: "First (Recommended)", Description: "The recommended option"},
					{Label: "Second", Description: "Another option"},
				},
			},
		},
	})

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	resp, err := tool.Run(ctx, ToolCall{ID: "1", Name: QuestionToolName, Input: string(input)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("expected success, got error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "First (Recommended)") {
		t.Errorf("expected auto-selected first option, got: %s", resp.Content)
	}
}
