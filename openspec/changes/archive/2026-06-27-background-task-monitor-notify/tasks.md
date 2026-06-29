## 1. DB migration & generated bindings

- [x] 1.1 Add SQLite migration `internal/db/sql/<NN>_messages_synthetic.sqlite.sql` with `ALTER TABLE messages ADD COLUMN synthetic BOOLEAN NOT NULL DEFAULT FALSE`
- [x] 1.2 Add MySQL migration `internal/db/sql/<NN>_messages_synthetic.mysql.sql` with the same column definition (`BOOLEAN`/`TINYINT(1)` matching MySQL convention used elsewhere in the codebase)
- [x] 1.3 Update sqlc queries in `internal/db/sql/queries/messages.sql` so all SELECT-from-messages queries include `synthetic` and INSERTs accept it; sort consumer queries surface the column
- [x] 1.4 Run `sqlc generate`; commit regenerated bindings under `internal/db/`
- [x] 1.5 Add `Synthetic bool` field to the `message.Message` Go struct in `internal/message/message.go` (or wherever the persisted struct lives); map from the sqlc-generated row type
- [x] 1.6 Extend `messages.CreatePair` signature to accept an explicit `synthetic bool` parameter (or a `CreatePairOptions{Synthetic: true}` field); preserve a back-compat shim — existing callers default to false
- [x] 1.7 Verify both DBs apply the migration at boot under `go test ./internal/db/...`

## 2. internal/task package — registry + lifecycle primitives

- [x] 2.1 Create `internal/task/` package; add `task.go` with `Task` struct (matching `design.md::D4`), `Kind` enum (`KindBash`, `KindTask`, `KindMonitor`, `KindCron`), `Status` enum (`StatusCompleted`, `StatusFailed`, `StatusKilled`, `StatusMonitorEvent`), `State` enum
- [x] 2.2 Add `registry.go` with a `Registry` interface and a `globalRegistry` singleton-backed implementation (sync.RWMutex around a `map[string]*Task`); methods: `Register`, `Get`, `ListBySession`, `Kill`, `MarkFinished`, `SweepOrphans`, `PrepareOutputFile`, `GlobalRegistry()`
- [x] 2.3 Add task ID generator: `NewTaskID(kind Kind) string` producing `<kind>_<base32-of-16-random-bytes>`; ensure uniqueness via registry-side rejection
- [x] 2.4 Add `PrepareOutputFile(taskID) (path string, file *os.File, err error)` that `MkdirAll`s `<config.Data.Directory>/tasks/` (0o700) and creates `<task_id>.out` with `O_CREATE|O_EXCL|O_WRONLY` (0o600)
- [x] 2.5 Add `SweepOrphans(dataDir string)` that iterates `<dataDir>/tasks/*.out`, deleting every file with no matching registry entry; called at opencode boot from app initialization
- [x] 2.6 Wire `SweepOrphans` into `internal/app/` boot path before any sessions activate
- [x] 2.7 Unit tests in `internal/task/registry_test.go`: register/get/list, duplicate-ID rejection, kill flips state and signals, sweep removes orphans
- [x] 2.8 Unit tests for ID generator: format match, uniqueness across 10K iterations

## 3. internal/task package — EnqueueTaskCompletion primitive

- [x] 3.1 Add `background.go` with `CompletionInput` struct and `EnqueueTaskCompletion(ctx, in) error` (matching `design.md::D3`)
- [x] 3.2 Implement the primitive's flow: build the synthetic Assistant(ToolCall) + Tool(ToolResult) pair → call `messages.CreatePair` with `synthetic: true` on the Assistant message → if `Status` is terminal AND `SuppressIfNotified` is true, CAS the task's `Notified` flag and bail-out on existing-true
- [x] 3.3 Implement auto-resume: after a successful write, if `agent.IsSessionBusy(sessionID)` is false, invoke `agent.Run(ctx, sessionID, "", 0)` in a goroutine; the in-flight session case is a no-op
- [x] 3.4 Surface `agent.IsSessionBusy` and `agent.Run` to `internal/task/` via a small Service interface (`internal/task/deps.go`) to avoid importing `internal/llm/agent` directly and creating a cycle; wire concrete dependencies at app boot
- [x] 3.5 Unit tests for `EnqueueTaskCompletion`: idle session triggers Run, busy session does NOT, terminal status sets notified, `monitor-event` skips notified gate, duplicate terminal call is dropped by CAS

## 4. Cron migration

- [x] 4.1 In `internal/cron/scheduler.go` replace the body of `writeSyntheticMessages` with a single call to `task.EnqueueTaskCompletion(...)` (passing `Kind: KindCron`, `OriginatingToolName: "task"`, `Status: StatusCompleted`)
- [x] 4.2 Verify `internal/cron/cron_test.go` passes without modification (`go test ./internal/cron/...`)
- [x] 4.3 If cron tests assert on `writeSyntheticMessages`'s direct call to `messages.CreatePair`, update the assertion target to `task.EnqueueTaskCompletion` while keeping the observable end-state identical
- [x] 4.4 Verify cron-fired Assistant messages now have `synthetic: true` via a targeted DB read assertion in the cron test suite

