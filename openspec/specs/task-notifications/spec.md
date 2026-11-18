# Task Notifications

## Purpose

Defines the single, canonical primitive for injecting background-task events into a live opencode session: `task.EnqueueTaskCompletion`. Every background-task source — `bash run_in_background`, `task async`, `monitor`, and the cron scheduler — uses this primitive. The primitive writes a synthetic `Assistant(ToolCall) + Tool(ToolResult)` pair via `messages.CreatePair`, marks both messages `synthetic = true` so the chat bridge filter suppresses outbound tool indicators, dedupes terminal completions via the per-task `notified` CAS flag, and auto-resumes the session via `agent.Run` if no run is currently in-flight. The shape mirrors what the cron scheduler already produces today, so downstream renderers (TUI, transcript exporter) need no new code paths.
## Requirements
### Requirement: `EnqueueTaskCompletion` is the single injection primitive
The system SHALL expose exactly one public function for asynchronously injecting a background-task event into a session's message log: `task.EnqueueTaskCompletion(ctx, CompletionInput)`. All callers (bash background, task async, monitor, cron) MUST use this function. No other code path is permitted to write the synthetic Assistant(ToolCall)+Tool(ToolResult) shape that this primitive produces.

#### Scenario: Cron uses the primitive
- **WHEN** the cron scheduler's `writeSyntheticMessages` fires
- **THEN** it calls `task.EnqueueTaskCompletion` with `OriginatingToolName: "task"` and `Kind: KindCron`; it does not call `messages.CreatePair` directly

#### Scenario: Bash background uses the primitive
- **WHEN** a bash subprocess started with `run_in_background: true` exits
- **THEN** the bash tool's monitor goroutine calls `task.EnqueueTaskCompletion` with `OriginatingToolName: "bash"` and `Kind: KindBash`

#### Scenario: Task async uses the primitive
- **WHEN** an async task tool's subagent run finishes
- **THEN** the task tool's monitor goroutine calls `task.EnqueueTaskCompletion` with `OriginatingToolName: "task"` and `Kind: KindTask`

#### Scenario: Monitor uses the primitive for every event
- **WHEN** a monitor task's coalesce window expires with one or more matched lines
- **THEN** the monitor's tick handler calls `task.EnqueueTaskCompletion` with `OriginatingToolName: "monitor"`, `Kind: KindMonitor`, and `Status: StatusMonitorEvent`

### Requirement: Synthetic injection shape
`EnqueueTaskCompletion` SHALL write exactly two messages to the session's message log, atomically as a single pair via `messages.CreatePair`:

1. An Assistant message containing a single `ToolCall` content part with `Name` set to the originating tool's name, the agent-shape input JSON, `Type: tool_use`, and `Finished: true`.
2. A Tool message containing a single `ToolResult` content part referencing the Assistant's ToolCall by ID and carrying the agent-facing result body.

#### Scenario: Pair is atomic
- **WHEN** `EnqueueTaskCompletion` writes the pair
- **THEN** the pair is committed in a single transaction with consecutive `seq` numbers; partial visibility (Assistant without Tool, or vice versa) is impossible to readers

#### Scenario: Renderer formats result via originating tool's renderer
- **WHEN** the TUI or transcript exporter renders the synthetic pair
- **THEN** it resolves the renderer by the `Name` field of the ToolCall; a `bash` synthetic completion renders through `bash`'s normal result renderer

