## MODIFIED Requirements

### Requirement: Subagent lifecycle in async mode

The async spawn SHALL:
1. Validate the subagent type the same way the synchronous path does.
2. Find or create the `taskSession` (resuming if `task_id` was supplied AND that session exists).
3. Allocate an output file under `<data.dir>/tasks/<task_id>.out`.
4. Register the task in the background-tasks registry with `Kind: KindTask` and a `context.CancelFunc` that can cancel the subagent's run.
5. Derive the subagent's run context from a **step-scoped context**, NOT from `context.Background()`. The step-scoped context MUST:
   - survive the parent agent's per-turn context ending (a turn finishing MUST NOT cancel an in-flight subagent — this preserves the async contract), AND
   - be bounded by the caller's deadline: when the step / caller has a deadline (`Step.Timeout`, the `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` default, or a surrounding orchestrator deadline), the subagent's context MUST be cancelled when that deadline elapses.
   The registered `context.CancelFunc` (for `taskstop`) wraps this step-scoped context.
6. Spawn a background goroutine that:
   - Invokes `a.Run(runCtx, taskSession.ID, prompt, 0)` and receives the `done` channel.
   - Waits on `<-done`.
   - Performs the same cost-rollup the synchronous path performs (subagent cost → parent session).
   - Writes the subagent's final response content (including the `<task_id>` and `<task_resume_hint>` trailers, OR the struct_output content if applicable) to the output file.
   - Invokes `task.EnqueueTaskCompletion` with `Kind: KindTask`, `OriginatingToolName: "task"`, `Status: StatusCompleted` (or `StatusFailed` if the run errored, including a context cancellation from the step-scoped deadline; `StatusKilled` remains reserved for `taskstop`).
7. Return the ack ToolResult to the original tool call.

#### Scenario: Subagent completes successfully

- **WHEN** an async subagent finishes its run with a normal assistant message
- **THEN** the cost rollup runs against the parent session, the final response (with trailers) is written to the output file AND becomes the `Content` of the synthetic Tool message; an `agent.Run` is auto-started on the parent if idle

#### Scenario: Subagent errors out

- **WHEN** an async subagent's run returns an error (model failure, context cancelled, etc.)
- **THEN** the synthetic pair fires with `Status: StatusFailed` (this includes a context cancellation, e.g. from the step-scoped deadline); the Content contains the error message; the task transitions to `failed` in the registry

#### Scenario: Subagent survives the parent's turn ending

- **GIVEN** a non-interactive parent spawns an async subagent
- **WHEN** the parent's current turn ends (its per-turn context is cancelled)
- **THEN** the subagent MUST continue running (its context is step-scoped, not per-turn)

#### Scenario: Subagent is cancelled when the step deadline elapses

- **GIVEN** a flow step with `timeout: 15m` spawns async subagents
- **WHEN** the step's 15-minute deadline elapses while subagents are still running
- **THEN** the step-scoped context MUST be cancelled
- **AND** each in-flight subagent's `a.Run` MUST observe the cancellation and return
- **AND** cost rollup MUST still run and a `StatusFailed` completion MUST be enqueued for each (matching the existing context-cancellation path; `StatusKilled` remains reserved for `taskstop`)
- **AND** no subagent goroutine SHALL outlive the step on `context.Background()`

#### Scenario: Resumed async task

- **WHEN** the agent invokes async task with a `task_id` that matches an existing session and `async: true`
- **THEN** the subagent's session is reused (continuing prior history), a new `agent_*` task ID is allocated for THIS run (the registry tracks runs, not sessions), and completion follows the same notification path

### Requirement: Async spawn ack format

When `async: true`, the tool's ack ToolResult SHALL contain at minimum:
- The literal phrase "Async subagent task started"
- A `task_id:` line with the task ID (`agent_*` prefix)
- A `subagent:` line with the subagent type and name
- An `output_file:` line with the absolute path to `<data.dir>/tasks/<task_id>.out` (the subagent's final response will be written here, and the path is used to reattach/inspect the result AFTER completion)
- Guidance text that: (a) a synthetic completion will arrive automatically when the subagent finishes; (b) in a non-interactive step the runtime holds the turn until the subagent reaches a terminal state, so the agent MUST NOT `sleep` or poll; (c) resuming the same `task_id` later (via the task tool with the returned id) reattaches to the same session.

The ack MUST NOT frame `output_file` as a progress-polling target and MUST NOT instruct the agent to read it "to check progress".

#### Scenario: Ack content

- **WHEN** the agent invokes async task with `subagent_type: "workhorse"` and `task_title: "rebuild fixtures"`
- **THEN** the ack contains lines matching `^task_id: agent_[A-Z2-7]+$`, `^subagent: workhorse$`, and an output_file line

#### Scenario: Ack does not invite polling

- **WHEN** the async ack guidance text is produced
- **THEN** it MUST contain a "do NOT poll / do NOT sleep" instruction
- **AND** it MUST NOT contain wording that presents reading the `output_file` as a way to inspect progress before completion
