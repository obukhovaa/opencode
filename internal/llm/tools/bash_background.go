package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/task"
)

// runBackground is the bash tool's `run_in_background: true` branch.
// It spawns the command as a detached subprocess that writes to a per-task
// output file under `<data.dir>/tasks/<task_id>.out`, registers the task in
// the global registry, and returns an immediate ack ToolResult. A monitor
// goroutine waits on cmd.Wait and fires the terminal synthetic completion
// notification via task.EnqueueTaskCompletion when the process exits.
func (b *bashTool) runBackground(_ context.Context, call ToolCall, params BashParams, workdir, sessionID string) (ToolResponse, error) {
	reg := task.GlobalRegistry()
	if reg == nil {
		return NewTextErrorResponse("background tasks not available: task registry not initialized"), nil
	}

	taskID := task.NewTaskID(task.KindBash)
	outputPath, outputFile, err := reg.PrepareOutputFile(taskID)
	if err != nil {
		return NewEmptyResponse(), fmt.Errorf("background bash: prepare output file: %w", err)
	}

	cmd := exec.Command("bash", "-c", params.Command)
	cmd.Dir = workdir
	cmd.Stdout = outputFile
	cmd.Stderr = outputFile
	// The leaf bash becomes its own process-group leader so a later
	// taskstop can SIGTERM the whole descendant tree (e.g. when the
	// bash wraps `make test` and we want the spawned compilers + the
	// shell to die together). No-op on Windows.
	task.SetProcessGroupAttr(cmd)

	if err := cmd.Start(); err != nil {
		_ = outputFile.Close()
		_ = os.Remove(outputPath)
		return NewTextErrorResponse(fmt.Sprintf("failed to start background command: %v", err)), nil
	}

	tk := &task.Task{
		ID:                    taskID,
		SessionID:             sessionID,
		Kind:                  task.KindBash,
		OutputPath:            outputPath,
		OriginatingToolCallID: call.ID,
		OriginatingToolName:   BashToolName,
		Description:           params.Description,
		Proc:                  cmd.Process,
	}
	if err := reg.Register(tk); err != nil {
		// Registration race — kill, drain, and surface a synchronous error.
		_ = cmd.Process.Kill()
		_ = outputFile.Close()
		_ = os.Remove(outputPath)
		return NewEmptyResponse(), fmt.Errorf("background bash: register task: %w", err)
	}

	syntheticInput := buildSyntheticBashInput(params)
	// Detach from the caller's ctx — when the agent's turn ends, ctx is
	// cancelled, but the subprocess must keep running. We use context.Background
	// for the EnqueueTaskCompletion call as well.
	go bashWaitAndNotify(cmd, outputFile, outputPath, sessionID, call.ID, taskID, syntheticInput, params)

	notice := ""
	if params.Timeout != 0 && params.Timeout != DefaultTimeout {
		notice = "\n(timeout parameter is ignored in background mode)"
	}
	body := fmt.Sprintf(
		"Background task started.\ntask_id: %s\noutput_file: %s\ncommand: %s%s\n\nThe task is running. A synthetic tool result with the final output will arrive automatically when it completes — do NOT poll and do NOT sleep while waiting. In a non-interactive (flow) step the runtime holds the turn open until the task reaches a terminal state, so sleeping cannot observe progress sooner. The output_file holds the full output once the task finishes. Use `tasklist` for a one-shot inventory query and `taskstop` to kill.",
		taskID,
		outputPath,
		truncateCommand(params.Command),
		notice,
	)
	return NewTextResponse(body), nil
}

// bashWaitAndNotify is the per-task monitor goroutine. It runs cmd.Wait,
// flushes the output file, reads its content (truncated to the synchronous
// bash output budget), and calls EnqueueTaskCompletion with the resulting
// status and content. The function honors the registry's `notified` dedupe
// flag indirectly via SuppressIfNotified=true.
func bashWaitAndNotify(cmd *exec.Cmd, outputFile *os.File, outputPath, sessionID, callID, taskID, syntheticInput string, _ BashParams) {
	defer logging.RecoverPanic("bash.runBackground.wait", nil)
	err := cmd.Wait()
	_ = outputFile.Sync()
	_ = outputFile.Close()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	raw, _ := os.ReadFile(outputPath)
	out := persistAndTruncate(string(raw), "stdout", BashToolName)
	content := out.content
	if exitCode != 0 {
		if content != "" {
			content += "\n"
		}
		content += fmt.Sprintf("Exit code %d", exitCode)
	}
	if content == "" {
		content = "no output"
	}

	status := task.StatusCompleted
	if exitCode != 0 {
		status = task.StatusFailed
	}

	// Skip the EnqueueTaskCompletion call if the task was killed via taskstop
	// — that path fires its own StatusKilled completion. We detect the kill
	// by checking the registry state set up-front by Kill.
	if reg := task.GlobalRegistry(); reg != nil {
		if existing, ok := reg.Get(taskID); ok && existing.State() == task.StateKilled {
			status = task.StatusKilled
		}
	}

	ec := exitCode
	if err := task.EnqueueTaskCompletion(context.Background(), task.CompletionInput{
		SessionID:             sessionID,
		OriginatingToolCallID: callID,
		OriginatingToolName:   BashToolName,
		TaskID:                taskID,
		Kind:                  task.KindBash,
		Status:                status,
		ExitCode:              &ec,
		Input:                 syntheticInput,
		Content:               content,
		SuppressIfNotified:    true,
	}); err != nil {
		logging.Warn("bash background: enqueue completion failed", "task_id", taskID, "err", err)
	}
}

// buildSyntheticBashInput marshals the original BashParams MINUS the
// run_in_background flag, so the synthetic ToolCall's Input renders like a
// synchronous bash call.
func buildSyntheticBashInput(p BashParams) string {
	stripped := struct {
		Command     string `json:"command"`
		Workdir     string `json:"workdir,omitempty"`
		Description string `json:"description,omitempty"`
	}{
		Command:     p.Command,
		Workdir:     p.Workdir,
		Description: p.Description,
	}
	b, _ := json.Marshal(stripped)
	return string(b)
}

// truncateCommand caps a command string to ~200 chars for the ack message.
func truncateCommand(c string) string {
	c = strings.TrimSpace(c)
	if len(c) > 200 {
		return c[:200] + "…"
	}
	return c
}
