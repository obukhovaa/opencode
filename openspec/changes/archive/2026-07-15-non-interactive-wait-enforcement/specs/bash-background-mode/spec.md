## MODIFIED Requirements

### Requirement: Background spawn ack format

When `run_in_background: true`, the tool's ack ToolResult SHALL contain at minimum:
- The literal phrase "Background task started"
- A `task_id:` line with the task ID assigned by the background-tasks registry
- An `output_file:` line with the absolute path to `<data.dir>/tasks/<task_id>.out`
- A `command:` line with the (possibly truncated) command string
- Guidance text that: (a) a synthetic completion notification will arrive automatically when the subprocess exits; (b) the agent MUST NOT `sleep` or poll while waiting — in a non-interactive step the runtime holds the turn until the task reaches a terminal state.

The ack MUST NOT frame the output file as a progress-polling target and MUST NOT instruct the agent to read it mid-flight "to inspect progress". (The path remains in the ack for post-completion inspection; the synthetic completion carries the output either way.)

#### Scenario: Ack content

- **WHEN** the agent invokes `{command: "go test ./...", run_in_background: true}`
- **THEN** the ack contains lines matching `^task_id: shell_[A-Z2-7]+$`, `^output_file: .+/tasks/shell_[A-Z2-7]+\.out$`, and `^command: go test \./\.\.\.$` (with reasonable command truncation if longer than 200 chars)

#### Scenario: Ack does not invite polling

- **WHEN** the background spawn ack guidance text is produced
- **THEN** it MUST contain a "do NOT poll / do NOT sleep" instruction
- **AND** it MUST NOT present reading the output file mid-flight as a way to inspect progress

## ADDED Requirements

### Requirement: Foreground wall-clock waits are redirected to the task wait in non-interactive mode

When the `bash` tool executes a FOREGROUND command (i.e. NOT `run_in_background`) under a non-interactive run (`RunOptions{NonInteractive: true}`), AND the session has one or more pending NON-MONITOR background tasks (`Kind` bash or task), AND the command's sole effect is a wall-clock wait, the tool SHALL NOT execute the wait. Instead it SHALL call `task.Registry.WaitForActiveTasks(ctx, sessionID, WaitOptions{IncludeMonitor: false})` and return a synthetic bash-style `ToolResponse` summarizing the tasks that reached a terminal state during the wait.

Monitor tasks are deliberately EXCLUDED from this redirect: a monitor is intentionally long-lived (bounded by `max_events` / a finite `cmd` / `taskstop`), so a foreground `sleep` MUST NOT be converted into a wait that blocks until a monitor terminates. When the only pending tasks are monitors, the command runs normally. (The end-of-turn drain in `background-tasks` still includes monitors — that is the correct place to bound a monitor's lifetime, not a mid-turn sleep.)

A command qualifies as a "wall-clock wait" iff, after trimming, it consists solely of `sleep <duration>` optionally followed by a separator (`;` or `&&`) and a single `echo …`. Any other command runs normally.

The non-interactive signal MUST be available to the tool via the tool-execution `context` (the agent sets it; it is runtime-only and never persisted); the session id is already available to tools via the existing context accessor. When the non-interactive marker is absent (interactive runs), NO redirection occurs and the command executes verbatim — preserving interactive behavior byte-for-byte.

The requested sleep duration is intentionally ignored: the redirected wait drains all pending tasks and is bounded solely by the surrounding `ctx` (the step timeout). On `ctx` cancellation the tool returns a result noting the deadline elapsed and which tasks remain pending.

#### Scenario: Pure sleep during pending tasks is converted to a wait

- **GIVEN** a non-interactive run whose session has 3 pending `task async` subagents
- **WHEN** the model calls `bash` (foreground) with `sleep 300; echo done`
- **THEN** the tool MUST NOT spawn a `sleep` process
- **AND** the tool MUST call `WaitForActiveTasks` for the session
- **AND** the returned tool result MUST enumerate the subagents that completed during the wait (id, kind, output_file)
- **AND** control returns to the model only after the pending tasks reach terminal state or `ctx` is cancelled

#### Scenario: Non-wait command runs normally even with pending tasks

- **GIVEN** a non-interactive run with pending background tasks
- **WHEN** the model calls `bash` (foreground) with `git status` (or any command that is not a pure `sleep`)
- **THEN** the tool MUST execute the command normally with no redirection

#### Scenario: Interactive foreground sleep is never redirected

- **GIVEN** an interactive run (`NonInteractive: false`)
- **WHEN** the model calls `bash` (foreground) with `sleep 5`
- **THEN** the tool MUST execute the sleep normally, regardless of any pending background tasks

#### Scenario: No pending tasks means no redirection

- **GIVEN** a non-interactive run whose session has zero pending background tasks
- **WHEN** the model calls `bash` (foreground) with `sleep 5; echo done`
- **THEN** the tool MUST execute the sleep normally

#### Scenario: Only pending monitors do not trigger redirection

- **GIVEN** a non-interactive run whose only pending background task is a long-lived `monitor`
- **WHEN** the model calls `bash` (foreground) with `sleep 5`
- **THEN** the tool MUST execute the sleep normally (monitor tasks do not trigger the redirect)
