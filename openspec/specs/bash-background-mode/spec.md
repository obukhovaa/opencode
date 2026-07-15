# Bash Background Mode

## Purpose

Extends the existing `bash` tool with a `run_in_background: true` parameter that detaches the subprocess from the synchronous tool-result return path. The tool returns an immediate ack containing a `task_id` and an `output_file` path; on subprocess exit, a synthetic completion notification is injected into the bound session via the task-notifications primitive. The 600s synchronous timeout cap does not apply in background mode — the subprocess can run until natural exit, `taskstop`, opencode shutdown, or the pod's `activeDeadlineSeconds`. Permission gating reuses the existing `bash` rule key; no separate `bash-background` rule.

## Requirements


### Requirement: `run_in_background` parameter on bash tool
The existing `bash` tool's input schema SHALL gain a new optional boolean parameter `run_in_background` (default `false`). When omitted or `false`, the tool MUST behave exactly as it does today (synchronous, 600s timeout cap, captured stdout/stderr returned in the tool result). When `true`, the tool SHALL spawn the subprocess in the background and return immediately with an ack.

The bash tool's ack semantics are unchanged when called with `run_in_background: true` — the tool returns immediately with `task_id` + `output_file`, the subprocess runs detached, and the per-task monitor goroutine writes a synthetic completion when the subprocess exits.

What changes is **what happens after the model's terminal turn** in non-interactive mode (`agent.RunWith(..., NonInteractive: true)`): the agent.Run loop waits for the bash task to complete and re-enters the agentic loop so the model observes the synthetic completion within the SAME `RunWith` invocation. The agent therefore experiences `run_in_background` in non-interactive mode as effectively per-cycle synchronous, but without the 600s timeout cap that applies to truly synchronous bash.

#### Scenario: Default behavior unchanged
- **WHEN** the agent invokes bash with `{command: "echo hi"}`
- **THEN** the tool blocks synchronously, captures output, and returns "hi" in the tool result (no change from prior behavior)

#### Scenario: Background mode returns immediately
- **WHEN** the agent invokes bash with `{command: "sleep 60", run_in_background: true}`
- **THEN** the tool returns within milliseconds with an ack ToolResult; the subprocess continues running

#### Scenario: Background bash in a flow step delivers completion within the same step

- **WHEN** a flow step invokes the agent and the agent calls `bash run_in_background: true` with a 30-second command
- **AND** the model then emits `struct_output`
- **THEN** `agent.RunWith` MUST wait up to `NonInteractiveTaskWaitTimeout` for the bash subprocess to exit
- **AND** the synthetic Assistant(ToolCall name="bash") + Tool(ToolResult) pair MUST be injected into the session
- **AND** the model MUST be invoked for at least one additional cycle so it can reference the bash output in its final struct_output
- **AND** the flow step's resulting struct_output MUST be the post-completion response

#### Scenario: Background bash in interactive mode is unchanged