## 5. Bash tool — `run_in_background`

- [x] 5.1 Add `RunInBackground bool` to `BashParams` in `internal/llm/tools/bash.go`; update the tool's input schema JSON to declare the new field with description per the bash-background-mode spec
- [x] 5.2 In `bash.Run`, branch on `RunInBackground`: when false, behave exactly as today; when true, proceed to the background path
- [x] 5.3 Background path: allocate output file via `task.GlobalRegistry().PrepareOutputFile`, redirect cmd.Stdout/Stderr to it, `cmd.Start()`, register the task, return the ack ToolResult immediately
- [x] 5.4 Spawn monitor goroutine that waits on `cmd.Wait`, syncs the output file, reads the output, calls `task.EnqueueTaskCompletion` with kind=Bash, name="bash", status from exit code, and content as the captured output (with the same output-size truncation as synchronous bash)
- [x] 5.5 Ensure the synthetic ToolCall's `Input` JSON strips `RunInBackground` (so the synthetic completion renders like a synchronous call)
- [x] 5.6 Ensure the 600s timeout cap does NOT apply when `RunInBackground: true`; emit a clear info note if both `RunInBackground: true` and `Timeout` are set (ignore timeout)
- [x] 5.7 Update bash tool description in `bash.go`'s `Info()` to document `run_in_background` per the spec — mention the ack shape and the deferred completion notification
- [x] 5.8 Update prompts in `internal/llm/prompt/` that talk about sleep/polling: discourage `sleep N` patterns where `run_in_background` would serve better; reference the task-notification arrival
- [x] 5.9 Integration test: bash with a 3-second sleep+echo, run_in_background:true, assert ack arrives within 100ms, assert synthetic completion appears in the session 3-4 seconds later, assert Output content is the expected text
- [x] 5.10 Integration test: bash with a non-existent command, run_in_background:true, assert spawn fails synchronously (no ack, regular tool error)
- [x] 5.11 Integration test: bash with a deliberately fast command (`echo hi`) — verify the dedupe race is benign (at most one completion appears in the log)

## 6. Task tool — `async` mode

- [x] 6.1 Add `Async bool` to `TaskParams` in `internal/llm/agent/agent-tool.go`; update the tool's input schema and the `Required` list (Async is optional)
- [x] 6.2 In `agentTool.Run`, branch on Async: when false, behave exactly as today; when true, proceed to the async path
- [x] 6.3 Async path: build the subagent and taskSession as today, allocate output file, register the task with Kind=KindTask and a cancel function, spawn background goroutine, return ack immediately
- [x] 6.4 Background goroutine: read `<-done`, perform cost rollup (identical to the synchronous path), write the final response (with `<task_id>`/`<task_resume_hint>` trailers OR struct-output content if applicable) to the output file, call `task.EnqueueTaskCompletion` with Kind=KindTask, name="task", status from result.Error nil-ness, content=final response
- [x] 6.5 Update task tool's `Info()` description with async semantics; emphasize that resume by task_id still works for both sync and async flows
- [x] 6.6 Integration test: task tool with `async: true` against a workhorse subagent — assert ack arrives quickly, subagent runs, synthetic completion appears with the expected response content
- [x] 6.7 Integration test: task tool with `async: true` then `taskstop` mid-run — assert cancellation propagates, cost rollup still runs, synthetic StatusKilled completion appears

## 7. Monitor tool

- [x] 7.1 Create `internal/llm/tools/monitor.go`; define `MonitorParams` with `cmd`, `args`, `cwd`, `pattern`, `min_interval_ms` (default 5000), `max_events` (default 200), `description`
- [x] 7.2 Implement input validation: compile `pattern` as `regexp.Compile`, validate ranges (min_interval_ms 100-600000, max_events 1-10000), return tool-validation error on failure
- [x] 7.3 Spawn subprocess with merged stdout+stderr through a tee: line-by-line scan against the compiled pattern, write every line to the output file
- [x] 7.4 Implement coalesce ticker (`time.NewTicker(min_interval_ms)`); on each tick, drain the matched-lines buffer; if non-empty, call `task.EnqueueTaskCompletion` with `Kind: KindMonitor`, `Status: StatusMonitorEvent`, `Content` formatted as "<N> match(es) in window:\n<lines>"
- [x] 7.5 Implement max_events counter; when reached, cancel ticker, drain remaining, SIGTERM subprocess, wait for exit, emit terminal StatusKilled notification with "max_events reached" summary
- [x] 7.6 Implement natural-exit handler: when subprocess exits without intervention, drain remaining buffer, emit terminal Status (Completed or Failed) with the spec's summary text
- [x] 7.7 Register the `monitor` tool in the tool registry; register the `monitor` permission key with default `ask`
- [x] 7.8 Add the new permission key documentation to `.opencode.json` schema generator output (`go run cmd/schema/main.go > opencode-schema.json`)
- [x] 7.9 Integration test: monitor `bash -c "echo a; echo ERROR; sleep 1; echo ERROR x2"` with pattern=`ERROR`, min_interval_ms=500, max_events=10 — assert one monitor-event notification arrives (containing both ERROR lines coalesced) and then a StatusCompleted terminal notification when bash exits
- [x] 7.10 Integration test: monitor with chatty output and max_events=3 — assert the subprocess is killed after 3 events and a StatusKilled "max_events reached" notification appears

