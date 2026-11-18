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

### Requirement: Reports are consumed between complete tool batches

An active run SHALL continue independent tool calls without waiting for unrelated
background tasks. After persisting the current Assistant tool-call message and all
its Tool results, the runtime SHALL append newly committed synthetic reports and
queued user input to the next model request. Each report SHALL be consumed once by
that run. Reports MUST NOT split an outstanding tool-call/result batch.

#### Scenario: Report arrives during independent work

- **GIVEN** a monitor is running while the model executes an unrelated tool batch
- **WHEN** a monitor-event pair commits during that batch
- **THEN** the independent batch completes normally
- **AND** the next model request includes the complete batch followed by the new synthetic pair
- **AND** later requests MUST NOT append a duplicate of the same report

### Requirement: Non-interactive waits wake on the first report or user input

When `agent.Service.RunWith` uses `RunOptions{NonInteractive: true}`, a natural
terminal turn with pending background work SHALL suspend generation until the first
unconsumed background report or queued user message. An explicit `yield` SHALL enter
the same event wait without a success claim or `struct_output` (see `yield-tool`).
All flow steps, including interactive flow steps, use this non-interactive runtime
mode. Interactive flow input is delivered through the step session's inbox.

Explicit `permission_denied`, `canceled` and `error` finish reasons SHALL end the run
before consuming queued reviewer input or entering a background report wait. Pending
reports MUST NOT turn these stop outcomes into another model call or a natural
completion. This does not change the existing report-driven re-entry after
`max_tokens` or the turn-budget exhaustion behavior.

Waking MUST NOT require all tasks to terminate. A monitor-event SHALL resume the
model while its subprocess is still running; completion of one task SHALL resume
the model while other tasks remain active. The runtime SHALL subscribe to session
report and inbox notifications before checking durable history and pending tasks,
so a report or user message racing with the wait cannot be lost. Already available
reports/input and an empty pending set SHALL avoid blocking. Background report waits
are scoped to the exact session and retain its process-global run slot.

A successful final return from a non-interactive run MUST still require zero pending
tasks for that session and consumption of newly committed reports/input. An early
`struct_output` MUST NOT bypass this guarantee. After processing a report, the model
may perform independent work, stop a watch it no longer needs, or yield again. The
pending set SHALL be checked again on subsequent terminal turns, including tasks
spawned in later cycles. Cancellation, turn-budget exhaustion and other run failures
can stop observation with tasks still pending; they MUST NOT be described as success.

Foreground sleep interception remains a separate compatibility guard. When the
session or its direct children have pending non-monitor tasks, a foreground bash
command beginning with a wall-clock wait SHALL be redirected to `WaitForActiveTasks`
with `ScopeSessionAndChildren`, as defined in `bash-background-mode`. That primitive
waits for all tasks in its snapshot to terminate. It MUST NOT be confused with the
first-report wait used by `yield` and natural non-interactive terminal turns.

Report waits MUST use the surrounding `ctx` as their deadline, with no additional
internal timeout. Flow callers derive that context from `Step.Timeout` or
`OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` (see `flow-runtime-resume`). Stall
detection SHALL continue alongside the wait and MAY terminate stalled task-kind
subagents as defined in `background-task-stall-detection`; it MUST NOT treat a silent
monitor as stalled. Periodic progress logs SHALL name still-pending tasks by ID, kind
and age without imposing a deadline. No progress log is needed for an immediate return.

#### Scenario: Composition rejection with a pending monitor report

- **GIVEN** a monitor report commits while a composition child awaits permission
- **WHEN** the permission request is rejected
- **THEN** the run SHALL finish with `permission_denied` without another model call
- **AND** the rejected child SHALL NOT execute, the monitor SHALL remain active, and queued reviewer input SHALL remain queued

#### Scenario: Flow step waits for background bash before returning struct_output

- **WHEN** a non-interactive flow step starts background bash and emits an early `struct_output`
- **THEN** the run MUST NOT return successfully while that bash remains pending
- **AND** its completion report MUST resume generation so the model can incorporate the result
- **AND** the final output delivered to the flow runner MUST reflect the response after the completion report

#### Scenario: One task reports while another remains active

- **GIVEN** two background tasks are pending when the model emits a natural `end_turn`
- **WHEN** one task commits a completion report
- **THEN** the run MUST resume the model with that report while the other task remains running
- **AND** a later successful final return MUST still require the other task to finish or be stopped

#### Scenario: Monitor event wakes before subprocess exit

- **GIVEN** a flow run is waiting on a streaming monitor
- **WHEN** a non-terminal monitor-event pair commits
- **THEN** the run MUST resume with the report while the monitor remains `running`
- **AND** it MUST NOT kill the monitor merely to wake the model

#### Scenario: Queued user input wakes a flow wait

- **GIVEN** an interactive flow step is running with `NonInteractive: true` and a pending task
- **WHEN** reviewer input enters its session inbox while generation is suspended
- **THEN** the existing run MUST resume with that input without waiting for task completion
- **AND** it MUST retain the step's agent, tools and output schema

#### Scenario: Report races with entry into the wait

