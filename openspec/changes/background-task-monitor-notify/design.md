## Context

Opencode currently runs every tool synchronously. `bash` returns to the agent only after the shell process exits (or the 600s timeout fires). The `task` tool spawns a subagent and blocks on `result := <-done` (`internal/llm/agent/agent-tool.go:171`). `agent.Run` rejects concurrent invocations on the same session (`ErrSessionBusy`), so even if a tool wanted to fire-and-forget, the parent agent couldn't keep working in parallel. The only path that asynchronously inserts new content into a session today is the cron scheduler, which writes a synthetic Assistant(ToolCall)+Tool(ToolResult) pair via `messages.CreatePair` (`internal/cron/scheduler.go:325 writeSyntheticMessages`) and relies on the existing session-busy-lock to atomically commit it.

Claude Code shipped — and then iterated through several deprecations of — a notification pattern for the same problem space. Their final shape (visible in `/Users/nouwa/Development/open-source-fork/claude-code-source-code/src/`):

- `BashTool` with `run_in_background: true` returns immediately with a `task_id` and an output-file path; on completion, a `<task-notification>` XML block is injected into the next user prompt.
- A separate feature-gated `MonitorTool` spawns a subprocess and streams per-line events as notifications.
- The deprecated `TaskOutput`/`TaskStop` pair (formerly `BashOutput`/`KillShell`) still exists for back-compat but the canonical agent affordance is "Read the output file" + "wait for the notification."

Both systems agree on the underlying idea: **the system pushes events into the agent's next turn instead of the agent pulling status**. The savings are two-fold — fewer tool calls (token-cheap) and an uninvalidated prompt cache (cache-friendly). The polling alternative spends ~500 tokens per check; a 30-poll wait for a slow build costs 15K+ tokens of pure waiting.

Opencode is well-positioned to adopt the same pattern because the transport layer already works — cron proves it. What's missing is the task lifecycle abstraction, the unified injection primitive, and the tool surfaces that drive it.

This change adds those pieces and migrates cron onto the new primitive so opencode has exactly one way to inject background events into a live session.

## Goals / Non-Goals

**Goals:**

- Provide a single, named, in-process primitive (`task.EnqueueTaskCompletion`) for "this background work finished, here's the result" that all background-task sources (bash, task, monitor, cron) call.
- Add a `run_in_background` parameter to the existing `bash` tool that returns an immediate ack with `task_id` + `output_file` path and runs the subprocess to completion in the background.
- Add an `async` parameter to the existing `task` tool that returns an immediate ack with `task_id` and runs the subagent to completion in the background.
- Add a process-spawning `monitor` tool that streams per-event notifications matched against a regex pattern, with a per-task coalesce window to bound notification rate.
- Add `tasklist` and `taskstop` control tools so the agent can inspect and terminate background work it spawned.
- Migrate the cron scheduler to call `EnqueueTaskCompletion` instead of its local `writeSyntheticMessages`. End-state behavior must be identical.
- Suppress chat-bridge tool-update indicator (🔧 icon) emission for any Assistant message marked `synthetic: true` so background-task injections do not leak into Slack/Telegram/Mattermost as spurious tool calls.
- Preserve today's synchronous behavior as the default for `bash` and `task` — omitting the new parameters keeps every existing caller correct.

**Non-Goals:**

- Restart-recovery of in-flight background tasks. Task state is in-memory only; an opencode restart loses the task list and produces no follow-up notification. Future change can add DB-backed task metadata; this one does not.
- DB-backed output storage. Output content lives on disk under `<data.dir>/tasks/`. The agent reads it via the existing `Read` tool.
- MCP-event-source monitors, kubectl-log monitors, webhook monitors, file-tail monitors. `monitor` only spawns its own subprocess in this change.
- TUI redesign for synthetic messages. A subtle styling (dim color, "(background)" tag) is the most we'll do; full transcript reflow is out of scope.
- Removing or deprecating the synchronous `bash`/`task` paths. Both remain the default. No breaking change to existing callers.
- A `taskoutput` polling tool. Claude Code deprecated theirs; we never introduce one. Agents `Read` the output file at the path the ack returned.

## Decisions

### D1. Always auto-continue on background-task completion

