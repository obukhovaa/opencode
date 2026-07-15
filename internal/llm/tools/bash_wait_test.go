package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/task"
)

func TestIsPureWaitCommand(t *testing.T) {
	tests := []struct {
		cmd      string
		want     bool
		duration time.Duration
	}{
		// CD-4761 observed forms.
		{"sleep 300; echo done", true, 300 * time.Second},
		{"sleep 120; echo waited", true, 120 * time.Second},
		// Bare / spacing / operator variants.
		{"sleep 5", true, 5 * time.Second},
		{"  sleep 5  ", true, 5 * time.Second},
		{"sleep 0.5", true, 500 * time.Millisecond},
		{"sleep 5 && echo ok", true, 5 * time.Second},
		{"sleep 5;echo ok", true, 5 * time.Second},
		{"sleep 5; echo", true, 5 * time.Second},
		{"sleep 2m", true, 2 * time.Minute},
		{"sleep 1h", true, time.Hour},
		{`sleep 10; echo "batch probably done"`, true, 10 * time.Second},
		// NOT pure waits — must run normally.
		{"git status", false, 0},
		{"sleep 5; ls", false, 0},
		{"sleep 5; echo a; echo b", false, 0},
		{"sleep 5 | tee log", false, 0},
		{"sleep 5; echo done > out.txt", false, 0},
		{"sleep 5; echo $(date)", false, 0},
		{"sleep 5 & echo bg", false, 0},
		{"echo first; sleep 5", false, 0},
		{"sleep", false, 0},
		{"sleep abc", false, 0},
		{"sleeper 5", false, 0},
		{"go test ./... && sleep 5", false, 0},
		{"while true; do sleep 5; done", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got, d := isPureWaitCommand(tt.cmd)
			if got != tt.want {
				t.Fatalf("isPureWaitCommand(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
			if got && d != tt.duration {
				t.Errorf("isPureWaitCommand(%q) duration = %v, want %v", tt.cmd, d, tt.duration)
			}
		})
	}
}

// waitFixtureCtx builds a tool ctx like the agent does for a run.
func waitFixtureCtx(nonInteractive bool) context.Context {
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "SESS")
	ctx = context.WithValue(ctx, MessageIDContextKey, "MSG")
	ctx = context.WithValue(ctx, NonInteractiveContextKey, nonInteractive)
	return ctx
}

