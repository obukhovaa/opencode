# Background Tasks

## Purpose

Defines the per-process in-memory task registry that backs opencode's event-driven background work. Three tool families spawn background tasks — `bash run_in_background`, `task async`, and `monitor` — and the cron scheduler also produces synthetic completions through the same primitive. Every running task lives only in memory: opencode restart drops every in-flight task with no recovery. The registry is responsible for assigning unique `task_id`s of the form `<kind>_<base32(16-byte-random)>`, maintaining the strict `running → completed|failed|killed` state machine, owning the per-task output file at `<config.Data.Directory>/tasks/<task_id>.out`, sweeping orphan output files at boot, and gating duplicate terminal notifications via a per-task `notified atomic.Bool` flag.

## Requirements

### Requirement: Per-process in-memory task registry
The system SHALL maintain a process-global, in-memory registry of background tasks keyed by `task_id`. The registry SHALL be the single source of truth for the lifecycle state of every running, completed, killed, or failed background task. The registry MUST NOT persist its contents to any durable store; opencode restart loses all in-flight tasks.

#### Scenario: Task is registered at spawn
- **WHEN** a tool spawns a background task (bash `run_in_background`, task `async`, monitor, cron-fired completion)
- **THEN** the spawning code path registers a `Task` entry in the registry containing `id`, `session_id`, `kind`, initial `state = running`, `started_at`, `output_path`, `originating_tool_call_id`, and a lifecycle handle (`*os.Process` for shell-spawned, `context.CancelFunc` for subagent-spawned)

#### Scenario: Restart drops in-flight tasks
- **WHEN** opencode restarts while one or more tasks have state `running`
- **THEN** the registry is empty after boot and no completion notification is injected for the lost tasks; the agent's session still contains the original spawn ack message referencing the now-orphaned `task_id`

#### Scenario: Lookup by id
- **WHEN** a caller invokes `Registry.Get(taskID)`
- **THEN** the caller receives the `Task` entry and an existence boolean; concurrent lookups are safe via internal `sync.RWMutex`

#### Scenario: Per-session listing
- **WHEN** a caller invokes `Registry.ListBySession(sessionID)`
- **THEN** the caller receives every `Task` whose `session_id` matches, regardless of state, in registration order

### Requirement: Task ID format
Task IDs SHALL be of the form `<kind>_<base32(16-byte-random)>`, where `kind` is one of `shell`, `agent`, `monitor`, or `cron`, lowercase. The random component MUST be cryptographically random and base32-encoded without padding. IDs MUST be globally unique within a process; the registry MUST refuse to register a duplicate.

#### Scenario: Bash background mode produces a `shell_` ID
- **WHEN** the bash tool is invoked with `run_in_background: true`
- **THEN** the ack ToolResult contains a `task_id` matching `^shell_[A-Z2-7]+$` (16 random bytes → 26 base32 chars)

#### Scenario: Task async mode produces an `agent_` ID
- **WHEN** the task tool is invoked with `async: true`
- **THEN** the ack ToolResult contains a `task_id` matching `^agent_[A-Z2-7]+$`

#### Scenario: Monitor produces a `monitor_` ID
- **WHEN** the monitor tool is invoked
- **THEN** the ack ToolResult contains a `task_id` matching `^monitor_[A-Z2-7]+$`

### Requirement: Output file location and creation
Every background task SHALL be assigned a unique output file path under `<config.Data.Directory>/tasks/<task_id>.out`. The `tasks/` subdirectory MUST be created on demand by the registry (`MkdirAll`, mode 0o700). The output file MUST be created exclusively (O_CREATE|O_EXCL) so two tasks with the same ID cannot share a file.

#### Scenario: First background task creates the tasks directory
- **WHEN** the first background task in a fresh data directory is spawned
- **THEN** the directory `<config.Data.Directory>/tasks/` exists with mode 0o700 and the task's output file `<task_id>.out` is open for writing

#### Scenario: Output file is co-located with the data directory
- **WHEN** `.opencode.json` sets `data.directory` to `/var/lib/myproject/.opencode`
- **THEN** task output files are created under `/var/lib/myproject/.opencode/tasks/<task_id>.out`

### Requirement: Task lifecycle states
A task SHALL transition through a strict state machine: `running → completed | failed | killed`. The non-terminal `monitor-event` notification status MUST NOT be a registry state — it is a per-event injection from a monitor task that remains in state `running`. Terminal-state transitions are one-way; a `completed` task cannot revert to `running`.

#### Scenario: Subprocess exits cleanly
- **WHEN** a `shell_*` or `monitor_*` task's subprocess exits with code 0
- **THEN** the registry transitions the task from `running` to `completed` and records `finished_at` and `exit_code: 0`

