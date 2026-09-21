## ADDED Requirements

### Requirement: Self-detaching commands are rejected in background mode

When `run_in_background: true`, the bash tool SHALL refuse a command that detaches its own
work from the tracked subprocess, because such a command makes the task's terminal state
meaningless: the wrapper shell exits immediately while the real work continues untracked,
and every downstream guarantee (completion notification content, end-of-turn drain,
foreground-wait redirect) silently reads that as "work finished".

Detection SHALL be conservative — it MUST flag only:
- a top-level trailing `&` (a `&` that terminates the final pipeline; NOT `&&`, NOT a `&`
  inside quotes, a subshell, or a command substitution), or
- `nohup`, `setsid`, or `disown` appearing at command position at the command's top level.

Anything ambiguous MUST be allowed through. A false rejection blocks legitimate work; a
false accept only reproduces current behavior.

On rejection the tool SHALL return an error ToolResult (not a Go error) that names the
offending construct and states that `run_in_background` already detaches the subprocess,
so the trailing `&` / `nohup` must be dropped. The subprocess MUST NOT be spawned, no task
ID is allocated, and no output file is created.

This gate applies ONLY when `run_in_background: true`. Synchronous bash calls are
unaffected — backgrounding inside a synchronous call is the caller's own business.

#### Scenario: Trailing `&` with run_in_background is refused

- **WHEN** the agent invokes `{command: "nohup ./gradlew test > /tmp/log 2>&1 &\necho $!", run_in_background: true}`
- **THEN** the tool returns an error ToolResult naming `nohup` and/or the trailing `&`
- **AND** no task is registered and no subprocess is spawned
- **AND** the error text instructs dropping the detachment because `run_in_background` already detaches

#### Scenario: `&&` is not mistaken for detachment

- **WHEN** the agent invokes `{command: "go build ./... && go test ./...", run_in_background: true}`
- **THEN** the command is accepted and spawned normally

#### Scenario: Quoted ampersand is not mistaken for detachment

- **WHEN** the agent invokes `{command: "echo 'a & b' > /tmp/f", run_in_background: true}`
- **THEN** the command is accepted and spawned normally

#### Scenario: Synchronous calls are unaffected

- **WHEN** the agent invokes `{command: "sleep 1 &"}` with `run_in_background` omitted
- **THEN** the command runs verbatim, exactly as today

## MODIFIED Requirements

### Requirement: `run_in_background` parameter on bash tool
The existing `bash` tool's input schema SHALL gain a new optional boolean parameter `run_in_background` (default `false`). When omitted or `false`, the tool MUST behave exactly as it does today (synchronous, 600s timeout cap, captured stdout/stderr returned in the tool result). When `true`, the tool SHALL spawn the subprocess in the background and return immediately with an ack — UNLESS the command is refused by the self-detach gate below, in which case nothing is spawned, no task ID is allocated, no output file is created, and an error ToolResult is returned instead.

For every command the gate accepts, the bash tool's ack semantics are unchanged when called with `run_in_background: true` — the tool returns immediately with `task_id` + `output_file`, the subprocess runs detached, and the per-task monitor goroutine writes a synthetic completion when the subprocess exits.

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

#### Scenario: A refused self-detaching command produces no ack

- **WHEN** the agent invokes bash with `{command: "nohup ./x &", run_in_background: true}`
- **THEN** the tool returns an error ToolResult, not an ack
- **AND** no task_id is allocated and no output file is created

### Requirement: Foreground wall-clock waits are redirected to the task wait in non-interactive mode

When the `bash` tool executes a FOREGROUND command (i.e. NOT `run_in_background`) under a non-interactive run (`RunOptions{NonInteractive: true}`), AND the calling session or its direct child sessions have one or more pending NON-MONITOR background tasks (`Kind` bash or task), AND the command's leading element is a wall-clock wait, the tool SHALL NOT execute the wait. Instead it SHALL call `task.Registry.WaitForActiveTasks(ctx, sessionID, WaitOptions{IncludeMonitor: false, Scope: ScopeSessionAndChildren})` and return a synthetic bash-style `ToolResponse` summarizing the tasks that reached a terminal state during the wait.

**Scope.** Both the pre-check and the wait itself use `ScopeSessionAndChildren` — the calling session plus sessions whose parent is the caller. It is deliberately NOT the flow `root_session_id`: a flow assigns one root to every step and steps run concurrently, so root scope would block a `sleep` in one parallel branch on an unrelated branch's work. Widening the pre-check alone is forbidden — the wait re-snapshots internally, so a mismatched pair would return in microseconds having waited for nothing.

Resolving the caller's children requires the caller's own session identity, which the tool layer cannot derive from a session ID alone. The runtime MUST supply it on the tool-execution context alongside the non-interactive marker.

**Monitors remain excluded from this redirect.** A monitor is intentionally long-lived and a monitor whose pattern has not yet matched emits nothing at all, so no monitor-derived wake condition is bounded; converting a foreground `sleep` into a wait on one would produce a step-length block. When the only pending tasks are monitors, the command runs normally. (The end-of-turn drain still includes monitors — that is the correct place to bound a monitor's lifetime, not a mid-turn sleep. The monitor ack's yield contract, see `monitor-tool`, is what steers the model away from sleeping in this case.)