- **WHEN** a report commits before or during the wait's history/pending checks
- **THEN** the runtime MUST observe it from durable history or the subscribed session channel
- **AND** it MUST NOT require a second report to wake

#### Scenario: Model attempts to self-poll with sleep while tasks pending

- **GIVEN** a non-interactive flow step has pending non-monitor tasks
- **WHEN** the model issues foreground `bash sleep N` with an optional trailer
- **THEN** the runtime MUST redirect the sleep to the snapshot terminal-task wait
- **AND** the result MUST describe the completed tasks and run any permitted trailer after waiting

#### Scenario: Tasks spawned across multiple cycles remain pending

- **GIVEN** a run processes a first report and then spawns additional background work
- **WHEN** the model next ends its turn
- **THEN** the runtime MUST re-check the current pending set
- **AND** it MUST NOT return successfully while any exact-session task remains pending

#### Scenario: Event cap does not bypass final completion

- **WHEN** a flow step starts a monitor with `max_events: 1` and yields
- **THEN** its monitor-event MAY wake the model before the terminal report
- **AND** final success MUST still wait for no pending tasks and consumption of the terminal report

#### Scenario: Interactive run releases instead of holding

- **GIVEN** a TUI or bridge run uses `NonInteractive: false` and has pending background work
- **WHEN** the model emits `end_turn` or calls `yield`
- **THEN** the run SHALL release its session slot
- **AND** user input can start a new turn
- **AND** a later background report SHALL use the existing idle auto-resume path
- **AND** foreground sleep SHALL retain its existing interactive behavior

#### Scenario: Context deadline bounds a natural end-of-turn wait

- **GIVEN** the run has a context deadline and pending tasks
- **WHEN** that context expires during a natural end-of-turn wait
- **THEN** the runtime MUST record the synthetic wait-timeout note for the pending tasks
- **AND** stop waiting with the latest `AgentEvent`, without claiming those tasks succeeded

#### Scenario: Progress logs do not bound the wait

- **GIVEN** a pending task has produced no report or inbox input for several progress intervals
- **AND** the surrounding context has no deadline
- **THEN** the wait SHALL keep logging the pending task's ID, kind and age
- **AND** logging SHALL NOT end the wait

#### Scenario: Stalled subagent reports while other tasks remain active

- **GIVEN** one task-kind subagent has exceeded `backgroundTasks.stallThreshold`
- **WHEN** stall handling terminates it and writes its completion report
- **THEN** the report SHALL wake the model without requiring other active tasks to terminate

#### Scenario: Healthy silent monitor keeps waiting

- **GIVEN** the only pending task is a monitor whose pattern has not matched
- **AND** there is no inbox input or context deadline
- **THEN** the runtime SHALL keep waiting for a report or user input
- **AND** silence SHALL NOT trigger stall termination

### Requirement: Task registry exposes a wait primitive

The `task.Registry` interface SHALL expose these methods:

```
PendingForSession(sessionID string, filter func(*Task) bool) []*Task
PendingForSessionTree(sessionID string, filter func(*Task) bool) []*Task
WaitForActiveTasks(ctx context.Context, sessionID string, opts WaitOptions) error
```

Where `WaitOptions` is:

```
type WaitOptions struct {
    IncludeMonitor bool // default true in non-interactive mode (see monitor-tool spec)
    Scope          WaitScope // ScopeExactSession (zero value) | ScopeSessionAndChildren
}
```

`Scope` selects which tasks the snapshot contains. `ScopeExactSession` — the zero value, and therefore the behavior of every existing caller — includes only tasks whose owning session equals `sessionID`. `ScopeSessionAndChildren` additionally includes tasks owned by sessions whose parent is `sessionID` (one level of descent; NOT the flow `root_session_id`, which is shared by every step of a flow including parallel siblings).

`Scope` MUST be honored by BOTH the pending lookup and the internal re-snapshot inside `WaitForActiveTasks`. A caller that widens its pre-check without widening the wait would return immediately having waited for nothing, while reporting tasks it never observed.

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

#### Scenario: Scope defaults to exact session

- **WHEN** `WaitForActiveTasks` is called with a zero-value `WaitOptions`
- **THEN** only tasks owned by `sessionID` itself are included, identical to prior behavior

#### Scenario: Child-owned tasks are included under ScopeSessionAndChildren

- **GIVEN** a subagent session whose parent is `S` has one running bash task
- **WHEN** `WaitForActiveTasks(ctx, "S", WaitOptions{Scope: ScopeSessionAndChildren})` is called
- **THEN** the wait blocks on the child's task

#### Scenario: Parallel sibling sessions are NOT included

- **GIVEN** sessions `A` and `B` are sibling steps of one flow, sharing a `root_session_id`
- **AND** `B` has a running bash task
- **WHEN** `WaitForActiveTasks(ctx, "A", WaitOptions{Scope: ScopeSessionAndChildren})` is called
- **THEN** `B`'s task MUST NOT be included in the wait set

### Requirement: Registry exposes exact-session report notifications

