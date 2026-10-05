package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/task"
)

func TestSplitLeadingWait(t *testing.T) {
	tests := []struct {
		cmd      string
		want     bool
		duration time.Duration
		trailer  string
	}{
		// CD-4761 observed forms.
		{"sleep 300; echo done", true, 300 * time.Second, "echo done"},
		{"sleep 120; echo waited", true, 120 * time.Second, "echo waited"},
		// Bare / spacing / operator variants.
		{"sleep 5", true, 5 * time.Second, ""},
		{"  sleep 5  ", true, 5 * time.Second, ""},
		{"sleep 0.5", true, 500 * time.Millisecond, ""},
		{"sleep 5 && echo ok", true, 5 * time.Second, "echo ok"},
		{"sleep 5;echo ok", true, 5 * time.Second, "echo ok"},
		{"sleep 5; echo", true, 5 * time.Second, "echo"},
		{"sleep 5;", true, 5 * time.Second, ""},
		{"sleep 2m", true, 2 * time.Minute, ""},
		{"sleep 1h", true, time.Hour, ""},
		{`sleep 10; echo "batch probably done"`, true, 10 * time.Second, `echo "batch probably done"`},
		// The traced shape (background-wait-integrity): a probe trailer with
		// pipes, redirects and several commands is split, not rejected.
		{"sleep 120; grep -nE 'BUILD SUCCESSFUL|FAILED' /tmp/log | head -20; echo '---tail---'; tail -5 /tmp/log",
			true, 120 * time.Second, "grep -nE 'BUILD SUCCESSFUL|FAILED' /tmp/log | head -20; echo '---tail---'; tail -5 /tmp/log"},
		{"sleep 5; ls", true, 5 * time.Second, "ls"},
		{"sleep 5; echo a; echo b", true, 5 * time.Second, "echo a; echo b"},
		{"sleep 5 | tee log", false, 0, ""},
		{"sleep 5; echo done > out.txt", true, 5 * time.Second, "echo done > out.txt"},
		{"sleep 5; echo $(date)", true, 5 * time.Second, "echo $(date)"},
		// `&` is neither separator: this backgrounds the sleep, it does not wait.
		{"sleep 5 & echo bg", false, 0, ""},
		// Only a LEADING wait qualifies.
		{"echo first; sleep 5", false, 0, ""},
		{"git status", false, 0, ""},
		{"sleep", false, 0, ""},
		{"sleep abc", false, 0, ""},
		{"sleeper 5", false, 0, ""},
		{"go test ./... && sleep 5", false, 0, ""},
		{"while true; do sleep 5; done", false, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			d, trailer, got := splitLeadingWait(tt.cmd)
			if got != tt.want {
				t.Fatalf("splitLeadingWait(%q) ok = %v, want %v", tt.cmd, got, tt.want)
			}
			if !got {
				return
			}
			if d != tt.duration {
				t.Errorf("duration = %v, want %v", d, tt.duration)
			}
			if trailer != tt.trailer {
				t.Errorf("trailer = %q, want %q", trailer, tt.trailer)
			}
		})
	}
}

