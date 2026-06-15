## ADDED Requirements

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

1. An Assistant message containing a single `ToolCall` content part:
   - `ID`: a freshly generated tool-use ID (format `toolu_<hex>`) — NOT the `OriginatingToolCallID` (that field is informational metadata included in the Tool message body)
   - `Name`: the `OriginatingToolName` (`bash`, `task`, or `monitor`)
   - `Input`: a JSON object that mirrors the shape the agent would have produced when invoking the originating tool — populated by the calling tool's monitor goroutine
   - `Type`: `tool_use`
   - `Finished`: `true`
2. A Tool message containing a single `ToolResult` content part:
   - `ToolCallID`: matches the Assistant message's ToolCall `ID`
   - `Name`: the same `OriginatingToolName`
   - `Type`: `ToolResultTypeText`
   - `Content`: the agent-facing result body

#### Scenario: Pair is atomic
- **WHEN** `EnqueueTaskCompletion` writes the pair
- **THEN** the pair is committed in a single transaction with consecutive `seq` numbers; partial visibility (Assistant without Tool, or vice versa) is impossible to readers

#### Scenario: Renderer formats result via originating tool's renderer
- **WHEN** the TUI or transcript exporter renders the synthetic pair
- **THEN** it resolves the renderer by the `Name` field of the ToolCall; a `bash` synthetic completion renders through `bash`'s normal result renderer

### Requirement: `synthetic` flag on Assistant messages
The system SHALL persist a `synthetic` boolean column (or equivalent message metadata field) on the `messages` table, defaulting to `false`. Every Assistant message written by `EnqueueTaskCompletion` MUST set `synthetic = true`. Every existing write path (the agent's real tool-use messages, user inbound messages) MUST continue to write `synthetic = false` (the default).

#### Scenario: Real tool calls are not flagged
- **WHEN** the agent invokes a tool (any tool, synchronous or background-spawning) and that tool's response is the agent's own ToolResult
- **THEN** the Assistant message containing the agent's ToolCall has `synthetic = false`

#### Scenario: Background completion is flagged
- **WHEN** `EnqueueTaskCompletion` writes its synthetic pair
- **THEN** the Assistant message has `synthetic = true`; the Tool message MAY also carry the flag for symmetry, but consumers MUST treat the Assistant flag as authoritative

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

All other statuses (`StatusCompleted`, `StatusFailed`, `StatusKilled`) are TERMINAL and MUST be subject to the `notified` CAS gate (see background-tasks spec).

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
