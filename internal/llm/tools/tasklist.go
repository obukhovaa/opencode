package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/task"
)

const TaskListToolName = "tasklist"

type TaskListParams struct {
	State string `json:"state,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type tasklistTool struct{}

func NewTaskListTool() BaseTool { return &tasklistTool{} }

func (t *tasklistTool) Info() ToolInfo {
	return ToolInfo{
		Name: TaskListToolName,
		Description: `List background tasks belonging to the current session (bash run_in_background, task async, monitor).

Each row shows task_id, kind, state, started_at, finished_at, exit_code, and a short description. Filter by state with the state parameter ("running" / "completed" / "failed" / "killed" / "all", default "all"). Limit results with limit (max 200, default 50).

This tool is for ONE-SHOT inventory queries — confirming a task is still running, listing your spawn fan-out, etc. Do NOT use it as a polling loop: completion notifications arrive automatically when a background task finishes. Each tool call costs tokens and invalidates the prompt cache; one-shot uses are cheap, polling is expensive.

Read the per-task output_file with the Read tool if you want to inspect a task's full log.`,
		Parameters: map[string]any{
			"state": map[string]any{
				"type":        "string",
				"description": "Optional state filter: running | completed | failed | killed | all (default all)",
				"enum":        []string{"running", "completed", "failed", "killed", "all"},
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum rows to return (default 50, max 200)",
			},
		},
	}
}

func (t *tasklistTool) AllowParallelism(ToolCall, []ToolCall) bool { return true }
func (t *tasklistTool) IsBaseline() bool                           { return false }

func (t *tasklistTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params TaskListParams
	if call.Input != "" && call.Input != "{}" {
		if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
			return NewTextErrorResponse(fmt.Sprintf("invalid parameters: %s", err)), nil
		}
	}
	limit := params.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	sessionID, _ := GetContextValues(ctx)
	if sessionID == "" {
		return NewEmptyResponse(), errors.New("session id is required")
	}
	reg := task.GlobalRegistry()
	if reg == nil {
		return NewTextErrorResponse("background tasks not available: task registry not initialized"), nil
	}
	all := reg.ListBySession(sessionID)
	// Sort newest-first.
	sort.Slice(all, func(i, j int) bool {
		return all[i].StartedAt.After(all[j].StartedAt)
	})
	wanted := strings.ToLower(strings.TrimSpace(params.State))
	if wanted == "" {
		wanted = "all"
	}
	rows := make([]*task.Task, 0, len(all))
	for _, tk := range all {
		if wanted != "all" && tk.State().String() != wanted {
			continue
		}
		rows = append(rows, tk)
		if len(rows) >= limit {
			break
		}
	}
	if len(rows) == 0 {
		return NewTextResponse("No background tasks for this session"), nil
	}
	var b strings.Builder
	for _, tk := range rows {
		b.WriteString(fmt.Sprintf(
			"%s\tkind=%s\tstate=%s\tstarted=%s",
			tk.ID, string(tk.Kind), tk.State().String(), tk.StartedAt.UTC().Format(time.RFC3339),
		))
		if fin := tk.FinishedAt(); tk.State() != task.StateRunning && !fin.IsZero() {
			b.WriteString(fmt.Sprintf("\tfinished=%s", fin.UTC().Format(time.RFC3339)))
		}
		if ec, ok := tk.ExitCode(); ok {
			b.WriteString(fmt.Sprintf("\texit=%d", ec))
		}
		if tk.Description != "" {
			b.WriteString(fmt.Sprintf("\tdesc=%q", tk.Description))
		}
		b.WriteString(fmt.Sprintf("\toutput=%s\n", tk.OutputPath))
	}
	return NewTextResponse(strings.TrimSuffix(b.String(), "\n")), nil
}
