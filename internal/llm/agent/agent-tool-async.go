package agent

import (
	"context"
	"fmt"
	"os"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/task"
)

// runAsync spawns the subagent in the background and returns an immediate
// ack ToolResult. A goroutine waits on the subagent's `done` channel; when
// it fires, cost is rolled up to the parent session and the final response
// content is injected as a synthetic Assistant(ToolCall name="task") +
// Tool(ToolResult) pair via task.EnqueueTaskCompletion.
func (b *agentTool) runAsync(
	ctx context.Context,
	call tools.ToolCall,
	params TaskParams,
	sessionID string,
	subagentType string,
	subagentInfo agentregistry.AgentInfo,
	taskSession session.Session,
	isResumed bool,
	a Service,
	prompt string,
) (tools.ToolResponse, error) {
	reg := task.GlobalRegistry()
	if reg == nil {
		return tools.NewTextErrorResponse("async tasks not available: task registry not initialized"), nil
	}

	taskID := task.NewTaskID(task.KindTask)
	outputPath, outputFile, err := reg.PrepareOutputFile(taskID)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("async task: prepare output file: %w", err)
	}

	// Run the subagent against its own background context so the parent's
	// turn ending (which cancels the parent ctx) does not cancel the
	// subagent. We retain a cancel function so taskstop can kill it.
	runCtx, cancel := context.WithCancel(context.Background())
	done, err := a.Run(runCtx, taskSession.ID, prompt, 0)
	if err != nil {
		cancel()
		_ = outputFile.Close()
		_ = os.Remove(outputPath)
		return tools.ToolResponse{}, fmt.Errorf("async task: start subagent: %w", err)
	}

	tk := &task.Task{
		ID:                    taskID,
		SessionID:             sessionID,
		Kind:                  task.KindTask,
		OutputPath:            outputPath,
		OriginatingToolCallID: call.ID,
		OriginatingToolName:   TaskToolName,
		Description:           params.TaskTitle,
		Cancel:                cancel,
	}
	if err := reg.Register(tk); err != nil {
		cancel()
		_ = outputFile.Close()
		_ = os.Remove(outputPath)
		return tools.ToolResponse{}, fmt.Errorf("async task: register task: %w", err)
	}

	syntheticInput := call.Input
	go b.waitAsyncAndNotify(done, outputFile, outputPath, sessionID, call.ID, taskID, taskSession.ID, syntheticInput)

	agentName := subagentType
	if subagentInfo.Name != "" {
		agentName = subagentInfo.Name
	}
	body := fmt.Sprintf(
		"Async subagent task started.\ntask_id: %s\ntask_session_id: %s\nsubagent: %s\noutput_file: %s\ntitle: %s\nresumed: %t\n\nThe subagent is running in the background. You will receive a synthetic tool result with the final response when it completes — do NOT poll. Use the `tasklist` tool for a one-shot inventory query and `taskstop` to cancel.",
		taskID, taskSession.ID, agentName, outputPath, params.TaskTitle, isResumed,
	)

	// Metadata mirrors the synchronous path so downstream consumers
	// (TUI, transcript exporter) see the same shape.
	return tools.WithResponseMetadata(
		tools.NewTextResponse(body),
		TaskResponseMetadata{
			TaskID:       taskSession.ID,
			SubagentType: subagentType,
			SubagentName: agentName,
			IsResumed:    isResumed,
		}), nil
}

func (b *agentTool) waitAsyncAndNotify(
	done <-chan AgentEvent,
	outputFile *os.File,
	outputPath, sessionID, callID, taskID, taskSessionID, syntheticInput string,
) {
	defer logging.RecoverPanic("agent.runAsync.wait", nil)
	result := <-done

	// Cost rollup runs FIRST (mirrors the synchronous path's resilience),
	// so a cancelled or errored subagent still attributes its incurred
	// spend to the parent.
	b.rollUpSubagentCost(context.Background(), sessionID, taskSessionID)

	var content string
	status := task.StatusCompleted
	if result.Error != nil {
		content = fmt.Sprintf("Async task error: %s", result.Error)
		status = task.StatusFailed
	} else {
		var isStructOutput bool
		content, isStructOutput = buildTaskResponseContent(result, taskSessionID)
		_ = isStructOutput
	}

	// If taskstop was used, the registry state was set to Killed before
	// the context cancellation reached us. Honor that by overriding status.
	if reg := task.GlobalRegistry(); reg != nil {
		if existing, ok := reg.Get(taskID); ok && existing.State() == task.StateKilled {
			status = task.StatusKilled
			if content == "" {
				content = "Async task killed by taskstop"
			}
		}
	}

	// Persist the final response to the output file so a Read tool call on
	// the path returns the same content (background-tasks spec requires
	// the file to be the canonical record).
	if _, err := outputFile.WriteString(content); err != nil {
		logging.Warn("async task: write output file", "task_id", taskID, "err", err)
	}
	_ = outputFile.Sync()
	_ = outputFile.Close()

	if err := task.EnqueueTaskCompletion(context.Background(), task.CompletionInput{
		SessionID:             sessionID,
		OriginatingToolCallID: callID,
		OriginatingToolName:   TaskToolName,
		TaskID:                taskID,
		Kind:                  task.KindTask,
		Status:                status,
		Input:                 syntheticInput,
		Content:               content,
		SuppressIfNotified:    true,
	}); err != nil {
		logging.Warn("async task: enqueue completion failed", "task_id", taskID, "err", err)
	}
}