func registerRunningTask(t *testing.T, reg task.Registry, sessionID string, kind task.Kind) string {
	t.Helper()
	id := task.NewTaskID(kind)
	if err := reg.Register(&task.Task{
		ID:          id,
		SessionID:   sessionID,
		Kind:        kind,
		OutputPath:  "/tmp/" + id + ".out",
		Description: "fixture " + string(kind),
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	return id
}

// (a) Interception fires: non-interactive + pending non-monitor task +
// pure-wait command → wait returns after the task completes, no sleep runs.
func TestInterceptForegroundWait_FiresAndEnumeratesCompleted(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()

	taskID := registerRunningTask(t, reg, "SESS", task.KindTask)
	go func() {
		time.Sleep(150 * time.Millisecond)
		reg.MarkFinished(taskID, task.StateCompleted, nil)
	}()

	start := time.Now()
	resp, intercepted := interceptForegroundWait(waitFixtureCtx(true), "sleep 300; echo done", "SESS")
	elapsed := time.Since(start)

	if !intercepted {
		t.Fatal("expected interception; command would have slept 300s")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("wait took %v; expected prompt return after task completion", elapsed)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("wait returned in %v — did not actually wait for the task", elapsed)
	}
	if !strings.Contains(resp.Content, taskID) {
		t.Errorf("response should enumerate completed task %s; got:\n%s", taskID, resp.Content)
	}
	if !strings.Contains(resp.Content, "[non-interactive wait]") {
		t.Errorf("response missing interception note; got:\n%s", resp.Content)
	}
	if resp.IsError {
		t.Error("interception response must not be an error")
	}
}

// (b) Interactive mode: never intercepted, regardless of pending tasks.
func TestInterceptForegroundWait_PassthroughInteractive(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	registerRunningTask(t, reg, "SESS", task.KindBash)

	if _, intercepted := interceptForegroundWait(waitFixtureCtx(false), "sleep 5; echo done", "SESS"); intercepted {
		t.Fatal("interactive run must never be intercepted")
	}
}

// (c) No pending tasks: passes through.
func TestInterceptForegroundWait_PassthroughNoPending(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()

	if _, intercepted := interceptForegroundWait(waitFixtureCtx(true), "sleep 5; echo done", "SESS"); intercepted {
		t.Fatal("no pending tasks — must pass through")
	}
}

// (d) Non-pure command: passes through even with pending tasks.
func TestInterceptForegroundWait_PassthroughNonPureCommand(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	registerRunningTask(t, reg, "SESS", task.KindTask)

	if _, intercepted := interceptForegroundWait(waitFixtureCtx(true), "git status", "SESS"); intercepted {
		t.Fatal("non-pure command must pass through")
	}
}

// (e) Only monitors pending: passes through (monitors are excluded from the
// redirect — they are bounded by the end-of-turn drain, not a mid-turn sleep).
func TestInterceptForegroundWait_PassthroughOnlyMonitors(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	registerRunningTask(t, reg, "SESS", task.KindMonitor)

	if _, intercepted := interceptForegroundWait(waitFixtureCtx(true), "sleep 5", "SESS"); intercepted {
		t.Fatal("monitor-only pending set must pass through")
	}
}

// Cross-session isolation: pending tasks in another session don't trigger.
func TestInterceptForegroundWait_PassthroughOtherSession(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	registerRunningTask(t, reg, "OTHER", task.KindTask)

	if _, intercepted := interceptForegroundWait(waitFixtureCtx(true), "sleep 5", "SESS"); intercepted {
		t.Fatal("pending tasks in another session must not trigger interception")
	}
}

// (f) Ctx deadline elapses while a task is still running: returns the
// still-pending note instead of blocking forever.
func TestInterceptForegroundWait_CtxDeadlineReportsStillPending(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	hangID := registerRunningTask(t, reg, "SESS", task.KindTask)
	defer reg.MarkFinished(hangID, task.StateKilled, nil)

	ctx, cancel := context.WithTimeout(waitFixtureCtx(true), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	resp, intercepted := interceptForegroundWait(ctx, "sleep 300", "SESS")
	if !intercepted {
		t.Fatal("expected interception")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("deadline path took %v", elapsed)
	}
	if !strings.Contains(resp.Content, "Still pending") {
		t.Errorf("expected still-pending note; got:\n%s", resp.Content)
	}
	if !strings.Contains(resp.Content, hangID) {
		t.Errorf("still-pending list should include %s; got:\n%s", hangID, resp.Content)
	}
}

// allowAllPerms is a permission.Service stub whose interactive Request
// always approves — lets the full bash.Run path pass the permission gate.
type allowAllPerms struct{ mockPermissionService }

func (a *allowAllPerms) Request(_ context.Context, _ permission.CreatePermissionRequest) bool {
	return true
}

// Full bash-tool path: Run() returns the interception response without
// executing the sleep (elapsed guard) in non-interactive mode.
func TestBashRun_InterceptsSleepEndToEnd(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(wd, false); err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	taskID := registerRunningTask(t, reg, "SESS", task.KindBash)
	go func() {
		time.Sleep(100 * time.Millisecond)
		reg.MarkFinished(taskID, task.StateCompleted, nil)
	}()

	bash := NewBashTool(&allowAllPerms{}, agentregistry.GetRegistry())
	start := time.Now()
	resp, err := bash.Run(waitFixtureCtx(true), ToolCall{
		ID:    "call-1",
		Input: `{"command":"sleep 60; echo done","description":"wait"}`,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Run took %v — sleep was executed instead of intercepted", elapsed)
	}
	if !strings.Contains(resp.Content, "[non-interactive wait]") {
		t.Errorf("expected interception content; got:\n%s", resp.Content)
	}
	if strings.Contains(resp.Content, "\ndone") {
		t.Errorf("echo must not have executed; got:\n%s", resp.Content)
	}
}