### Requirement: `synthetic` flag on Assistant messages
The system SHALL persist a `synthetic` boolean column on the `messages` table, defaulting to `false`. Every Assistant message written by `EnqueueTaskCompletion` MUST set `synthetic = true`. Every existing write path (the agent's real tool-use messages, user inbound messages) MUST continue to write `synthetic = false` (the default).

#### Scenario: Real tool calls are not flagged
- **WHEN** the agent invokes a tool (any tool, synchronous or background-spawning) and that tool's response is the agent's own ToolResult
- **THEN** the Assistant message containing the agent's ToolCall has `synthetic = false`

#### Scenario: Background completion is flagged
- **WHEN** `EnqueueTaskCompletion` writes its synthetic pair
- **THEN** the Assistant message has `synthetic = true`; the Tool message MUST also carry the flag

#### Scenario: Flag is queryable
- **WHEN** a consumer (transcript exporter, bridge filter, debug query) reads messages from the DB
- **THEN** the `synthetic` field is exposed on the `message.Message` struct produced by sqlc-generated bindings

### Requirement: Auto-continue on idle session
If the target session is idle (no in-flight `agent.Run` on ANY agent instance — the busy check answers from the process-global session-run ledger, see `session-run-exclusivity`) at the time `EnqueueTaskCompletion` writes its pair, AND the completed task is not flow-owned (see "Flow-owned tasks never auto-resume"), the primitive SHALL start a fresh `agent.Run(ctx, sessionID, "", maxTurnsOverride)` immediately after the write. The empty content argument signals the agent that the new turn input is in the just-written synthetic ToolResult.

#### Scenario: Idle session auto-resumes
- **WHEN** a background bash spawned OUTSIDE a flow step completes and the bound session has no in-flight `agent.Run` on any instance
- **THEN** `EnqueueTaskCompletion` writes the synthetic pair, then invokes `agent.Run` against the same session; the agent observes the new synthetic ToolResult and produces a follow-up Assistant message

#### Scenario: Busy session is NOT re-triggered
- **WHEN** a background monitor fires a `monitor-event` while an `agent.Run` is in-flight on the bound session — on ANY agent instance, including a per-step flow agent the primary instance never sees
- **THEN** `EnqueueTaskCompletion` writes the synthetic pair but DOES NOT start a new `agent.Run`; the in-flight run consumes the pair after its current tool batch, or wakes from its non-interactive report wait

#### Scenario: Cron preserves its own busy-skip logic
- **WHEN** cron's scheduler invokes `EnqueueTaskCompletion` after the cron's existing session-busy check passed
- **THEN** the primitive proceeds normally; cron is the only caller whose busy semantics are owned externally

### Requirement: `monitor-event` is a non-terminal status
The `Status` value `StatusMonitorEvent` SHALL be treated specially by `EnqueueTaskCompletion`:
- It MUST NOT set the per-task `notified` flag.
- It MUST NOT transition the task's registry state out of `running`.
- It MAY fire arbitrarily many times for the same `task_id` during the task's lifetime.

All other statuses (`StatusCompleted`, `StatusFailed`, `StatusKilled`) are TERMINAL and MUST be subject to the `notified` CAS gate.

#### Scenario: Multiple monitor-event notifications during a single monitor's lifetime
- **WHEN** a monitor's coalesce window fires 10 times over 50 seconds, each with matched lines
- **THEN** 10 synthetic pairs are written; the task's `state` remains `running` and `notified` remains `false`

#### Scenario: Terminal status after monitor-event sequence
- **WHEN** a monitor subprocess exits after firing 10 `monitor-event` notifications
- **THEN** the final completion call is `StatusCompleted` and IS subject to `notified` CAS; the task transitions to terminal state `completed`

### Requirement: Idempotence on duplicate terminal calls
If two terminal calls to `EnqueueTaskCompletion` race for the same task ID (e.g., a kill path and a natural-exit path both fire), the `notified` CAS SHALL ensure exactly one synthetic pair is written. The losing call MUST return without error.

#### Scenario: Kill races with natural exit
- **WHEN** `taskstop` calls Kill while the subprocess is in the act of exiting; both code paths reach `EnqueueTaskCompletion` near-simultaneously
- **THEN** exactly one synthetic pair appears in the message log; the second call observes `notified == true` and returns silently

### Requirement: Commit reports before waking the session

After successfully committing a synthetic pair, `EnqueueTaskCompletion` SHALL signal
`Registry.NotifySession(sessionID)`. A terminal report SHALL also make its terminal
registry state visible before signaling. A monitor-event SHALL signal while leaving
the task `running` and its terminal `notified` flag unchanged. A failed pair write
MUST NOT publish a report wakeup. Watchers SHALL be scoped to the exact session.

#### Scenario: SQLite event is readable immediately on wake

- **GIVEN** a caller subscribes to `Registry.WatchSession` before spawning a monitor
- **AND** the monitor emits a matched line then blocks on a sandbox release file
- **WHEN** the session notification wakes the caller
- **THEN** an immediate SQLite read MUST observe both synthetic messages, linked by tool-call ID
- **AND** the monitor MUST still be running with no terminal notification
- **AND** releasing the file SHALL allow the normal terminal completion to follow

### Requirement: Active non-interactive waits consume reports in the owning run

A non-interactive report wait SHALL retain the session's process-global run slot.
`EnqueueTaskCompletion` MUST NOT start a parallel run while any instance owns that
slot. The owning run SHALL wake on the first report or queued user input and consume
reports at complete tool-batch boundaries, rather than waiting for every task to end.
This includes interactive flow steps, which use `NonInteractive: true`.

TUI and bridge `yield` instead release their run slot like a normal `end_turn`.
Subsequent reports SHALL use the existing idle auto-resume path to continue the same
session. A report racing with release MUST remain eligible for consumption or resume;
yielding MUST NOT strand it.

The release-time handoff SHALL NOT resume a turn whose finish reason is
`permission_denied`, `canceled` or `error`, even when its run-level error is empty
and an unseen report skipped auto-resume while the session was busy. The report
SHALL remain available for a later run. This does not change idle auto-resume for
new reports arriving after release or existing turn-budget exhaustion behavior.

#### Scenario: Explicit stop suppresses the release handoff

- **GIVEN** a report commits while an interactive turn owns the session slot
- **WHEN** the turn finishes with `permission_denied`, `canceled` or `error`
- **THEN** releasing the slot SHALL NOT start a run for that skipped report
- **AND** the unseen report SHALL remain in the session history

#### Scenario: Flow wait does not spawn a parallel Run

- **GIVEN** a flow run is waiting after `yield` or a natural terminal turn
- **WHEN** a monitor-event commits while its process remains active
- **THEN** the owning run MUST wake with the report
- **AND** no separate `ResumeSession` run SHALL start

#### Scenario: Interactive yield releases and later resumes

- **GIVEN** a TUI or bridge run calls `yield` with background work still active
- **WHEN** the run releases its session slot
- **THEN** user input can start a turn immediately
- **AND** a later report for a non-flow-owned task SHALL auto-resume the idle session

### Requirement: Flow-owned tasks never auto-resume

A background task spawned under a flow step's step-scoped context (`tools.StepScopedContextKey` present at spawn time — async task subagents, background bash, monitor) SHALL be marked flow-owned (`task.Task.FlowOwned`). `EnqueueTaskCompletion` SHALL NOT call `deps.ResumeSession` for a flow-owned task's completion — regardless of the session's busy state. Everything else about the completion is unchanged: the synthetic pair is written, terminal statuses transition the registry state and honor the `notified` CAS, and monitor-events remain non-terminal.

While the owning step's run is active, it consumes reports at tool-batch boundaries
and waits for the first report or inbox message when idle. A monitor event can wake
it while its process runs. This preserves the step's agent, tools and output schema.
`ResumeSession` resolves through the active/primary agent and cannot substitute for
that run. After the step has routed, an auto-resume would start an unrelated turn on
the completed step's session (GENAI-239).

A flow-owned task that outlives its step (a detached `bash`/`monitor` subprocess when the step's Run exits on a timeout or turn-budget exhaustion) therefore has NO consumer for its completion. This is accepted: the pair is still written and remains visible to the transcript, `tasklist`, and any later iteration of the step, and the runtime's wait-timeout note already enumerates the abandoned tasks. The alternative is the zombie turn above.

#### Scenario: Flow-owned bash completion after the step ended

- **GIVEN** a flow step spawned a background bash and the step's Run has since ended (the subprocess outlived the step)
- **WHEN** the subprocess exits and `EnqueueTaskCompletion` fires with a terminal status
- **THEN** the synthetic pair is written and the task transitions terminal state, but `deps.ResumeSession` is NOT called

#### Scenario: Flow-owned monitor event

- **WHEN** a monitor spawned under a step-scoped context fires a `monitor-event` completion
- **THEN** the pair is written and `deps.ResumeSession` is NOT called; an in-flight step run consumes the pair after its current tool batch or wakes from its report wait

#### Scenario: Non-flow spawns keep today's behavior

- **WHEN** a background task spawned WITHOUT a step-scoped context (TUI, bridge chat session) completes while its session is idle
- **THEN** `deps.ResumeSession` fires exactly as before

#### Scenario: Unknown or empty task IDs are not flow-owned

- **WHEN** `EnqueueTaskCompletion` is invoked with an empty `TaskID` (cron) or a task ID absent from the registry (e.g. after a registry reset)
- **THEN** the flow-owned gate does not suppress the resume; the busy check alone decides