#### Scenario: Subprocess exits non-zero
- **WHEN** a `shell_*` or `monitor_*` task's subprocess exits with code != 0
- **THEN** the registry transitions the task to `failed` and records `finished_at` and the exit code

#### Scenario: Subagent panics or errors
- **WHEN** an `agent_*` task's subagent run returns an error (or the agent goroutine panics)
- **THEN** the registry transitions the task to `failed` and records `finished_at`; no exit code is set

#### Scenario: taskstop terminates a running task
- **WHEN** the `taskstop` tool is called against a `running` task
- **THEN** the registry signals (SIGTERM for subprocess, `Cancel()` for subagent), transitions the task to `killed`, records `finished_at`, and the originating tool's monitor goroutine fires a `status: killed` completion notification via the task-notifications primitive

### Requirement: Per-task `notified` dedupe flag
Each task SHALL carry a `notified atomic.Bool` flag, initialized to `false`. The `EnqueueTaskCompletion` primitive MUST compare-and-swap this flag from `false` to `true` before writing a TERMINAL synthetic pair (`status: completed | failed | killed`). If the CAS fails (flag already true), the call returns silently without writing messages or starting `agent.Run`. The `monitor-event` status MUST NOT set or check this flag.

#### Scenario: Fast task whose ack carried output is not re-notified
- **WHEN** a bash background subprocess exits before the spawn goroutine reaches `cmd.Wait`, and the spawn ack ToolResult already carried the final output (atomically set `notified = true`)
- **THEN** the subsequent monitor goroutine's call to `EnqueueTaskCompletion` observes `notified == true`, returns silently, and no duplicate completion pair appears in the message log

#### Scenario: Monitor-event injections are never gated by notified
- **WHEN** a running monitor task fires consecutive `monitor-event` notifications
- **THEN** each call writes its synthetic pair regardless of the flag's value; the flag is neither read nor mutated by monitor-event injections

### Requirement: Orphan output cleanup at boot
At opencode startup, before any session activates, the registry SHALL sweep `<config.Data.Directory>/tasks/` and delete every `*.out` file that does not correspond to a registered (live) task. Since the registry is in-memory only, this cleanup deletes ALL output files on every boot.

#### Scenario: Long-lived data directory accumulates output between runs
- **WHEN** opencode boots and `<data.dir>/tasks/` contains output files from a previous session (file timestamps older than process start time)
- **THEN** the registry deletes all such files; the directory may remain (empty), or may be removed if empty

#### Scenario: K8s ephemeral pod
- **WHEN** opencode boots in a fresh pod with no prior `<data.dir>/tasks/`
- **THEN** the sweep is a no-op; the directory is created on first task spawn

### Requirement: Output writing concurrency
The subprocess writing to the task output file SHALL append in a single goroutine (`cmd.Stdout = f; cmd.Stderr = f`). Concurrent agent `Read` of the same file MUST be safe — the agent reads the file at the file system's consistency level (best-effort recent bytes; no locking required). The file MUST be flushed before the synthetic completion pair is written to the message log.

#### Scenario: Agent reads in-progress output
- **WHEN** the agent calls `Read` on `<data.dir>/tasks/<task_id>.out` while the subprocess is still running
- **THEN** the read returns whatever bytes have been flushed so far; partial-line reads are acceptable

#### Scenario: Output flushed before completion notification
- **WHEN** a background subprocess exits and `EnqueueTaskCompletion` is about to be called
- **THEN** the spawn goroutine SHALL `Sync()` the output file before invoking `EnqueueTaskCompletion`, so a subsequent agent `Read` triggered by the completion notification sees the full output

### Requirement: Non-interactive `agent.Run` MUST hold the turn until pending background tasks complete

When `agent.Service.RunWith` is invoked with `RunOptions{NonInteractive: true}`, the runtime SHALL NOT return until every running background task associated with the session (regardless of `Kind` — bash, task, AND monitor) has reached a terminal state (`StateCompleted`, `StateFailed`, or `StateKilled`), or until the surrounding `ctx` is cancelled.

This guarantee MUST NOT be bypassable by model behavior. It is enforced through two complementary mechanisms:

1. **End-of-turn drain.** After the model emits a terminal turn (`end_turn` or `struct_output`) for the current agentic cycle, and BEFORE the `AgentEvent` is delivered to the caller, the runtime calls `WaitForActiveTasks`. On a `nil` return the runtime re-reads the session's pending tasks and, if any remain (e.g. tasks spawned in a later cycle after an earlier wait's snapshot), waits again — looping until the session has zero pending tasks or `ctx` is cancelled. After each successful wait the runtime reloads the session's message history and re-enters the agentic loop for at least one additional cycle so the model can react to the just-arrived synthetic completion(s). The `WaitForActiveTasks` primitive keeps its snapshot-at-start semantics; the drain loop lives in the agent.

