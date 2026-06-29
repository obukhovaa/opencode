package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/task"
)

const TaskStopToolName = "taskstop"

type TaskStopParams struct {
	TaskID string `json:"task_id"`
}

type taskstopTool struct {
	permissions permission.Service
	registry    agentregistry.Registry
}

func NewTaskStopTool(perm permission.Service, reg agentregistry.Registry) BaseTool {
	return &taskstopTool{permissions: perm, registry: reg}
}

func (t *taskstopTool) Info() ToolInfo {
	return ToolInfo{
		Name: TaskStopToolName,
		Description: `Terminate a running background task (bash run_in_background, task async, monitor) by its task_id.

The kill is SYNCHRONOUS: this tool returns only after the SIGTERM (or context cancel for subagent tasks) has been sent, the underlying process has exited (with a 5s SIGTERM→SIGKILL escalation for stubborn subprocesses), AND the synthetic killed completion notification has been written into the session.

Use sparingly — most background tasks finish on their own. Common reasons to use taskstop:
- A monitor whose output is no longer useful
- An async task that's gone off-track and should be abandoned
- A bash subprocess that's taking longer than expected and isn't worth waiting for

Refuses to kill tasks from other sessions.`,
		Parameters: map[string]any{
			"task_id": map[string]any{
				"type":        "string",
				"description": "The task_id of the background task to terminate",
			},
		},
		Required: []string{"task_id"},
	}
}

func (t *taskstopTool) AllowParallelism(ToolCall, []ToolCall) bool { return false }
func (t *taskstopTool) IsBaseline() bool                           { return false }

func (t *taskstopTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params TaskStopParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("invalid parameters: %s", err)), nil
	}
	params.TaskID = strings.TrimSpace(params.TaskID)
	if params.TaskID == "" {
		return NewTextErrorResponse("task_id is required"), nil
	}

	sessionID, _ := GetContextValues(ctx)
	if sessionID == "" {
		return NewEmptyResponse(), errors.New("session id is required")
	}

	reg := task.GlobalRegistry()
	if reg == nil {
		return NewTextErrorResponse("background tasks not available: task registry not initialized"), nil
	}
	tk, ok := reg.Get(params.TaskID)
	if !ok {
		return NewTextErrorResponse(fmt.Sprintf("No task found with ID: %s", params.TaskID)), nil
	}
	if tk.SessionID != sessionID {
		return NewTextErrorResponse(fmt.Sprintf("Task %s does not belong to this session", params.TaskID)), nil
	}
	if tk.State() != task.StateRunning {
		return NewTextResponse(fmt.Sprintf("Task %s is not running (state: %s); no kill performed.", params.TaskID, tk.State())), nil
	}

	// Permission gate. Reuses the taskstop key. The agent's tool config is
	// the source of truth — default is "ask"; headless `permissionMode:
	// allow` covers automated kills.
	action := t.registry.EvaluatePermission(string(GetAgentID(ctx)), TaskStopToolName, params.TaskID)
	switch action {
	case permission.ActionAllow:
	case permission.ActionDeny:
		return NewEmptyResponse(), permission.ErrorPermissionDenied
	default:
		ok := t.permissions.Request(ctx, permission.CreatePermissionRequest{
			SessionID:   sessionID,
			ToolName:    TaskStopToolName,
			Action:      "kill",
			Description: fmt.Sprintf("Kill background task %s (%s: %s)", params.TaskID, tk.Kind, tk.Description),
			Params:      params,
		})
		if !ok {
			return NewEmptyResponse(), permission.ErrorPermissionDenied
		}
	}

	if err := reg.Kill(params.TaskID); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("kill failed: %v", err)), nil
	}

	// Wait synchronously (with escalation) for the task's terminal
	// notification to land. The originating tool's monitor goroutine
	// (bash/async/monitor) calls EnqueueTaskCompletion when its underlying
	// work observes the SIGTERM / cancel; we poll Notified.Load until
	// either it flips or we hit the 5s SIGKILL escalation window.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if tk.Notified.Load() {
			return NewTextResponse(fmt.Sprintf("Task %s killed.", params.TaskID)), nil
		}
		if time.Now().After(deadline) {
			// Escalate: SIGKILL the whole process group on POSIX, leaf-
			// only on Windows. Subagent tasks were already cancelled via
			// context; nothing to escalate there.
			if tk.Proc != nil {
				task.SignalProcessGroup(tk.Proc, task.KillSignal())
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// One more grace window for the completion to land after SIGKILL.
	deadline = time.Now().Add(2 * time.Second)
	for {
		if tk.Notified.Load() {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return NewTextResponse(fmt.Sprintf("Task %s killed (forced).", params.TaskID)), nil
}