The registry SHALL expose `WatchSession(sessionID) <-chan struct{}` and
`NotifySession(sessionID)`. Notification SHALL wake all current subscribers for
exactly that session; a new subscription SHALL wait for the next notification.
Consumers MUST subscribe before inspecting durable history and pending work.
These channels are wake signals; committed messages remain the source of report
content. They SHALL wake on non-terminal monitor events as well as completions.
`WaitForActiveTasks` SHALL retain its separate snapshot terminal-state semantics.

#### Scenario: Watcher wakes on an intermediate report

- **GIVEN** a session watcher was installed before a monitor spawned
- **WHEN** the monitor commits an event without exiting
- **THEN** the watcher SHALL wake while the task remains pending
- **AND** watchers of another session SHALL remain blocked

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
- **AND** the ack MUST direct the idle agent to `yield` alone and explain that reports arrive automatically

### Requirement: Registry exposes a session-and-children scoped pending lookup

The task registry SHALL expose a pending-task lookup covering the queried session **plus
sessions whose parent is that session** (one level of descent), alongside the existing
exact-session lookup.

The scope is deliberately NOT the flow `root_session_id`. A flow assigns one root to every
step and steps run concurrently, so root scope would let a `sleep` in one parallel branch
block on an unrelated branch's background work. One level of descent covers the
parent-agent → subagent case without that blast radius; deeper nesting degrades to
exact-session behavior, which is the pre-existing behavior and therefore not a regression.

Two lookups exist with distinct, non-interchangeable callers:

| Caller | Scope | Rationale |
|---|---|---|
| End-of-turn drain (`agent.Run` non-interactive hold) | exact session | A parent MUST NOT block at end of turn on a child's tasks. |
| Foreground-wait redirect (`bash` sleep interception) | session + direct children | The model explicitly asked to wait; its own children's pending work is what it is waiting for. |

Each task SHALL record the parent of its owning session at registration, and the lookup
matches `SessionID == S || ParentSessionID == S`. The caller's own ID is already on the
tool-execution context; what the tool layer cannot derive is the owning session's
*parent*, because `internal/llm/tools` holds no session service. The runtime SHALL
therefore place the owning session's `ParentSessionID` on the tool-execution context
alongside the non-interactive marker, at the same site where the session row is already
loaded. It MUST be the session's parent, NOT its `RootSessionID`: for a flow step's
subagent the two differ, and stamping the root would reintroduce the flow-wide scope this
requirement rejects. An implementation that omits this plumbing degrades the lookup to
exact scope silently; a test MUST cover the plumbing, not only the registry method.

Introducing this lookup MUST NOT widen the exact-session report wait or final pending check.

#### Scenario: Lookup spans parent and its direct child sessions

- **WHEN** a subagent session whose parent is `S` has one running bash task
- **AND** the lookup is called with `S`
- **THEN** the subagent's task is returned

#### Scenario: Parallel sibling sessions are excluded

- **GIVEN** sessions `A` and `B` are sibling steps of one flow sharing a `root_session_id`
- **AND** `B` has a running bash task
- **WHEN** the lookup is called with `A`
- **THEN** `B`'s task is NOT returned

#### Scenario: A session with no children degrades to exact scope

- **WHEN** the lookup is called with a session that has no child sessions
- **THEN** it returns exactly what the exact-session lookup returns

#### Scenario: Drain scope is unchanged

- **WHEN** a parent session's turn ends while only a child session's task is running
- **THEN** the parent's end-of-turn drain does NOT wait on it

### Requirement: Subagents inherit the caller's non-interactive marker

A subagent launched by the `task` tool — synchronously or with `async: true` — SHALL run with `RunOptions{NonInteractive: m}` where `m` is the non-interactive marker on the tool-execution context of the turn that invoked the tool. The async path SHALL read `m` from that caller context, not from the detached run context it derives for the subagent (which carries no values). No other run option is inherited. Under a non-interactive run the subagent therefore holds its own turn open until its background tasks reach a terminal state (the end-of-turn drain of this spec, exact-session scope, stall detection included) and has its foreground wall-clock waits redirected (`bash-background-mode`); under an interactive run its behaviour is unchanged.

#### Scenario: Flow-step subagent drains before answering

- **GIVEN** a flow step (non-interactive) whose agent calls the `task` tool
- **AND** the subagent starts a `bash run_in_background` task that takes 3 minutes and emits its terminal turn after 3 seconds
- **THEN** the subagent's run does not return until that task reaches a terminal state and the subagent has reacted to the completion
- **AND** the parent's `task` call returns the subagent's post-completion answer

#### Scenario: Flow-step subagent's sleep is redirected

- **GIVEN** the same step, and the subagent has a pending `bash run_in_background` task
- **WHEN** the subagent calls `bash` (foreground) with `sleep 60; cat /tmp/log`
- **THEN** the sleep is redirected to the task wait exactly as for the step's primary agent

#### Scenario: Interactive subagent is unchanged

- **GIVEN** an interactive TUI turn (no non-interactive marker) that spawns a subagent
- **THEN** the subagent runs interactive: its turn ends immediately after a background spawn and its foreground sleeps run verbatim