2. **Anti-spin.** While the session has pending non-monitor background tasks (bash or task), the runtime SHALL NOT allow the model to consume wall-clock time in a foreground self-wait. The canonical case — a foreground `bash` command whose sole effect is to sleep — MUST be redirected to `WaitForActiveTasks` rather than executed as a sleep (see `bash-background-mode`). This ensures the guarantee holds even when the model never voluntarily emits a terminal turn but instead attempts to poll. (Long-lived monitors are excluded from the redirect; they are bounded by the end-of-turn drain above, not by a mid-turn sleep.)

The wait MUST NOT impose its own timeout — the surrounding `ctx` is the sole deadline source. See `flow-runtime-resume` for how callers derive the ctx deadline from `Step.Timeout` and the `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` env var.

#### Scenario: Flow step waits for background bash before returning struct_output

- **WHEN** a flow step invokes `agent.RunWith(..., RunOptions{NonInteractive: true})`
- **AND** the model calls `bash` with `run_in_background: true` mid-turn
- **AND** the model then emits `struct_output` for the step
- **THEN** `agent.RunWith` MUST NOT return immediately
- **AND** the runtime MUST wait for the background bash subprocess to exit and write its synthetic completion pair into the session
- **AND** the runtime MUST re-enter the agentic loop so the model can observe the synthetic Tool result
- **AND** the `AgentEvent.StructOutput` returned to the flow runner MUST reflect the model's response generated AFTER the synthetic completion arrived

#### Scenario: Model attempts to self-poll with sleep while tasks pending

- **GIVEN** a non-interactive flow step has spawned one or more `task async` subagents that are still running
- **WHEN** the model, instead of emitting a terminal turn, issues a foreground `bash` command whose sole effect is `sleep N` (optionally followed by an `echo`)
- **THEN** the runtime MUST NOT execute the sleep
- **AND** the runtime MUST instead wait for the pending background tasks to reach terminal state (bounded by the surrounding `ctx`)
- **AND** the tool result returned to the model MUST summarize the tasks that completed during the wait
- **AND** no foreground process SHALL consume the requested sleep duration

#### Scenario: Drain covers tasks spawned across multiple turns

- **GIVEN** a non-interactive step's agent spawns a first wave of async subagents, then in a later cycle spawns a second wave
- **WHEN** the runtime enters the end-of-turn wait after the first wave and that wave completes
- **THEN** the runtime MUST re-check pending tasks and observe the second wave
- **AND** the runtime MUST wait again until the session has zero pending tasks or `ctx` is cancelled
- **AND** `agent.RunWith` MUST NOT return while any spawned task for the session is still running

#### Scenario: Flow step waits for monitor with max_events to terminate

- **WHEN** a flow step's agent spawns `monitor` with `max_events: 1` pattern matching an expected event
- **AND** the agent emits `struct_output`
- **THEN** the runtime MUST wait until the monitor reaches a terminal state (event matched + max_events triggers SIGTERM, OR subprocess exits naturally, OR taskstop)
- **AND** the final struct_output delivered to the flow runner MUST reflect the post-monitor-completion response

#### Scenario: Interactive agent.Run is unaffected

- **WHEN** `agent.Run` is invoked (the original 4-arg form, or `RunWith` with `NonInteractive: false`)
- **AND** the model spawns a background bash task
- **AND** the model then emits `end_turn`
- **THEN** `agent.Run` MUST return as today (synchronously after the inner agentic loop exits)
- **AND** a foreground `bash sleep` MUST execute normally (no anti-spin redirection)
- **AND** the background task's eventual synthetic completion MUST trigger a fresh `agent.Run` via `task.deps.ResumeSession` as today

#### Scenario: Wait respects the surrounding context deadline

- **GIVEN** the caller passes a context with a 30-second deadline (e.g. `flow.Service` wrapped step.Timeout)
- **WHEN** the background task is still running at the 30-second mark
- **THEN** the wait MUST unblock with `ctx.Err()`
- **AND** the runtime MUST inject a synthetic Assistant timeout note into the session log
- **AND** the outer agentic loop MUST break
- **AND** `agent.RunWith` MUST return the latest `AgentEvent` (the pre-wait terminal turn)

### Requirement: Task registry exposes a wait primitive

The `task.Registry` interface SHALL expose two new methods:

```
PendingForSession(sessionID string, filter func(*Task) bool) []*Task
WaitForActiveTasks(ctx context.Context, sessionID string, opts WaitOptions) error
```

