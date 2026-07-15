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

// subagentBaseContext returns the base context for a detached async
// subagent run: the step-scoped ctx installed by the flow runner when
// present (see tools.StepScopedContextKey — survives single turns, dies
// with the step), else context.Background(). Interactive callers never
// install the value, so their async subagents keep today's unbounded
// lifetime.
func subagentBaseContext(ctx context.Context) context.Context {
	if base := tools.StepScopedContext(ctx); base != nil {
		return base
	}
	return context.Background()
}

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

	// Run the subagent against a context detached from the parent's
	// per-turn ctx so the parent's turn ending does not cancel the
	// subagent. The base is the step-scoped ctx when the caller installed
	// one (flow steps — bounded by Step.Timeout / the env default, and
	// cancelled when the step completes), else context.Background()
	// (interactive callers — unchanged). We retain a cancel function so
	// taskstop can kill it. Re-installing the step-scope value on runCtx
	// lets nested async spawns inherit the same step bound.
	runCtx, cancel := context.WithCancel(subagentBaseContext(ctx))
	if stepScope := tools.StepScopedContext(ctx); stepScope != nil {
		runCtx = context.WithValue(runCtx, tools.StepScopedContextKey, stepScope)
	}
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
		"Async subagent task started.\ntask_id: %s\ntask_session_id: %s\nsubagent: %s\noutput_file: %s\ntitle: %s\nresumed: %t\n\nThe subagent is running in the background. A synthetic tool result with its final response will arrive automatically when it completes — do NOT poll and do NOT sleep while waiting. In a non-interactive (flow) step the runtime holds the turn open until the subagent reaches a terminal state, so sleeping cannot observe progress sooner. The output_file receives the final response only AFTER completion (it is not a progress log). To continue this subagent later, call the task tool again with the same task_id to reattach to its session. Use `tasklist` for a one-shot inventory query and `taskstop` to cancel.",
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