When a background task fires `EnqueueTaskCompletion` and the bound session is idle (no in-flight `agent.Run`), the primitive writes the synthetic Assistant(ToolCall)+Tool(ToolResult) pair AND immediately starts a fresh `agent.Run` with the synthetic input. No mode distinction — TUI, `opencode serve`, and flow execution all behave the same.

*Why:* simplest mental model. The headless serve path needs auto-continue or the agent freezes waiting for human input that will never come (c2-agent runs in K8s with no human in the loop). The TUI already renders unexpected turns gracefully — cron-fired and bridge-fired messages already arrive without warning today, so background completions are visually indistinguishable from already-supported cases.

*Alternative considered:* mode-aware (auto-continue in headless, surface-only in TUI). Rejected: doubles the code path, surfaces a "queued notification" UI affordance that doesn't exist today, and makes background tasks behave differently across run modes — the same .opencode.json and same code yielding different agent behavior depending on whether it's a TUI session vs a `serve` invocation, which is a surprising debug experience.

### D2. Synthetic injection shape: Assistant(ToolCall)+Tool(ToolResult), originating tool name + `synthetic` flag

The completion event materializes in the message log as the SAME shape `cron` already uses: an Assistant message carrying a ToolCall, followed by a Tool message carrying the matching ToolResult. The ToolCall `name` is the **originating tool's name** (`bash`, `task`, `monitor`). The Assistant message carries a new `synthetic: true` boolean column on the messages table.

*Why two parts.* First, the originating-tool name keeps the existing tool result renderers honest — `bash`'s renderer already knows how to format a bash result; reusing the name means we don't need bespoke render paths for background completions. Second, the `synthetic` flag is the **machine-readable** marker that distinguishes "the model actually called this tool" from "the system injected this on its behalf." Without the flag, downstream consumers (bridge indicator emission, transcript export, analytics) cannot tell the two apart by inspection because the message shape is identical.

*Alternative considered (rejected):* a dedicated `name="task_complete"` synthetic tool. Pro: visually honest in the transcript. Con: every renderer (TUI, transcript exporter, bridge) needs a new code path to display `task_complete` results — and that renderer would mostly delegate back to the originating tool's renderer anyway since the result body is the same. The dedicated-name path doubles the rendering surface to save one boolean column.