Where `WaitOptions` is:

```
type WaitOptions struct {
    IncludeMonitor bool // default true in non-interactive mode (see monitor-tool spec)
}
```

`WaitForActiveTasks` MUST block until every task included in the snapshot transitions to a terminal state, OR until ctx is cancelled. The implementation MUST signal completion via a per-task `done chan struct{}` closed exactly once in `Registry.MarkFinished` and `Registry.Kill`.

The wait MUST use snapshot-at-start semantics: tasks registered AFTER the wait begins are NOT included in the wait set. This keeps the contract bounded and deterministic.

#### Scenario: Wait returns when all pending tasks finish

- **GIVEN** two `bash run_in_background` tasks and one `monitor` task are running for session `S`
- **WHEN** `WaitForActiveTasks(ctx, "S", WaitOptions{IncludeMonitor: true})` is called
- **AND** all three tasks reach terminal state
- **THEN** the wait MUST return `nil` within milliseconds of the last task's completion

#### Scenario: Concurrent task registration is not retroactively waited

- **GIVEN** one task is pending and `WaitForActiveTasks` has been called
- **WHEN** a second task is `Register`'d 50ms later
- **AND** the first task completes 100ms after the wait began
- **THEN** `WaitForActiveTasks` MUST return `nil` at the 100ms mark
- **AND** the second task's lifecycle MUST NOT be observed by this wait call

### Requirement: Synthetic Assistant timeout note on wait cancellation

When the non-interactive wait returns `ctx.Err()` (the surrounding ctx was cancelled or its deadline elapsed) AND there are still tasks pending, the runtime SHALL inject a synthetic Assistant message into the session message log BEFORE breaking the outer agentic loop. The message has Role=`Assistant`, Parts=`[TextContent]`, and `Synthetic: true`.

The text body MUST enumerate every still-pending task's `task_id`, `Kind`, `started_at` (RFC3339), `output_file` path, and a short description. It MUST explicitly state that the step's terminal turn was produced WITHOUT observing these completions, and recommend that any subsequent invocation on this session inspect the `output_file` paths before re-spawning equivalent work.

This message is `Synthetic: true` so the bridge filter (`if ev.Payload.Synthetic { return }`) skips it for outbound chat indicators. Non-bridge consumers (transcript export, ACP SSE replay, the model on re-invocation) observe it as a normal assistant text turn.

#### Scenario: Timeout produces an observable note for the next invocation

- **GIVEN** a non-interactive `agent.RunWith` is waiting for a long-running bash task
- **AND** the surrounding ctx cancels after the step's timeout elapses
- **WHEN** the wait unblocks with `ctx.Err()`
- **THEN** the runtime MUST write exactly one synthetic Assistant text message into the session
- **AND** that message MUST contain the still-pending task IDs, output_file paths, kinds, and start times
- **AND** any subsequent `agent.Run` on this session MUST observe the timeout note in the message history

### Requirement: No-poll guidance is delivered independent of the agent's system prompt

The runtime guidance that background tasks are event-driven and MUST NOT be polled (i.e. the model must not `sleep` and re-check, and a synthetic completion arrives automatically) SHALL be present in the assembled system prompt for EVERY agent that has tool access (per the agent registry's `HasTools`), including agents that supply a custom system prompt via `info.Prompt`. It MUST NOT live exclusively in a base prompt (such as `CoderPrompt`) that is skipped when a custom prompt is set. Tool-less agents (e.g. summarizer, descriptor) are exempt — they cannot spawn or poll background work, so the guidance would be dead prompt weight on every title/summarize call.

This is defense-in-depth: the anti-spin enforcement (see the hold-the-turn requirement and `bash-background-mode`) makes polling harmless, but correct guidance reduces wasted cycles and duplicate task dispatch.

#### Scenario: Custom-prompt agent still receives the no-poll contract

- **GIVEN** an agent registered with a non-empty custom `info.Prompt` (e.g. `composer-developer`)
- **WHEN** the system prompt is assembled for a run
- **THEN** the assembled prompt MUST include the "background tasks are event-driven; do NOT poll/sleep; completions arrive as synthetic tool results" guidance
- **AND** this MUST hold even though the base `CoderPrompt` is not appended for a custom-prompt agent

#### Scenario: Async ack does not frame the output file as a poll target

- **WHEN** the `task async` ack is returned to the parent agent
- **THEN** the ack MAY include the `output_file` path for resume/inspection semantics
- **AND** the ack MUST NOT instruct or imply that the agent should read the output file to poll for progress
- **AND** the ack MUST state that in a non-interactive step the runtime holds the turn until the subagent completes