// A trailer that is itself a wait must not smuggle the sleep back in.
func TestStripLeadingWaits(t *testing.T) {
	cases := map[string]string{
		"":                               "",
		"sleep 300":                      "",
		"sleep 0; sleep 300":             "",
		"sleep 0; sleep 300; echo x":     "echo x",
		"sleep 1 && sleep 2 && cat /tmp": "cat /tmp",
		"echo x; sleep 5":                "echo x; sleep 5",
		"grep a b | head":                "grep a b | head",
	}
	for in, want := range cases {
		if got := stripLeadingWaits(in); got != want {
			t.Errorf("stripLeadingWaits(%q) = %q, want %q", in, got, want)
		}
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
	resp, intercepted := interceptForegroundWait(waitFixtureCtx(true), BashParams{Command: "sleep 300; echo done"}, t.TempDir(), "SESS")
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
	// The `echo done` trailer ran AFTER the wait, through the real shell.
	note := strings.Index(resp.Content, "[non-interactive wait]")
	out := strings.Index(resp.Content, "run after the wait")
	if out < 0 || out < note || !strings.Contains(resp.Content[out:], "done") {
		t.Errorf("trailer output must follow the interception note; got:\n%s", resp.Content)
	}
}

// (b) Interactive mode: never intercepted, regardless of pending tasks.
func TestInterceptForegroundWait_PassthroughInteractive(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	registerRunningTask(t, reg, "SESS", task.KindBash)

	if _, intercepted := interceptForegroundWait(waitFixtureCtx(false), BashParams{Command: "sleep 5; echo done"}, t.TempDir(), "SESS"); intercepted {
		t.Fatal("interactive run must never be intercepted")
	}
}

// (c) No pending tasks: passes through.
func TestInterceptForegroundWait_PassthroughNoPending(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()

	if _, intercepted := interceptForegroundWait(waitFixtureCtx(true), BashParams{Command: "sleep 5; echo done"}, t.TempDir(), "SESS"); intercepted {
		t.Fatal("no pending tasks — must pass through")
	}
}

// (d) Non-pure command: passes through even with pending tasks.
func TestInterceptForegroundWait_PassthroughNonPureCommand(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	registerRunningTask(t, reg, "SESS", task.KindTask)

	if _, intercepted := interceptForegroundWait(waitFixtureCtx(true), BashParams{Command: "git status"}, t.TempDir(), "SESS"); intercepted {
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

	if _, intercepted := interceptForegroundWait(waitFixtureCtx(true), BashParams{Command: "sleep 5"}, t.TempDir(), "SESS"); intercepted {
		t.Fatal("monitor-only pending set must pass through")
	}
}

// Cross-session isolation: pending tasks in another session don't trigger.
func TestInterceptForegroundWait_PassthroughOtherSession(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	registerRunningTask(t, reg, "OTHER", task.KindTask)

	if _, intercepted := interceptForegroundWait(waitFixtureCtx(true), BashParams{Command: "sleep 5"}, t.TempDir(), "SESS"); intercepted {
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
	resp, intercepted := interceptForegroundWait(ctx, BashParams{Command: "sleep 300"}, t.TempDir(), "SESS")
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
	// background-wait-integrity: the trailer is split from the wait and run
	// afterwards, so its output is part of the result (the previous contract
	// dropped it, which cost the agent its probe).
	if !strings.Contains(resp.Content, "run after the wait") || !strings.Contains(resp.Content, "done") {
		t.Errorf("trailer must have executed after the wait; got:\n%s", resp.Content)
	}
	var meta BashResponseMetadata
	if err := json.Unmarshal([]byte(resp.Metadata), &meta); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if meta.ExitCode != 0 {
		t.Errorf("trailer exit code = %d, want 0", meta.ExitCode)
	}
}

// registerChildTask registers a running task owned by childSession whose
// parent is parentSession — the shape a subagent's bash task has.
func registerChildTask(t *testing.T, reg task.Registry, childSession, parentSession string, kind task.Kind) string {
	t.Helper()
	id := task.NewTaskID(kind)
	if err := reg.Register(&task.Task{
		ID:              id,
		SessionID:       childSession,
		ParentSessionID: parentSession,
		Kind:            kind,
		OutputPath:      "/tmp/" + id + ".out",
		Description:     "child fixture",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	return id
}

// The trailer runs after the wait with its real exit code in the metadata.
func TestInterceptForegroundWait_TrailerRunsAfterWaitWithExitCode(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	taskID := registerRunningTask(t, reg, "SESS", task.KindBash)
	go func() {
		time.Sleep(120 * time.Millisecond)
		reg.MarkFinished(taskID, task.StateCompleted, nil)
	}()
	resp, intercepted := interceptForegroundWait(waitFixtureCtx(true), BashParams{Command: "sleep 600; echo after-wait; exit 3"}, t.TempDir(), "SESS")
	if !intercepted {
		t.Fatal("expected interception")
	}
	if !strings.Contains(resp.Content, "after-wait") || !strings.Contains(resp.Content, "Exit code 3") {
		t.Errorf("trailer output/exit code missing; got:\n%s", resp.Content)
	}
	var meta BashResponseMetadata
	if err := json.Unmarshal([]byte(resp.Metadata), &meta); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if meta.ExitCode != 3 {
		t.Errorf("metadata exit code = %d, want 3 (a failing trailer must not report success)", meta.ExitCode)
	}
}

// `sleep 0; sleep 300` must not bypass the guard through its trailer.
func TestInterceptForegroundWait_TrailerCannotSmuggleASleep(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	taskID := registerRunningTask(t, reg, "SESS", task.KindBash)
	go func() {
		time.Sleep(100 * time.Millisecond)
		reg.MarkFinished(taskID, task.StateCompleted, nil)
	}()
	start := time.Now()
	resp, intercepted := interceptForegroundWait(waitFixtureCtx(true), BashParams{Command: "sleep 0; sleep 300; echo tail"}, t.TempDir(), "SESS")
	if !intercepted {
		t.Fatal("expected interception")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %v: the trailer's sleep ran", elapsed)
	}
	if !strings.Contains(resp.Content, "tail") {
		t.Errorf("the non-wait remainder of the trailer should still run; got:\n%s", resp.Content)
	}
}

// A wait that ends on a cancelled ctx runs no trailer.
func TestInterceptForegroundWait_CancelledWaitSkipsTrailer(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	hangID := registerRunningTask(t, reg, "SESS", task.KindTask)
	defer reg.MarkFinished(hangID, task.StateKilled, nil)

	marker := filepath.Join(t.TempDir(), "ran")
	ctx, cancel := context.WithTimeout(waitFixtureCtx(true), 150*time.Millisecond)
	defer cancel()
	resp, intercepted := interceptForegroundWait(ctx, BashParams{Command: "sleep 600; touch " + marker}, t.TempDir(), "SESS")
	if !intercepted {
		t.Fatal("expected interception")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("trailer ran although the wait ended on a cancelled context")
	}
	if !strings.Contains(resp.Content, "Still pending") || !strings.Contains(resp.Content, "was NOT run") {
		t.Errorf("expected the deadline note and the skipped-trailer note; got:\n%s", resp.Content)
	}
}

// `&` is not a trailer separator: the command backgrounds the sleep.
func TestInterceptForegroundWait_AmpersandRunsVerbatim(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	registerRunningTask(t, task.GlobalRegistry(), "SESS", task.KindTask)
	if _, intercepted := interceptForegroundWait(waitFixtureCtx(true), BashParams{Command: "sleep 5 & echo bg"}, t.TempDir(), "SESS"); intercepted {
		t.Fatal("`sleep 5 & echo bg` must not be intercepted")
	}
}

// Scope: a parent's sleep is redirected onto its direct child's task, and
// the note points at output_file rather than promising a completion here.
func TestInterceptForegroundWait_ParentWaitsOnChildTask(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	childTask := registerChildTask(t, reg, "CHILD", "SESS", task.KindBash)
	go func() {
		time.Sleep(150 * time.Millisecond)
		reg.MarkFinished(childTask, task.StateCompleted, nil)
	}()
	start := time.Now()
	resp, intercepted := interceptForegroundWait(waitFixtureCtx(true), BashParams{Command: "sleep 120"}, t.TempDir(), "SESS")
	if !intercepted {
		t.Fatal("parent must be intercepted for a direct child's pending task")
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("wait took %v; the pre-check and the wait must share the children scope", elapsed)
	}
	if !strings.Contains(resp.Content, childTask) || !strings.Contains(resp.Content, "owner=CHILD") {
		t.Errorf("child task must be listed with its owner; got:\n%s", resp.Content)
	}
	if !strings.Contains(resp.Content, "read output_file") {
		t.Errorf("child-owned completion note must point at output_file; got:\n%s", resp.Content)
	}
}

// Scope: parallel sibling steps (same flow root, different parents) do not
// count, and so does not a grandchild.
func TestInterceptForegroundWait_SiblingsAndGrandchildrenExcluded(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	registerChildTask(t, reg, "STEP-B", "FLOW-ROOT", task.KindBash) // sibling branch of the caller STEP-A
	registerChildTask(t, reg, "GRANDCHILD", "CHILD", task.KindBash) // two levels below SESS
	for _, caller := range []string{"STEP-A", "SESS"} {
		ctx := context.WithValue(waitFixtureCtx(true), SessionIDContextKey, caller)
		if _, intercepted := interceptForegroundWait(ctx, BashParams{Command: "sleep 5"}, t.TempDir(), caller); intercepted {
			t.Errorf("caller %s must not be intercepted by a sibling's or grandchild's task", caller)
		}
	}
}
