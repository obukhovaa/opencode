package cron

import (
	"context"

	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// TaskToolRunner wraps a tools.BaseTool (the task tool) to implement TaskRunner.
type TaskToolRunner struct {
	taskTool tools.BaseTool
}

func NewTaskToolRunner(taskTool tools.BaseTool) *TaskToolRunner {
	return &TaskToolRunner{taskTool: taskTool}
}

func (r *TaskToolRunner) RunTask(ctx context.Context, call tools.ToolCall) (tools.ToolResponse, error) {
	return r.taskTool.Run(ctx, call)
}

// AppBusyChecker wraps an App-like struct to check if any primary agent has a busy session
// and to acquire/release the session-busy slot for the cron scheduler's atomic
// synthetic-message commit.
type AppBusyChecker struct {
	checker func(sessionID string) bool
	tryLock func(sessionID string) bool
	unlock  func(sessionID string)
}

func NewAppBusyChecker(
	checker func(sessionID string) bool,
	tryLock func(sessionID string) bool,
	unlock func(sessionID string),
) *AppBusyChecker {
	return &AppBusyChecker{checker: checker, tryLock: tryLock, unlock: unlock}
}

func (c *AppBusyChecker) IsSessionBusy(sessionID string) bool {
	if c.checker == nil {
		return false
	}
	return c.checker(sessionID)
}

func (c *AppBusyChecker) TryLockSession(sessionID string) bool {
	if c.tryLock == nil {
		return true
	}
	return c.tryLock(sessionID)
}

func (c *AppBusyChecker) UnlockSession(sessionID string) {
	if c.unlock == nil {
		return
	}
	c.unlock(sessionID)
}

// AppActiveSessionProvider wraps a function that returns the active session ID.
type AppActiveSessionProvider struct {
	provider func() string
}

func NewAppActiveSessionProvider(provider func() string) *AppActiveSessionProvider {
	return &AppActiveSessionProvider{provider: provider}
}

func (p *AppActiveSessionProvider) ActiveSessionID() string {
	if p.provider == nil {
		return ""
	}
	return p.provider()
}