*Alternative considered (rejected):* `<task-notification>` user-prompt envelope (Claude Code's choice). Cleanly orthogonal to tool calls, but in opencode it would introduce a SECOND injection shape alongside cron's existing Assistant+Tool pair. The "unify on one shape" mandate from question B rules this out — we'd be carrying two injection patterns indefinitely.

### D3. The injection primitive is `task.EnqueueTaskCompletion`

New package `internal/task/`. Public entry point:

```go
package task

type CompletionInput struct {
    SessionID             string
    OriginatingToolCallID string         // ID of the spawn tool call (Bash run_in_background, Task async, Monitor, cron)
    OriginatingToolName   string         // "bash" | "task" | "monitor"
    TaskID                string
    Kind                  Kind           // bash | task | monitor | cron
    Status                Status         // completed | failed | killed
    ExitCode              *int           // for shell-spawned tasks
    Content               string         // the agent-facing tool result body
    SuppressIfNotified    bool           // honor the per-task notified flag (see D9)
}

// EnqueueTaskCompletion writes the synthetic Assistant(ToolCall)+Tool(ToolResult)
// pair to the session and, if the session is idle, starts a fresh agent.Run
// with the synthetic ToolCall as input. If the session is busy, the pair is
// committed atomically and agent.Run is NOT started — the in-flight run will
// see the new messages on its next iteration. Idempotent on the task's
// `notified` flag.
func EnqueueTaskCompletion(ctx context.Context, in CompletionInput) error
```

*Why this shape:* it generalizes cron's `writeSyntheticMessages` while preserving the session-busy semantics that cron already relies on. The session-busy detection reuses the existing `agent.IsSessionBusy(sessionID)` check. The `agent.Run` trigger is the same call other components use today (cron, bridge dispatch); we are not inventing a new "resume" code path.

### D4. The in-memory registry lives in `internal/task/registry.go`

```go
package task

type State int8
const (
    StatePending State = iota
    StateRunning
    StateCompleted
    StateFailed
    StateKilled
)

type Task struct {
    ID                    string
    SessionID             string
    Kind                  Kind
    State                 State
    StartedAt             time.Time
    FinishedAt            time.Time
    ExitCode              *int
    OutputPath            string
    OriginatingToolCallID string
    OriginatingToolName   string
    Notified              atomic.Bool

    // Lifecycle hooks (one of these is set depending on Kind):
    Cancel context.CancelFunc       // for `task` (subagent) async
    Proc   *os.Process              // for `bash` background, `monitor`
}

type Registry interface {
    Register(t *Task)                 // called at spawn
    Get(taskID string) (*Task, bool)
    ListBySession(sessionID string) []*Task
    Kill(taskID string) error         // signals + flips state to killed; emits completion
    MarkFinished(taskID string, s State, exitCode *int)
    SweepOrphans(dataDir string)      // removes <data.dir>/tasks/*.out without a matching live task; called at boot
}

// Process-global singleton.
func GlobalRegistry() Registry
```

*Why a singleton:* tasks span sessions (the agent that spawned task X may have finished its turn; the registry needs to live longer than any single `agent.Run`). A per-process singleton with sync.RWMutex matches how `cron.Service` already lives.

*Why in-memory only:* see Non-Goals. Restart drops everything. If we ever want recovery, a `MetadataStore` interface can be added that the registry calls on transitions — no API break.

### D5. Output file path: `<config.Data.Directory>/tasks/<task_id>.out`

`config.Data.Directory` is already a stable path (set via the `data.directory` field in `.opencode.json`, default `.opencode`). The `tasks/` subdirectory is created on demand (`MkdirAll`) by `task.GlobalRegistry().PrepareOutputFile(taskID)` which returns the absolute path.

Task IDs are `<kind>_<base32(16-byte rand)>` — e.g. `shell_F4M2P9...`, `agent_J7K8X3...`, `monitor_Q1Z2A4...`. Kind-prefixing makes the path human-debuggable and matches Claude Code's convention.

Cleanup: `task.GlobalRegistry().SweepOrphans` runs at opencode boot before any sessions activate. It deletes any `*.out` under `tasks/` that doesn't correspond to a live registry entry — which after restart is all of them. K8s ephemeral pods reset the whole tree between runs anyway, so this sweep is meaningful only in long-lived dev sessions.

### D6. Bash background mode (`run_in_background: true`)

`BashParams` gets `RunInBackground bool`. When true:

1. `bash.Run` skips its current `output := captureOutput(cmd)` synchronous path.
2. Calls `task.GlobalRegistry().PrepareOutputFile(taskID)` to allocate `<data.dir>/tasks/<task_id>.out`.
3. Starts the subprocess with stdout/stderr redirected (`cmd.Stdout = f; cmd.Stderr = f`).
4. Registers the task: `registry.Register(&Task{ID:..., Kind:KindBash, Proc:cmd.Process, ...})`.
5. Returns IMMEDIATELY with an ack ToolResult:
   ```
   Background task started.
   task_id: shell_F4M2P9...
   output_file: /workspace/.opencode/tasks/shell_F4M2P9....out
   command: <truncated command>

   The task is running. You will receive a synthetic tool result with the
   final output when it completes. Read the output file with the Read tool
   if you need to inspect progress in the meantime.
   ```
6. A monitor goroutine waits on `cmd.Wait()`. When the process exits:
   - Reads the output file content (capped at `bash`'s existing output size budget).
   - Calls `task.EnqueueTaskCompletion` with `Kind:KindBash`, `OriginatingToolName:"bash"`, exit code, content.
   - The synthetic ToolCall `input` JSON is the same `BashParams` the agent originally sent (without `RunInBackground`) so the renderer formats it naturally.
   - The Tool message `content` is the bash output, same shape as a synchronous bash result.

*Why the synthetic input mirrors the original spawn input:* renderers and downstream consumers (logs, exporters, tests) inspect the input to format the command. Reusing it ensures a synthetic completion is visually identical to a synchronous bash result — modulo the `synthetic: true` flag.

*Timeout:* the existing 600s synchronous timeout cap does NOT apply to the background subprocess. Background bash can run indefinitely (until the pod dies or the agent calls `taskstop`).

### D7. Task tool async mode (`async: true`)

`TaskParams` gets `Async bool`. When true:

1. The factory creates the subagent and its taskSession the same as today.
2. Instead of `result := <-done` (blocking), we read `done` in a background goroutine.
3. Return IMMEDIATELY with an ack ToolResult (analogous to D6's ack but with subagent metadata).
4. The background goroutine waits on `done`, computes the same response content the synchronous path produces (including the `<task_id>` and `<task_resume_hint>` trailers), then calls `task.EnqueueTaskCompletion` with `Kind:KindTask`, `OriginatingToolName:"task"`, content.
5. Cost rollup from subagent to parent session still happens — moved into the background goroutine.

*Why preserve the synchronous default:* every existing caller of `task` (cron, the agent prompt's natural usage) expects the call to block until the subagent finishes. Flipping the default would silently change behavior across the codebase. Opt-in via `async: true` keeps the diff bounded.

### D8. Monitor tool

```
monitor(
  cmd: string,
  args: []string?,
  cwd: string?,
  pattern: string,                    // RE2 regex, line-by-line
  min_interval_ms: int? (default 5000),
  max_events: int? (default 200),
  description: string?
)
```

Behavior:

1. Allocates an output file (D5) and a task ID with `monitor_` prefix.
2. Starts the subprocess. Stdout AND stderr are merged into a tee that:
   - Writes every line to the output file (full record).
   - Scans every line against the compiled `pattern`.
   - Pushes matched lines into a `batchedEvents []string` buffer.
3. A coalesce ticker (driven by `min_interval_ms`) fires once per window:
   - If `batchedEvents` is empty, do nothing (no notification this window).
   - Otherwise, call `task.EnqueueTaskCompletion` with:
     - `Kind: KindMonitor`
     - `OriginatingToolName: "monitor"`
     - `OriginatingToolCallID: <id of the original monitor tool call>` (so the Assistant ToolCall ID is consistent across all events from this monitor — the agent can correlate)
     - `Content: <newline-joined matched lines, header line indicating how many lines were matched in this window>`
     - `Status: "monitor-event"` (a NEW status — see D11)
   - Clear `batchedEvents`.
4. When `max_events` worth of notifications have been fired, OR the subprocess exits, OR `taskstop` is invoked:
   - Cancel the coalesce ticker.
   - Drain any remaining matched lines into one final notification.
   - Emit a terminal notification with `Status: completed | killed | failed` and a summary line ("Monitor stopped: stream ended" / "...: max_events reached" / "...: killed by taskstop").

*Why coalesce over event-cap-only:* a 100-event burst at startup and steady silence after is the common case; coalescing collapses the burst to one or two notifications without dropping events. The `max_events` cap is a separate safety net for runaway monitors.

*Why merge stdout and stderr:* monitor's job is to surface signal from a process; the agent rarely cares which stream a line came from, and dual scanning would double the implementation surface for little gain.

### D9. `notified` dedupe flag

Each `Task` has `Notified atomic.Bool`. The flag is set:

- By a tool's spawn path when the ack ToolResult itself carries the final output (a race-rare case for very fast subprocesses that exit before the goroutine starts waiting).
- By `EnqueueTaskCompletion` immediately after writing the synthetic pair (so a redundant Enqueue call — e.g. a Monitor that fires `monitor-event` and then immediately exits — does not double-notify).

`EnqueueTaskCompletion` first CAS-flips the flag from `false` to `true`. If the CAS fails (already true), the call returns silently without writing messages or starting `agent.Run`. This is the same pattern Claude Code uses at `LocalShellTask.tsx:106-119`.

*Caveat for monitor:* monitor-event notifications must NOT set `notified` — they're per-event, not terminal. Only the terminal completion (status `completed`/`killed`/`failed`) sets the flag. Implementation: `EnqueueTaskCompletion` only honors `SuppressIfNotified` for terminal statuses; monitor-event injections always write and never gate.

### D10. Bridge filter on `synthetic`

The bridge's tool-update indicator emission lives in `internal/bridge/service/dispatch.go` (the parts demux). It currently flows EVERY `message.ContentPart` of type `ToolCall` to the outbound indicator path. Add a guard:

```go
// In the parts handler:
if part.Type == message.ToolCallType && msg.Synthetic {
    continue // synthetic spawn marker — no chat indicator
}
```

`msg.Synthetic` is populated from the new `messages.synthetic` column. The message Service's read paths already hydrate messages from the DB; sqlc-regenerated bindings expose the new column on `message.Message`.

*What this does NOT change:* the agent's NEXT real assistant message (the human-readable reaction to the completion) still flows to chat unchanged. The bridge only suppresses the indicator emission for synthetic-flagged messages, not their consumption by the message broker.

### D11. New `Status` value `"monitor-event"`

`internal/task/status.go` defines:

```go
type Status string
const (
    StatusCompleted   Status = "completed"
    StatusFailed      Status = "failed"
    StatusKilled      Status = "killed"
    StatusMonitorEvent Status = "monitor-event" // non-terminal per-event notification from monitor
)
```

`monitor-event` is the only non-terminal status. `EnqueueTaskCompletion` treats it specially in two places:

- Does not set the task's `Notified` flag.
- Does not flip the task's `State` to a terminal value.

### D12. Cron migration

In `internal/cron/scheduler.go`:

```go
// BEFORE
func (s *Scheduler) writeSyntheticMessages(ctx context.Context, job CronJob, callID, inputJSON, resultContent string) error {
    _, _, err := s.messages.CreatePair(ctx, job.SessionID, /* assistant+tool */)
    return err
}

// AFTER
func (s *Scheduler) writeSyntheticMessages(ctx context.Context, job CronJob, callID, inputJSON, resultContent string) error {
    return task.EnqueueTaskCompletion(ctx, task.CompletionInput{
        SessionID:             job.SessionID,
        OriginatingToolCallID: callID,
        OriginatingToolName:   "task",  // cron always invokes the task tool
        TaskID:                job.ID,
        Kind:                  task.KindCron,
        Status:                task.StatusCompleted,
        Content:               resultContent,
        SuppressIfNotified:    false,    // cron jobs don't use the notified flag
    })
}
```

The session-busy lock that cron currently holds remains — the `EnqueueTaskCompletion` primitive does NOT acquire it (cron owns that responsibility because it has its own "did the user start typing while we were running" check). For non-cron callers (bash, task, monitor), `EnqueueTaskCompletion` performs its own session-busy probe via `agent.IsSessionBusy(sessionID)` to decide whether to start `agent.Run`.

The cron test suite (`internal/cron/cron_test.go`) is the regression net — same end-state, same observable messages, same auto-continue behavior. Cron should pass its existing tests without changes.

### D13. DB migration for `messages.synthetic`

```sql
-- internal/db/sql/migrations/00NN_messages_synthetic.sql
ALTER TABLE messages ADD COLUMN synthetic BOOLEAN NOT NULL DEFAULT FALSE;
```

Applies to both the SQLite and MySQL schemas. sqlc regeneration updates `internal/db/messages.sql.go` and the `message.Message` Go struct gains a `Synthetic bool` field.

`messages.CreatePair` gets an optional `Synthetic bool` parameter (default false). `EnqueueTaskCompletion` calls it with `Synthetic: true`. All existing callsites (the agent's real tool-use writes) pass `Synthetic: false` and are unaffected by the column default.

*Why a column not metadata JSON:* the bridge filter (`D10`) reads this on every parts event. A column is index-friendly and avoids JSON parse on the hot path. The column also makes "show me all synthetic messages" a one-line SQL query for debugging.

## Risks / Trade-offs

- **In-memory registry vs. K8s restart.** Tasks spawned in a pod that gets evicted vanish silently — the agent sees the original ack but never a completion. Mitigation: the ack's wording explicitly says "you will receive a synthetic tool result when it completes" so a noticing-the-silence agent (or a "still running?" follow-up tool call to `tasklist`) can self-correct. Future change can add DB-backed metadata if this stings.

- **Synthetic Assistant message in transcript can confuse readers.** A developer reading session logs may see a "bash" tool call they don't remember the agent making. Mitigation: the `synthetic` column makes filtering trivial (SQL, transcript exporter, TUI dim color). All transcript-export paths should respect the flag.

- **Background bash bypasses the 600s timeout cap.** A misbehaving subprocess can run until the pod dies. Mitigation: `taskstop` exists as the kill switch. The pod's `activeDeadlineSeconds` (already set for `opencode serve` mode) provides a hard ceiling. Permission gate at spawn time keeps the human or `permissionMode: allow` in the loop.

- **Coalesce window adds latency.** A 5s default means an "ERROR" line in the matched output is visible to the agent up to 5s late. For interactive debugging this is fine; for hot-path alerts it's a tradeoff. Mitigation: `min_interval_ms` is configurable per monitor call; an alert-y monitor can set it to 500ms.

- **Auto-continue can cascade.** A background task completion auto-starts a turn that itself spawns more background tasks. Mitigation: each auto-resumed `agent.Run` is still bound by `maxTurns` budget. The cascade depth is naturally limited.

- **Bridge filter relies on `synthetic` column being correctly set.** A bug in EnqueueTaskCompletion (forgetting `Synthetic: true`) would leak indicators to chat. Mitigation: a single chokepoint (`messages.CreatePair` with explicit param) makes this a unit-test-able invariant. Tests in `internal/task/...` and `internal/bridge/...` verify both ends.

- **Cron tests are the regression net for D12.** If we accidentally drop a behavior (e.g., cron-fired completions stop triggering auto-resume), cron's existing tests should catch it. Mitigation: explicit task in `tasks.md` to run `go test ./internal/cron/...` after migration and confirm green without changes.

- **`monitor` permission default `ask`.** First-use monitor calls in headless mode require `permissionMode: allow` (which c2-agent already sets). If a deployment forgets, monitor calls silently fail with a permission deny. Mitigation: clear error message; CHANGELOG entry calling out the new permission key.

- **Dedupe race on very fast subprocesses.** A bash command that exits before its `cmd.Wait` goroutine reaches the registry could in principle finish twice (once via the synchronous spawn path's ack, once via the goroutine). The CAS on `Notified` makes this race benign. Tested by a deliberate `bash --run_in_background "echo hi"` integration test.

## Migration Plan

1. Land DB migration first (D13). New column defaults to false; existing rows safe.
2. Land `internal/task/` package with `Registry`, `Task`, `EnqueueTaskCompletion`, `Kind`, `Status`. Unit tests cover idle/busy session paths and the `notified` flag.
3. Migrate cron (D12). Cron tests stay green.
4. Add `bash` `run_in_background` (D6). Targeted integration test using a sleep+echo script.
5. Add `task` `async` (D7). Subagent integration test.
6. Add `tasklist` and `taskstop` tools. Read-only first, then kill.
7. Add `monitor` (D8). Tail-a-log integration test.
8. Wire bridge filter (D10). Bridge tests verify synthetic flag suppresses indicator.
9. Update tool prompts: `bash` description mentions `run_in_background`; `task` mentions `async`; sleep-loop language discouraged where applicable; agent's main prompt gains a paragraph about background tasks and auto-resume.
10. Update CHANGELOG and `docs/`.

Each step is independent and unit-testable; no big-bang merge. The change is purely additive to the public API surface (new tools, new parameters, new column) so partial-deploy half-state is safe.

## Open Questions

1. **Should `taskstop` fire `status: killed` synchronously (return after the kill+notify) or async (return immediately, notify shortly after)?** Sync is simpler; async matches the rest of the background pattern. Default to sync for v1 — kill is fast and the symmetry isn't worth the extra goroutine.
2. **Should `tasklist` enumerate tasks from OTHER sessions belonging to the same project, or only the caller's session?** Default to caller's session only (least surprise); cross-session listing can come later when there's a use case.
3. **Should the `agent.Run` triggered by auto-continue (D1) preserve the parent's `maxTurns` budget, or get a fresh one?** Lean toward fresh budget per resume — otherwise long-running observation loops would die mid-stream. Decide during implementation; covered by an integration test that asserts the second auto-resumed run has at least N more turns available.
4. **TUI display of synthetic messages.** Dim color? Italic? "(background)" suffix? Defer to the TUI implementer's taste; not blocking.
