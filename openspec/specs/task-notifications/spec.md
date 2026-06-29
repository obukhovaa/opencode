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
- **THEN** the Assistant message has `synthetic = true`; the Tool message MAY also carry the flag for symmetry

#### Scenario: Flag is queryable
- **WHEN** a consumer (transcript exporter, bridge filter, debug query) reads messages from the DB
- **THEN** the `synthetic` field is exposed on the `message.Message` struct produced by sqlc-generated bindings

### Requirement: Auto-continue on idle session
If the target session is idle (no in-flight `agent.Run`) at the time `EnqueueTaskCompletion` writes its pair, the primitive SHALL start a fresh `agent.Run(ctx, sessionID, "", maxTurnsOverride)` immediately after the write. The empty content argument signals the agent that the new turn input is in the just-written synthetic ToolResult.

#### Scenario: Idle session auto-resumes
- **WHEN** a background bash completes and the bound session has no in-flight `agent.Run`
- **THEN** `EnqueueTaskCompletion` writes the synthetic pair, then invokes `agent.Run` against the same session; the agent observes the new synthetic ToolResult and produces a follow-up Assistant message

#### Scenario: Busy session is NOT re-triggered
- **WHEN** a background monitor fires a `monitor-event` while an `agent.Run` is in-flight on the bound session
- **THEN** `EnqueueTaskCompletion` writes the synthetic pair but DOES NOT start a new `agent.Run`; the in-flight run's next message-list refresh observes the new pair naturally on its next iteration

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

### Requirement: `task.deps.ResumeSession` is naturally suppressed during a non-interactive wait

`task.EnqueueTaskCompletion` SHALL continue to call `deps.ResumeSession(sessionID)` after writing the synthetic Assistant + Tool pair, IF AND ONLY IF `deps.IsSessionBusy(sessionID)` returns false. The non-interactive end-of-turn wait MUST NOT introduce any new suppression logic — instead, the existing `IsSessionBusy` check naturally returns true because:

1. The non-interactive `agent.RunWith` invocation that called the model is still in progress. Its goroutine holds the session-busy slot in `agent.activeRequests` from the moment `Run` was called until the goroutine returns.
2. The end-of-turn wait happens INSIDE that same goroutine, between the inner agentic loop's exit and the goroutine's eventual return.
3. While the wait is in progress, any background task that completes invokes `EnqueueTaskCompletion`, which observes `IsSessionBusy=true` and skips `ResumeSession`.
4. The synthetic Assistant + Tool pair is still committed to the message log atomically.
5. The in-flight `agent.RunWith` reloads the message history on its next outer-loop cycle and consumes the synthetic pair as input for the model's next call.

This means there is exactly ONE `agent.Run`-like invocation observing the synthetic completion in non-interactive mode — the original one. No parallel goroutine, no race.

#### Scenario: Background task completing during a non-interactive wait does NOT spawn a parallel Run

- **GIVEN** a non-interactive `agent.RunWith` is waiting for a background bash task at the end of its first inner agentic cycle
- **WHEN** the bash subprocess exits and `bashWaitAndNotify` calls `EnqueueTaskCompletion`
- **THEN** `EnqueueTaskCompletion` MUST call `deps.IsSessionBusy(sessionID)` and observe `true`
- **AND** `deps.ResumeSession` MUST NOT be called
- **AND** the synthetic pair MUST be written to the session
- **AND** the original `agent.RunWith` goroutine's wait MUST unblock and re-enter the agentic loop, picking up the synthetic pair from the reloaded message history

#### Scenario: Background task completing in interactive mode still auto-resumes (unchanged)

- **GIVEN** a TUI agent.Run has returned and the session is idle (no `activeRequests` entry)
- **WHEN** a background task spawned earlier in that session completes
- **THEN** `EnqueueTaskCompletion` MUST observe `IsSessionBusy=false`
- **AND** `deps.ResumeSession` MUST start a fresh `agent.Run` on the session
- **AND** the new assistant message MUST publish to the message broker as today
