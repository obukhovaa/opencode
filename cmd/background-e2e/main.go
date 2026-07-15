// Command background-e2e is a black-box driver that exercises the
// background-tasks subsystem end-to-end against a sandboxed data
// directory and an in-memory SQLite session. It mirrors the pattern of
// cmd/hooks-e2e: a small Go entry point invoked from a shell test that
// produces deterministic JSON output for the script to inspect.
//
// What it exercises:
//
//  1. task.NewRegistry against `<sandbox>/.opencode/tasks/`
//  2. task.SetDeps with a synchronous Messages-backed adapter that
//     mirrors what app.taskDeps does in production.
//  3. bash run_in_background: spawns `sleep 0.2 && echo hello-bg`,
//     waits for the synthetic completion pair to land in the messages
//     table, and verifies the output file lives under the sandbox.
//  4. monitor: spawns a tiny "echo … sleep … echo" command, asserts the
//     coalesced monitor-event AND the terminal completion fire.
//  5. taskstop: spawns a long-running sleep, kills it, asserts the
//     killed notification arrives.
//  6. SweepOrphans at boot: registers a stale file then re-sweeps with
//     an empty registry to assert it gets removed.
//  7. Sandbox containment: asserts no files were created outside the
//     sandbox data dir.
//
// Usage: invoked from scripts/test/background.sh; cwd is expected to
// be the sandbox directory.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/pressly/goose/v3"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	opencodedb "github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/task"
)

type testDeps struct {
	messages    message.Service
	mu          sync.Mutex
	resumeCalls int
	writeCalls  int
	lastErr     string
}

func (d *testDeps) WritePair(ctx context.Context, sessionID string, p task.SyntheticPair) error {
	d.mu.Lock()
	d.writeCalls++
	d.mu.Unlock()
	assistant := message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{
				ID:       p.AssistantToolCallID,
				Name:     p.AssistantToolName,
				Input:    p.AssistantInput,
				Type:     "tool_use",
				Finished: true,
			},
		},
		Synthetic: true,
	}
	tool := message.CreateMessageParams{
		Role: message.Tool,
		Parts: []message.ContentPart{
			message.ToolResult{
				Type:       message.ToolResultTypeText,
				ToolCallID: p.ToolToolCallID,
				Name:       p.ToolName,
				Content:    p.ToolContent,
			},
		},
		Synthetic: true,
	}
	_, _, err := d.messages.CreatePair(ctx, sessionID, assistant, tool)
	if err != nil {
		d.mu.Lock()
		d.lastErr = err.Error()
		d.mu.Unlock()
	}
	return err
}

func (d *testDeps) IsSessionBusy(string) bool { return false }
func (d *testDeps) ResumeSession(string) {
	d.mu.Lock()
	d.resumeCalls++
	d.mu.Unlock()
}

type result struct {
	BashAck                  string `json:"bash_ack"`
	BashTaskID               string `json:"bash_task_id"`
	BashOutputUnderSandbox   bool   `json:"bash_output_under_sandbox"`
	BashCompletionContent    string `json:"bash_completion_content"`
	BashCompletionSynthetic  bool   `json:"bash_completion_synthetic"`
	BashRunCompletionInDB    bool   `json:"bash_completion_in_db"`
	BashSyntheticInputNoFlag bool   `json:"bash_synthetic_strips_flag"`
	MonitorAck               string `json:"monitor_ack"`
	MonitorEventReceived     bool   `json:"monitor_event_received"`
	MonitorTerminalStatus    string `json:"monitor_terminal_status"`
	TaskListContainsBash     bool   `json:"tasklist_contains_bash"`
	TaskListCrossSessionLeak bool   `json:"tasklist_cross_session_leak"`
	TaskStopKilledReceived   bool   `json:"taskstop_killed_received"`
	OrphanSweepRemoved       bool   `json:"orphan_sweep_removed"`
	SandboxLeak              bool   `json:"sandbox_leak"`
	// Non-interactive wait primitive end-to-end check.
	NonInteractiveWaitOK        bool `json:"non_interactive_wait_ok"`
	NonInteractiveWaitElapsedOK bool `json:"non_interactive_wait_elapsed_ok"`
	NonInteractiveCtxTimeoutOK  bool `json:"non_interactive_ctx_timeout_ok"`
	// Anti-spin: foreground sleep interception through the full bash-tool
	// Run path (openspec bash-background-mode "Foreground wall-clock waits
	// are redirected ..." requirement).
	SleepInterceptOK              bool     `json:"sleep_intercept_ok"`
	SleepInterceptFast            bool     `json:"sleep_intercept_fast"`
	SleepInterceptNoEcho          bool     `json:"sleep_intercept_no_echo"`
	SleepPassthroughInteractiveOK bool     `json:"sleep_passthrough_interactive_ok"`
	SleepPassthroughNoPendingOK   bool     `json:"sleep_passthrough_no_pending_ok"`
	WriteCalls                    int      `json:"write_calls"`
	WriteErr                      string   `json:"write_err,omitempty"`
	Errors                        []string `json:"errors,omitempty"`
}

