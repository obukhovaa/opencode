package tools

import (
	"context"
	"encoding/json"

	"github.com/opencode-ai/opencode/internal/task"
)

const YieldToolName = "yield"

type yieldTool struct{}

func NewYieldTool() BaseTool { return &yieldTool{} }

func (t *yieldTool) Info() ToolInfo {
	return ToolInfo{
		Name: YieldToolName,
		Description: `Wait for a background task or monitor to report something when you have no independent work left. Call yield alone, with no other tool calls. The runtime pauses model generation and resumes the session with the next event or queued user message; no polling or foreground sleep is needed. Returns immediately if a report is already waiting or no background tasks are running.

In flow/headless runs, the runtime waits inside this run. In interactive chat/TUI sessions, it releases the turn so user messages can be handled; reports resume the session automatically.

Yielding keeps the task open: it does not claim success, emit structured output, cancel tasks, or require the user to continue. After a report, act on it; yield again only while work is still pending. Read completion reports already in context before choosing to yield; completed work needs action, not another wait or tasklist check. Continue useful independent work before yielding. Use taskstop only when a watch is no longer needed, never merely because it is silent.

Yield waits only for this session's registered background bash, async task, or monitor. A remote CI job needs a background waiter that exits on success or failure; starting a remote job alone gives yield nothing to wait for. Use question to request an answer from a reviewer.`,
		Parameters: map[string]any{},
	}
}

func (t *yieldTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params map[string]json.RawMessage
	if call.Input != "" {
		if err := json.Unmarshal([]byte(call.Input), &params); err != nil || params == nil || len(params) != 0 {
			return NewTextErrorResponse("yield takes an empty object: {}"), nil
		}
	}
	if reg := task.GlobalRegistry(); reg != nil {
		sessionID, _ := GetContextValues(ctx)
		if sessionID != "" && len(reg.PendingForSession(sessionID, nil)) == 0 {
			return NewTextResponse("No active background tasks remain for this session. Process completion reports delivered with this result and continue the task. Do not yield again or call tasklist merely to confirm completed work. Use question if you need a reviewer answer."), nil
		}
	}
	// The agent loop handles the wait after persisting this result, outside
	// tool execution. No watchdog or parallel-tool context bounds the wait.
	return NewTextResponse("Yielding for background reports or user input. Flow/headless runs wait here; interactive sessions resume automatically when a report arrives. If nothing is running, continue immediately."), nil
}

func (t *yieldTool) AllowParallelism(ToolCall, []ToolCall) bool { return false }
func (t *yieldTool) IsBaseline() bool                           { return false }