## 8. tasklist and taskstop tools

- [x] 8.1 Create `internal/llm/tools/tasklist.go`; implement `TaskListParams` (`state` optional, `limit` optional), read-only registry call `ListBySession(sessionID)`, format result with task_id, kind, state, started_at, finished_at, exit_code lines
- [x] 8.2 Register `tasklist` permission key with default `allow`
- [x] 8.3 Create `internal/llm/tools/taskstop.go`; implement `TaskStopParams` (`task_id` required), verify session_id match (refuse cross-session), call `registry.Kill(taskID)`
- [x] 8.4 In `registry.Kill`: for shell tasks send SIGTERM with 5s SIGKILL escalation; for subagent tasks call the stored CancelFunc; in both cases the originating tool's monitor goroutine fires the StatusKilled completion via EnqueueTaskCompletion
- [x] 8.5 Register `taskstop` permission key with default `ask`
- [x] 8.6 Add anti-polling language to both tool descriptions: "do NOT use tasklist as a polling loop — completion notifications arrive automatically"
- [x] 8.7 Unit tests: tasklist returns only caller's session tasks; taskstop refuses cross-session; taskstop on already-terminal task is a no-op; taskstop on unknown id returns an error

## 9. Bridge filter (chat-bridge spec)

- [x] 9.1 In `internal/bridge/service/dispatch.go` (or wherever the parts demux emits tool-update indicators), add a `if msg.Synthetic { continue }` guard before any indicator-emission code path
- [x] 9.2 Verify the synthetic Tool message also flows through normally (synthetic Tool messages do NOT trigger indicator emission today because indicators are keyed off Assistant ToolCall parts, not Tool parts — confirm this assumption holds)
- [x] 9.3 Unit test in `internal/bridge/service/`: inject a synthetic Assistant(ToolCall, synthetic=true) + Tool pair into a bound session; assert no outbound indicator is dispatched; inject a real (synthetic=false) Assistant(ToolCall) pair; assert the indicator IS dispatched
- [x] 9.4 Integration test (bridge end-to-end): spawn a background bash on a Slack-bound session, let it complete; assert the bot sends ONLY the agent's text reply to the channel (no 🔧 task or 🔧 bash icon for the synthetic completion); assert the agent's next REAL tool call (e.g., a Read) DOES emit an icon

## 10. Agent prompt + tool description updates

- [x] 10.1 Update the main agent system prompt (`internal/llm/prompt/`) to document the background-task pattern: "if you run `bash` with `run_in_background: true` or `task` with `async: true`, the tool will return an immediate ack containing `task_id`. You will receive a synthetic completion message later — do NOT poll. Use `tasklist` for one-shot inventory if you need to confirm a task is still running. Use `taskstop` to kill if needed."
- [x] 10.2 Cross-reference: when discussing long-running shell commands, link to background-bash; when discussing parallel subagent work, link to async-task; when discussing log watching, link to `monitor`
- [x] 10.3 Discourage sleep-loops: explicitly direct the agent away from `while true; do sleep 5; done` patterns where a `monitor` or `run_in_background` would serve

## 11. Documentation

- [x] 11.1 Add `docs/background-tasks.md` covering: the three tool surfaces (bash bg, task async, monitor), output file convention, restart semantics, permissions
- [x] 11.2 Update `CHANGELOG.md` with the new tools, new bash/task parameters, new `synthetic` column, new permission keys (`monitor`, `tasklist`, `taskstop`), and the cron internal-migration note (no user-facing behavior change)
- [x] 11.3 Update `CLAUDE.md` if any developer-facing patterns shift (notably: the `synthetic` flag and the EnqueueTaskCompletion primitive)
- [x] 11.4 Update `opencode-schema.json` via `go run cmd/schema/main.go > opencode-schema.json` to reflect new tool parameters and permission keys

## 12. End-to-end validation

- [x] 12.1 Final test pass: `make test`
- [x] 12.2 Build a release-mode binary, run `opencode serve` in a scratch dir, verify a background bash + a monitor + an async task all complete and inject correctly via the HTTP `/event` SSE stream
- [x] 12.3 Smoke in a c2-agent-shaped headless run: ensure `permissionMode: allow` covers all three new spawn paths and the auto-resume actually starts a fresh agent turn
- [x] 12.4 Verify the deprecated synchronous paths (bash without `run_in_background`, task without `async`) are byte-identical to pre-change behavior for at least 5 representative commands; no regressions
