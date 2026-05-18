package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/opencode-ai/opencode/internal/todo"
)

const TodoWriteToolName = "todowrite"

// TodoStore is the interface the todowrite tool requires.
type TodoStore interface {
	Set(sessionID string, items []todo.Item)
	Get(sessionID string) []todo.Item
}

type todoWriteTool struct {
	store TodoStore
}

type todoWriteParams struct {
	Todos []todo.Item `json:"todos"`
}

func NewTodoWriteTool(store TodoStore) BaseTool {
	return &todoWriteTool{store: store}
}

func (t *todoWriteTool) Info() ToolInfo {
	return ToolInfo{
		Name: TodoWriteToolName,
		Description: `Create and maintain a structured task list for the current coding session. Tracks progress, organizes multi-step work, and surfaces status to the user.

## When to use
Use proactively when:
- The task requires 3+ distinct steps or actions (not just 3 tool calls for a single conceptual step)
- The work is non-trivial and benefits from planning
- The user provides multiple tasks (numbered or comma-separated) or explicitly asks for a todo list
- New instructions arrive — capture them as todos
- You start a task — mark it in_progress (only one at a time) before working
- You finish a task — mark it completed and add any follow-ups discovered during the work

## When NOT to use
Skip when:
- The work is a single, straightforward task (or <3 trivial steps)
- The request is purely informational or conversational
- Tracking adds no organizational value

## Rules
- Each call replaces the entire list — always include all items
- Keep exactly one item in_progress at a time
- Mark completed only after the required work is actually done. Never based on intent.
- Items should be specific and actionable; break large work into smaller steps`,
		Parameters: map[string]any{
			"todos": map[string]any{
				"type":        "array",
				"description": "The complete, updated todo list. Each call replaces the entire list.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"content": map[string]any{
							"type":        "string",
							"description": "Brief task description",
						},
						"status": map[string]any{
							"type":        "string",
							"description": "Task status",
							"enum":        []string{"pending", "in_progress", "completed", "cancelled"},
						},
						"priority": map[string]any{
							"type":        "string",
							"description": "Task priority",
							"enum":        []string{"high", "medium", "low"},
						},
					},
					"required": []string{"content", "status", "priority"},
				},
			},
		},
		Required: []string{"todos"},
	}
}

var validStatuses = map[string]bool{
	"pending":     true,
	"in_progress": true,
	"completed":   true,
	"cancelled":   true,
}

var validPriorities = map[string]bool{
	"high":   true,
	"medium": true,
	"low":    true,
}

func (t *todoWriteTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params todoWriteParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}

	sessionID, _ := GetContextValues(ctx)
	if sessionID == "" {
		return NewTextErrorResponse("session context required"), nil
	}

	if len(params.Todos) == 0 {
		t.store.Set(sessionID, nil)
		return NewTextResponse("No todos."), nil
	}

	// Normalize invalid status/priority values to defaults.
	for i := range params.Todos {
		if !validStatuses[params.Todos[i].Status] {
			params.Todos[i].Status = "pending"
		}
		if !validPriorities[params.Todos[i].Priority] {
			params.Todos[i].Priority = "medium"
		}
	}

	t.store.Set(sessionID, params.Todos)

	remaining := 0
	for _, item := range params.Todos {
		if item.Status != "completed" && item.Status != "cancelled" {
			remaining++
		}
	}

	resp := NewTextResponse(fmt.Sprintf("Todos updated. %d remaining. Continue with current tasks.", remaining))
	return WithResponseMetadata(resp, map[string]any{
		"todos": params.Todos,
	}), nil
}

func (t *todoWriteTool) AllowParallelism(_ ToolCall, _ []ToolCall) bool {
	return false
}

func (t *todoWriteTool) IsBaseline() bool { return true }