func main() {
	sandbox := flag.String("sandbox", "", "sandbox directory (required)")
	flag.Parse()
	if *sandbox == "" {
		fmt.Fprintln(os.Stderr, "--sandbox is required")
		os.Exit(2)
	}
	abs, err := filepath.Abs(*sandbox)
	if err != nil {
		die(err)
	}
	dataDir := filepath.Join(abs, ".opencode")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		die(err)
	}

	// Load minimal config so config.Get() returns non-nil — needed by
	// db.NewQuerier and any other config-aware code reached transitively.
	// We load from the sandbox so the loader picks up the sandbox's
	// data.directory (default `.opencode` resolved relative to cwd).
	if err := os.Chdir(abs); err != nil {
		die(err)
	}
	if _, err := config.Load(abs, false); err != nil {
		die(fmt.Errorf("config.Load: %w", err))
	}

	dbPath := filepath.Join(dataDir, "test.db")
	dsn := "file:" + dbPath
	conn, err := sql.Open("sqlite3", dsn)
	if err != nil {
		die(err)
	}
	defer conn.Close()
	if _, err := conn.Exec("PRAGMA foreign_keys = ON;"); err != nil {
		die(err)
	}
	goose.SetBaseFS(opencodedb.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		die(err)
	}
	if err := goose.Up(conn, "migrations/sqlite"); err != nil {
		die(err)
	}
	if _, err := conn.Exec(`
		INSERT INTO sessions (id, project_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at)
		VALUES ('SESS', 'proj', 't', 0, 0, 0, 0, strftime('%s','now'), strftime('%s','now'))
	`); err != nil {
		die(err)
	}
	if _, err := conn.Exec(`
		INSERT INTO sessions (id, project_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at)
		VALUES ('OTHER', 'proj', 't', 0, 0, 0, 0, strftime('%s','now'), strftime('%s','now'))
	`); err != nil {
		die(err)
	}

	q := opencodedb.NewQuerier(conn)
	messages := message.NewService(q, conn)

	perm := permission.NewPermissionService()
	perm.AutoApproveSession("SESS")
	agentReg := agentregistry.GetRegistry()

	task.ResetGlobalRegistry()
	reg := task.NewRegistry(func() string { return dataDir })
	task.SetGlobalRegistry(reg)
	deps := &testDeps{messages: messages}
	task.SetDeps(deps)

	res := result{}

	// ── 1. bash run_in_background ────────────────────────────────────
	ctx := context.WithValue(context.Background(), tools.SessionIDContextKey, "SESS")
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, "msg-1")

	bashTool := tools.NewBashToolForTest()
	bashResp, err := bashTool.RunBackgroundForTest(ctx, tools.ToolCall{ID: "bash-call-1"}, tools.BashParams{
		Command:         "sleep 0.2 && echo hello-bg-output",
		Description:     "e2e bash bg",
		RunInBackground: true,
	}, abs, "SESS")
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("runBackground: %v", err))
	}
	res.BashAck = strings.TrimSpace(bashResp.Content)
	res.BashTaskID = extractTaskID(bashResp.Content)

	// Verify the output file is under the sandbox.
	if res.BashTaskID != "" {
		expectedPath := filepath.Join(dataDir, "tasks", res.BashTaskID+".out")
		if _, err := os.Stat(expectedPath); err == nil {
			res.BashOutputUnderSandbox = strings.HasPrefix(expectedPath, dataDir)
		}
	}

	// Wait for completion to land in DB.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msgs, err := messages.List(ctx, "SESS")
		if err == nil {
			for _, m := range msgs {
				if m.Synthetic && m.Role == message.Assistant {
					for _, p := range m.Parts {
						if tc, ok := p.(message.ToolCall); ok && tc.Name == "bash" {
							res.BashRunCompletionInDB = true
							res.BashCompletionSynthetic = m.Synthetic
							if !strings.Contains(tc.Input, "run_in_background") {
								res.BashSyntheticInputNoFlag = true
							}
						}
					}
				}
				if m.Synthetic && m.Role == message.Tool {
					for _, p := range m.Parts {
						if tr, ok := p.(message.ToolResult); ok && tr.Name == "bash" {
							res.BashCompletionContent = tr.Content
						}
					}
				}
			}
		}
		if res.BashRunCompletionInDB && res.BashCompletionContent != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// ── 2. tasklist scope correctness ────────────────────────────────
	// Register a task in OTHER session to confirm tasklist filters.
	_ = reg.Register(&task.Task{
		ID:        task.NewTaskID(task.KindBash),
		SessionID: "OTHER",
		Kind:      task.KindBash,
	})
	listTool := tools.NewTaskListTool()
	listResp, _ := listTool.Run(ctx, tools.ToolCall{Input: `{"state":"all"}`})
	if strings.Contains(listResp.Content, res.BashTaskID) {
		res.TaskListContainsBash = true
	}
	if strings.Contains(listResp.Content, "OTHER") {
		res.TaskListCrossSessionLeak = true
	}

	// ── 3. taskstop on a long-running bash ───────────────────────────
	stopResp, err := bashTool.RunBackgroundForTest(ctx, tools.ToolCall{ID: "bash-call-stop"}, tools.BashParams{
		Command:         "sleep 30",
		Description:     "e2e bash stop",
		RunInBackground: true,
	}, abs, "SESS")
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("stop spawn: %v", err))
	}
	stopTaskID := extractTaskID(stopResp.Content)
	stopTool := tools.NewTaskStopToolForTest(perm, agentReg)
	_, _ = stopTool.Run(ctx, tools.ToolCall{Input: fmt.Sprintf(`{"task_id":%q}`, stopTaskID)})

	// Wait for the killed completion to land. The bash monitor goroutine
	// notices SIGTERM, fires the synthetic completion with status=killed.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if t, ok := reg.Get(stopTaskID); ok && t.Notified.Load() {
			res.TaskStopKilledReceived = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// ── 4. monitor ───────────────────────────────────────────────────
	mTool := tools.NewMonitorToolForTest(perm, agentReg)
	mResp, mErr := mTool.Run(ctx, tools.ToolCall{
		ID:    "monitor-call",
		Input: `{"cmd":"bash","args":["-c","echo INFO; echo ERROR-LINE; sleep 0.05; echo ERROR-LINE-2"],"pattern":"ERROR","min_interval_ms":100,"max_events":20}`,
	})
	if mErr != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("monitor: %v", mErr))
	}
	res.MonitorAck = strings.TrimSpace(mResp.Content)
	monitorTaskID := extractTaskID(mResp.Content)

	// Wait for monitor-event + terminal completion.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msgs, _ := messages.List(ctx, "SESS")
		for _, m := range msgs {
			if !m.Synthetic {
				continue
			}
			for _, p := range m.Parts {
				if tr, ok := p.(message.ToolResult); ok && tr.Name == "monitor" {
					if strings.Contains(tr.Content, "match") && strings.Contains(tr.Content, "ERROR") {
						res.MonitorEventReceived = true
					}
					if strings.Contains(tr.Content, "Monitor stream ended") {
						res.MonitorTerminalStatus = "completed"
					}
					if strings.Contains(tr.Content, "max_events reached") {
						res.MonitorTerminalStatus = "killed-cap"
					}
					if strings.Contains(tr.Content, "Monitor script failed") {
						res.MonitorTerminalStatus = "failed"
					}
				}
			}
		}
		if res.MonitorEventReceived && res.MonitorTerminalStatus != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if t, ok := reg.Get(monitorTaskID); ok && !t.Notified.Load() {
		res.Errors = append(res.Errors, "monitor terminal notif missing")
	}

	// ── 5. SweepOrphans containment ──────────────────────────────────
	// Drop a stale .out file that isn't in the registry. Sweep should kill it.
	stale := filepath.Join(dataDir, "tasks", "shell_STALEORPHANXYZXYZXYZXYZXYZ.out")
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("write stale: %v", err))
	}
	// Reset the registry to mimic boot. Live tasks vanish; orphan sweep
	// then removes EVERY stale file (including the live task's file —
	// which is desired: in-memory restart loses tasks).
	task.ResetGlobalRegistry()
	freshReg := task.NewRegistry(func() string { return dataDir })
	task.SetGlobalRegistry(freshReg)
	freshReg.SweepOrphans(dataDir)
	if _, err := os.Stat(stale); errors.Is(err, os.ErrNotExist) {
		res.OrphanSweepRemoved = true
	}

	// ── 6. Sandbox containment ───────────────────────────────────────
	// Walk the sandbox and verify every .out file lives under .opencode/tasks/.
	res.SandboxLeak = false
	_ = filepath.Walk(abs, func(path string, info os.FileInfo, _ error) error {
		if info == nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".out") {
			return nil
		}
		if !strings.HasPrefix(path, filepath.Join(dataDir, "tasks")) {
			res.SandboxLeak = true
		}
		return nil
	})

	// ── 7. Non-interactive wait primitive ─────────────────────────────
	// Direct end-to-end exercise of the wait primitive against the SAME
	// registry the production processGeneration outer loop uses. This
	// pins the wait contract without needing an LLM in the loop:
	//   - register a bash-style task that finishes in ~150ms,
	//   - call WaitForActiveTasks (the call processGeneration makes),
	//   - assert it unblocks within ~150-300ms of the task transitioning,
	//   - then in a second scenario, register a task that NEVER finishes
	//     and assert ctx.Err() unblocks the wait at the deadline.
	{
		waitReg := task.GlobalRegistry()
		waitSess := "WAIT_SESS"

		// Scenario A: clean completion.
		waitTaskID := task.NewTaskID(task.KindBash)
		if err := waitReg.Register(&task.Task{
			ID:        waitTaskID,
			SessionID: waitSess,
			Kind:      task.KindBash,
		}); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("wait register: %v", err))
		}
		waitStart := time.Now()
		// Simulate the bash subprocess monitor goroutine: MarkFinished
		// after ~150ms.
		go func() {
			time.Sleep(150 * time.Millisecond)
			waitReg.MarkFinished(waitTaskID, task.StateCompleted, nil)
		}()
		ctxWait, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
		if err := waitReg.WaitForActiveTasks(ctxWait, waitSess, task.WaitOptions{IncludeMonitor: true}); err == nil {
			res.NonInteractiveWaitOK = true
		} else {
			res.Errors = append(res.Errors, fmt.Sprintf("wait clean: %v", err))
		}
		cancelWait()
		elapsed := time.Since(waitStart)
		if elapsed >= 100*time.Millisecond && elapsed < 500*time.Millisecond {
			res.NonInteractiveWaitElapsedOK = true
		} else {
			res.Errors = append(res.Errors, fmt.Sprintf("wait elapsed=%v (want 100ms-500ms)", elapsed))
		}

		// Scenario B: ctx deadline trips the wait while a task is still
		// running. Verifies the timeout path that production uses for
		// step.Timeout / OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT.
		hangID := task.NewTaskID(task.KindBash)
		_ = waitReg.Register(&task.Task{
			ID:        hangID,
			SessionID: waitSess + "_HANG",
			Kind:      task.KindBash,
		})
		ctxTight, cancelTight := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := waitReg.WaitForActiveTasks(ctxTight, waitSess+"_HANG", task.WaitOptions{IncludeMonitor: true})
		cancelTight()
		if err != nil {
			res.NonInteractiveCtxTimeoutOK = true
		} else {
			res.Errors = append(res.Errors, "wait ctx-timeout returned nil; want non-nil err")
		}
		// Clean up the hanging task so the orphan sweep at end-of-run is honest.
		waitReg.MarkFinished(hangID, task.StateKilled, nil)
	}

	// ── 8. Anti-spin: foreground sleep interception ───────────────────
	// Full bash-tool Run path (permission gate included). In a
	// non-interactive ctx with a pending non-monitor task, a pure-wait
	// command (`sleep N; echo …`) must NOT execute; the tool blocks on
	// WaitForActiveTasks and returns a synthetic result enumerating the
	// task(s) that completed during the wait. Interactive ctx and
	// no-pending-task ctx must execute the sleep verbatim.
	{
		fullBash := tools.NewBashTool(perm, agentReg)
		ireg := task.GlobalRegistry()
		niCtx := context.WithValue(ctx, tools.NonInteractiveContextKey, true)

		// 8a. Interception: pending task finishes ~300ms in; the requested
		// sleep is 30s. A pass returns fast with the interception note and
		// without the echo output.
		pendID := task.NewTaskID(task.KindBash)
		if err := ireg.Register(&task.Task{ID: pendID, SessionID: "SESS", Kind: task.KindBash}); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("intercept register: %v", err))
		}
		go func() {
			time.Sleep(300 * time.Millisecond)
			ireg.MarkFinished(pendID, task.StateCompleted, nil)
		}()
		interceptStart := time.Now()
		iResp, iErr := fullBash.Run(niCtx, tools.ToolCall{
			ID:    "sleep-intercept-call",
			Input: `{"command":"sleep 30; echo intercept-should-not-run","description":"e2e sleep intercept"}`,
		})
		interceptElapsed := time.Since(interceptStart)
		if iErr != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("intercept run: %v", iErr))
		}
		res.SleepInterceptOK = iErr == nil &&
			strings.Contains(iResp.Content, "[non-interactive wait]") &&
			strings.Contains(iResp.Content, pendID)
		res.SleepInterceptFast = interceptElapsed < 10*time.Second
		res.SleepInterceptNoEcho = !strings.Contains(iResp.Content, "intercept-should-not-run")
		if !res.SleepInterceptOK {
			res.Errors = append(res.Errors, fmt.Sprintf("intercept content (elapsed=%v): %.300s", interceptElapsed, iResp.Content))
		}

		// 8b. Interactive passthrough: same command shape actually runs.
		pResp, pErr := fullBash.Run(ctx, tools.ToolCall{
			ID:    "sleep-passthrough-call",
			Input: `{"command":"sleep 0.2; echo passthrough-ok","description":"e2e sleep passthrough"}`,
		})
		if pErr != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("passthrough run: %v", pErr))
		}
		res.SleepPassthroughInteractiveOK = pErr == nil && strings.Contains(pResp.Content, "passthrough-ok")

		// 8c. Non-interactive but zero pending tasks: passthrough too.
		nResp, nErr := fullBash.Run(niCtx, tools.ToolCall{
			ID:    "sleep-no-pending-call",
			Input: `{"command":"sleep 0.2; echo no-pending-ok","description":"e2e no-pending passthrough"}`,
		})
		if nErr != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("no-pending run: %v", nErr))
		}
		res.SleepPassthroughNoPendingOK = nErr == nil && strings.Contains(nResp.Content, "no-pending-ok")
	}

	deps.mu.Lock()
	res.WriteCalls = deps.writeCalls
	res.WriteErr = deps.lastErr
	deps.mu.Unlock()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		die(err)
	}
}

func extractTaskID(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "task_id:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "task_id:"))
		}
	}
	return ""
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "background-e2e:", err)
	os.Exit(2)
}