**Trailer handling.** A command qualifies as a wall-clock wait iff, after trimming, it BEGINS with `sleep <duration>` immediately followed by a `;` or `&&` separator, or consists solely of `sleep <duration>`. The separator MUST be anchored: `sleep 5 & echo bg` does NOT qualify, because `&` is neither separator. When a trailer is present the runtime performs the wait and then executes the trailer verbatim through the synchronous bash path, returning the interception note followed by the trailer's output and its real exit code and temp-file metadata. Three guards apply:

1. **No self-bypass.** If the trailer is itself a leading wall-clock wait, that wait is dropped rather than executed — otherwise `sleep 0; sleep 300` would sleep 300s under a result instructing the model not to sleep.
2. **No trailer on a cancelled wait.** When the wait returns `ctx.Err()`, the trailer MUST NOT be executed; the result carries the deadline note and the still-pending tasks only.
3. **No second permission check.** Interception occurs after the tool's permission gate has already evaluated the full command string, trailer included. The trailer MUST NOT be re-gated, which would double-prompt.

The non-interactive signal MUST be available to the tool via the tool-execution `context` (the agent sets it; runtime-only, never persisted). When the marker is absent (interactive runs), NO redirection occurs and the command executes verbatim — preserving interactive behavior byte-for-byte.

The requested sleep duration is intentionally ignored: the redirected wait drains the pending tasks and is bounded solely by the surrounding `ctx`. On `ctx` cancellation the tool returns a result noting the deadline elapsed and which tasks remain pending.

Because a child-owned task's synthetic completion is enqueued on the child's session, the interception note MUST NOT promise that a completion result will appear in the caller's conversation for such tasks; it points at `output_file` instead.

#### Scenario: Pure sleep during pending tasks is converted to a wait

- **GIVEN** a non-interactive run whose session has 3 pending `task async` subagents
- **WHEN** the model calls `bash` (foreground) with `sleep 300; echo done`
- **THEN** the tool MUST NOT spawn a `sleep` process
- **AND** the tool MUST call `WaitForActiveTasks` for the session
- **AND** the returned tool result MUST enumerate the subagents that completed during the wait (id, kind, output_file)
- **AND** control returns to the model only after the pending tasks reach terminal state or `ctx` is cancelled

#### Scenario: Non-wait command runs normally even with pending tasks

- **GIVEN** a non-interactive run with pending background tasks
- **WHEN** the model calls `bash` (foreground) with `git status` (or any command that does not begin with `sleep`)
- **THEN** the tool MUST execute the command normally with no redirection

#### Scenario: Interactive foreground sleep is never redirected

- **GIVEN** an interactive run (`NonInteractive: false`)
- **WHEN** the model calls `bash` (foreground) with `sleep 5`
- **THEN** the tool MUST execute the sleep normally, regardless of any pending background tasks

#### Scenario: No pending tasks means no redirection

- **GIVEN** a non-interactive run whose session and child sessions have zero pending background tasks
- **WHEN** the model calls `bash` (foreground) with `sleep 5; echo done`
- **THEN** the tool MUST execute the sleep normally

#### Scenario: Only pending monitors do not trigger redirection

- **GIVEN** a non-interactive run whose only pending background task is a long-lived `monitor`
- **WHEN** the model calls `bash` (foreground) with `sleep 5`
- **THEN** the tool MUST execute the sleep normally (monitor tasks do not trigger the redirect)

#### Scenario: Parent is intercepted for child-owned work

- **GIVEN** a subagent whose parent session is `S` has registered a still-running `run_in_background` bash task
- **WHEN** the agent on session `S` calls `bash` (foreground) with `sleep 120`
- **THEN** the sleep MUST NOT be executed
- **AND** both the pre-check and the wait MUST include the child's task
- **AND** the note MUST reference the task's `output_file` rather than promising a completion result in this conversation

#### Scenario: Sleep with a probe trailer is split, not skipped

- **GIVEN** a non-monitor task is pending in scope
- **WHEN** the model calls `bash` (foreground) with `sleep 120; grep -nE 'BUILD SUCCESSFUL' /tmp/log | head -20; tail -5 /tmp/log`
- **THEN** the sleep MUST NOT be executed
- **AND** the runtime waits on the pending task to reach terminal state
- **AND** only then is the trailer executed verbatim
- **AND** the result contains the interception note followed by the trailer's output and its real exit code

#### Scenario: A trailer that is itself a sleep does not bypass the guard

- **GIVEN** a non-monitor task is pending in scope
- **WHEN** the model calls `bash` (foreground) with `sleep 0; sleep 300`
- **THEN** no 300-second sleep is executed

#### Scenario: Trailer is skipped when the wait is cancelled

- **GIVEN** a non-monitor task is pending and the surrounding `ctx` deadline elapses during the wait
- **WHEN** the intercepted command was `sleep 600; cat /tmp/log`
- **THEN** the trailer MUST NOT be executed
- **AND** the result carries the deadline note and the still-pending task list

#### Scenario: Ampersand is not a trailer separator

- **GIVEN** a non-interactive run with pending background tasks
- **WHEN** the model calls `bash` (foreground) with `sleep 5 & echo bg`
- **THEN** the command MUST NOT be intercepted and MUST run verbatim

#### Scenario: Sleep that is not the leading element runs verbatim

- **GIVEN** a non-interactive run with pending background tasks
- **WHEN** the model calls `bash` (foreground) with `echo first; sleep 5`
- **THEN** the tool MUST execute the command normally with no redirection
