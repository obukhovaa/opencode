# Task Async Mode

## Purpose

Extends the existing `task` tool with an `async: true` parameter that runs the subagent in the background and returns an immediate ack to the caller. On subagent completion, cost is rolled up to the parent session and a synthetic completion notification is injected via the task-notifications primitive. The synchronous default (`async: false`) is preserved so existing callers (cron, the agent's natural usage) see no behavior change. `taskstop` against an `agent_*` task cancels the subagent's context and emits a `StatusKilled` completion.

## Requirements


### Requirement: `async` parameter on task tool
The existing `task` tool's input schema SHALL gain a new optional boolean parameter `async` (default `false`). When omitted or `false`, the tool MUST behave exactly as today (synchronous, blocks on subagent completion, returns final response). When `true`, the tool SHALL spawn the subagent in the background and return immediately with an ack.

The `task` tool's `async: true` ack semantics are unchanged — the parent agent receives an immediate ack with `task_id` and `task_session_id`, the subagent runs in a detached goroutine bound to its own context, and the cost rollup + synthetic completion fire when the subagent's `done` channel emits.

When the parent agent is itself running under `NonInteractive: true` (i.e., the flow step's primary agent), the parent's `agent.RunWith` MUST wait for the subagent's terminal state before returning, just like for `bash run_in_background`. The subagent's synthetic completion is committed to the PARENT'S session message log via `EnqueueTaskCompletion`, and the parent's wait observes the parent-side task transition via the parent's per-task `done` channel.

#### Scenario: Default synchronous behavior preserved
- **WHEN** any existing caller invokes the task tool without `async`
- **THEN** behavior is identical to the prior implementation; the tool blocks on `result := <-done` and returns the subagent's final response

#### Scenario: Async mode returns immediately
- **WHEN** the agent invokes the task tool with `{prompt: "...", subagent_type: "workhorse", task_title: "...", async: true}`
- **THEN** the tool returns within milliseconds with an ack ToolResult; the subagent continues running in the background

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

### Requirement: Async spawn ack format
When `async: true`, the tool's ack ToolResult SHALL contain at minimum:
- The literal phrase "Async subagent task started"
- A `task_id:` line with the task ID (`agent_*` prefix)
- A `subagent:` line with the subagent type and name
- An `output_file:` line with the absolute path to `<data.dir>/tasks/<task_id>.out` (the subagent's final response will be written here)
- Guidance text reminding the agent that a synthetic completion will arrive when the subagent finishes, and that resuming the same task_id later (via the task tool with the returned id) reattaches to the same session

#### Scenario: Ack content
- **WHEN** the agent invokes async task with `subagent_type: "workhorse"` and `task_title: "rebuild fixtures"`
- **THEN** the ack contains lines matching `^task_id: agent_[A-Z2-7]+$`, `^subagent: workhorse$`, and an output_file line

### Requirement: Subagent lifecycle in async mode
The async spawn SHALL:
1. Validate the subagent type the same way the synchronous path does.
2. Find or create the `taskSession` (resuming if `task_id` was supplied AND that session exists).
3. Allocate an output file under `<data.dir>/tasks/<task_id>.out`.
4. Register the task in the background-tasks registry with `Kind: KindTask` and a `context.CancelFunc` that can cancel the subagent's run.
5. Spawn a background goroutine that:
   - Invokes `a.Run(ctx, taskSession.ID, prompt, 0)` and receives the `done` channel.
   - Waits on `<-done`.
   - Performs the same cost-rollup the synchronous path performs (subagent cost → parent session).
   - Writes the subagent's final response content (including the `<task_id>` and `<task_resume_hint>` trailers, OR the struct_output content if applicable) to the output file.
   - Invokes `task.EnqueueTaskCompletion` with `Kind: KindTask`, `OriginatingToolName: "task"`, `Status: StatusCompleted` (or `StatusFailed` if the run errored), `Content` set to the final response.
6. Return the ack ToolResult to the original tool call.

#### Scenario: Subagent completes successfully
- **WHEN** an async subagent finishes its run with a normal assistant message
- **THEN** the cost rollup runs against the parent session, the final response (with trailers) is written to the output file AND becomes the `Content` of the synthetic Tool message; an `agent.Run` is auto-started on the parent if idle

#### Scenario: Subagent errors out
- **WHEN** an async subagent's run returns an error (model failure, context cancelled, etc.)
- **THEN** the synthetic pair fires with `Status: StatusFailed`; the Content contains the error message; the task transitions to `failed` in the registry

#### Scenario: Resumed async task
- **WHEN** the agent invokes async task with a `task_id` that matches an existing session and `async: true`
- **THEN** the subagent's session is reused (continuing prior history), a new `agent_*` task ID is allocated for THIS run (the registry tracks runs, not sessions), and completion follows the same notification path

### Requirement: Cost rollup in async mode
The async subagent's cost SHALL roll up to the parent session in the background goroutine, BEFORE `EnqueueTaskCompletion` writes the completion pair. The rollup behavior MUST be identical to the synchronous path's existing rollup (`internal/llm/agent/agent-tool.go:184-196`), including the resilience against transient Get/Save errors (log warn, continue).

#### Scenario: Cost rolled up exactly once
- **WHEN** an async subagent completes
- **THEN** the parent session's stored `cost` field has incremented by exactly the subagent's accumulated cost; no double-counting; no cost loss if cancelled

### Requirement: Permission gate uses existing `task` rule
The spawn-time permission check for `async: true` SHALL use the existing `task` permission rule key. There is no separate `task-async` rule. Once spawn is approved, the async completion notification MUST NOT trigger a fresh permission check.

#### Scenario: task rule allows
- **WHEN** `permission.rules.task: {"*": "allow"}` and the agent invokes async task
- **THEN** spawn succeeds without prompt; completion fires without prompt

### Requirement: taskstop on async subagent
When `taskstop` is invoked against an `agent_*` task, the registry's `Kill(taskID)` SHALL call the stored `context.CancelFunc`, propagating cancellation into the subagent's `agent.Run`. The subagent's goroutine SHALL still perform cost rollup and SHALL emit a `StatusKilled` completion notification via `EnqueueTaskCompletion`.

#### Scenario: Async subagent is killed mid-run
- **WHEN** the agent invokes `taskstop` on a running async subagent
- **THEN** the subagent's context is cancelled, its `agent.Run` returns with a cancellation error, cost is rolled up, and a synthetic completion with `Status: StatusKilled` and summary "Async task killed by taskstop" is injected