- **WHEN** the user types a TUI message and the agent calls `bash run_in_background: true`
- **AND** the agent emits `end_turn`
- **THEN** the TUI MUST observe the agent's turn end immediately
- **AND** the eventual synthetic completion MUST trigger a fresh `agent.Run` via auto-resume, surfacing as a new assistant message in the TUI (today's behaviour)

#### Scenario: Background bash exceeding the wait timeout in non-interactive mode

- **GIVEN** the surrounding ctx carries a 5-minute deadline (from a `Step.Timeout: 5m` field, or the `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` env var)
- **AND** the agent spawns `bash run_in_background` for a command that takes 10 minutes
- **WHEN** the deadline elapses while the wait is active
- **THEN** the wait MUST unblock with `ctx.Err()`
- **AND** the bash subprocess MUST continue running (the runtime does NOT auto-kill bash background tasks)
- **AND** the synthetic Assistant timeout note (see `background-tasks` spec) MUST be injected into the session enumerating the still-pending task IDs and output_file paths
- **AND** `agent.RunWith` MUST return the pre-wait `AgentEvent` so the flow runner can surface the result it has

#### Scenario: Background bash with no step timeout and no env default

- **GIVEN** the step has no `timeout` field
- **AND** `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` is unset
- **AND** the surrounding ctx has no deadline
- **WHEN** the agent spawns `bash run_in_background` for a 30-minute command and emits `struct_output`
- **THEN** the wait MUST block until the bash subprocess exits (no synthetic timeout note)
- **AND** the flow step's resulting struct_output MUST be the post-completion response

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

### Requirement: Subprocess lifecycle in background mode
The background spawn SHALL:
1. Allocate the task ID and output file via the background-tasks registry.
2. Open the output file write-only (mode 0o600), set `cmd.Stdout` and `cmd.Stderr` to it.
3. Start the subprocess (`cmd.Start()`); on start-failure, return a tool-execution error and do NOT register the task.
4. Register the task with `Kind: KindBash` and the running `*os.Process`.
5. Launch a monitor goroutine that calls `cmd.Wait()`, then on exit:
   - `Sync()` the output file (see background-tasks spec).
   - Read the file content (capped at the existing bash output-size budget).
   - Invoke `task.EnqueueTaskCompletion` with `Kind: KindBash`, `OriginatingToolName: "bash"`, `Status: StatusCompleted` (exit 0) or `StatusFailed` (exit != 0), `ExitCode`, and `Content` set to the captured output.
6. Return the ack ToolResult to the original tool call.

#### Scenario: Subprocess succeeds
- **WHEN** a background bash subprocess exits with code 0 after 30 seconds
- **THEN** at the 30s mark a synthetic Assistant(ToolCall name=bash, synthetic=true) + Tool(ToolResult) pair appears in the session log; the ToolResult content matches what a synchronous bash with the same command would have produced; an `agent.Run` is started on the session if it was idle

#### Scenario: Subprocess fails
- **WHEN** a background bash subprocess exits with code 2 after 5 seconds
- **THEN** a synthetic pair appears with `Status: StatusFailed`, the captured output is in the ToolResult content, and exit code 2 is recorded in the registry

#### Scenario: Subprocess start fails
- **WHEN** a background bash is invoked with a command not on PATH (e.g., `{command: "nonexistent-cmd", run_in_background: true}`)
- **THEN** the tool returns a regular synchronous tool error (no ack); no task is registered; no output file is created

### Requirement: No 600s timeout in background mode
When `run_in_background: true`, the existing 600s synchronous timeout cap SHALL NOT apply. The subprocess may run indefinitely (until natural exit, `taskstop`, opencode shutdown, or the K8s pod's `activeDeadlineSeconds`). The `timeout` parameter MUST be silently ignored when `run_in_background: true`; the tool MAY emit an informational note in the ack but MUST NOT error.

#### Scenario: Long-running background subprocess
- **WHEN** the agent invokes `{command: "sleep 7200", run_in_background: true}` (2 hours)
- **THEN** the spawn succeeds; the subprocess runs for the full duration; no synchronous-timeout error is produced

#### Scenario: timeout parameter is ignored in background mode
- **WHEN** the agent invokes `{command: "sleep 60", run_in_background: true, timeout: 5000}`
- **THEN** the subprocess runs for 60 seconds (not 5); the `timeout` parameter has no effect

### Requirement: Permission gate uses existing `bash` rule
The spawn-time permission check for `run_in_background: true` SHALL use the existing `bash` permission rule key. There is no separate `bash-background` rule. Once spawn is approved, the background completion notification MUST NOT trigger a fresh permission check.

#### Scenario: bash rule allows
- **WHEN** `permission.rules.bash: {"*": "allow"}` and the agent invokes a background bash
- **THEN** spawn succeeds without a prompt; completion notification fires without a prompt

#### Scenario: bash rule denies
- **WHEN** `permission.rules.bash: {"*": "deny"}` and the agent invokes a background bash
- **THEN** spawn is denied with the same tool-permission error a synchronous bash would produce; no task is registered

### Requirement: Synthetic ToolCall input mirrors the spawn input
The synthetic Assistant(ToolCall) written by the bash background completion path SHALL set its `Input` JSON to the same `BashParams` shape the agent originally sent, with `run_in_background` STRIPPED. This means the renderer formats the synthetic completion as if it were a synchronous bash result of the same command.

#### Scenario: Synthetic input reformatted
- **WHEN** the agent invoked `{command: "go test ./...", run_in_background: true}` and completion fires
- **THEN** the synthetic Assistant ToolCall's input JSON is `{"command": "go test ./..."}` (no `run_in_background` field), and renders identically to a synchronous bash call's input

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
