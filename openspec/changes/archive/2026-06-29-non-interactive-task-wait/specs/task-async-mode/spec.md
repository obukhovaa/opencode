## MODIFIED Requirements

### Requirement: `task async: true` ack semantics are unchanged; non-interactive callers wait at parent turn end

The `task` tool's `async: true` ack semantics are unchanged — the parent agent receives an immediate ack with `task_id` and `task_session_id`, the subagent runs in a detached goroutine bound to its own context, and the cost rollup + synthetic completion fire when the subagent's `done` channel emits.

When the parent agent is itself running under `NonInteractive: true` (i.e., the flow step's primary agent), the parent's `agent.RunWith` MUST wait for the subagent's terminal state before returning, just like for `bash run_in_background`. The subagent's synthetic completion is committed to the PARENT'S session message log via `EnqueueTaskCompletion`, and the parent's wait observes the parent-side task transition via the parent's per-task `done` channel.

#### Scenario: Async task in a flow step delivers completion within the same step

- **WHEN** a non-interactive flow step's agent calls `task` with `async: true` against a workhorse subagent
- **AND** the parent agent emits `struct_output` immediately after receiving the ack
- **THEN** the parent's `agent.RunWith` MUST wait for the subagent to reach terminal state
- **AND** the synthetic Assistant(ToolCall name="task") + Tool(ToolResult) pair MUST be injected into the parent session
- **AND** the parent agent MUST be invoked for at least one additional cycle so its final struct_output can reflect the subagent's response

#### Scenario: Async task with taskstop in non-interactive mode

- **GIVEN** a flow step spawns an async subagent
- **WHEN** the parent agent calls `taskstop` against the spawned task within the same turn
- **THEN** the registry MUST mark the task `StateKilled`
- **AND** the parent's wait MUST observe the killed state and return
- **AND** the synthetic `StatusKilled` completion MUST be injected into the parent session
- **AND** the cost rollup MUST still run (mirroring the synchronous path's resilience guarantee)
