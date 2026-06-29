# Monitor Tool

## Purpose

Defines the `monitor` tool — a process-spawning subprocess watcher that streams regex-matched lines back to the caller's session as synthetic notifications. The tool returns an immediate ack with `task_id` and `output_file`; matched lines are coalesced into per-window notifications (controlled by `min_interval_ms`) so a burst doesn't flood the session. Output of the underlying command (stdout + stderr merged) is teed to the output file in full so the agent can `Read` the complete log at any time. The monitor stops when (a) the subprocess exits naturally, (b) `max_events` notifications have been fired (subprocess SIGTERMed), or (c) `taskstop` is called. A terminal notification (`completed` / `failed` / `killed`) always fires.

## Requirements


### Requirement: Monitor tool registration and input schema
The system SHALL register a new top-level tool named `monitor`. Its input schema MUST be:
- `cmd` (string, required): the command to spawn
- `args` ([]string, optional): arguments to the command
- `cwd` (string, optional): working directory; defaults to the workspace root
- `pattern` (string, required): an RE2-compatible regex applied line-by-line to merged stdout+stderr
- `min_interval_ms` (int, optional, default 5000, min 100, max 600000): coalesce window in milliseconds
- `max_events` (int, optional, default 200, min 1, max 10000): hard cap on the number of `monitor-event` notifications this monitor may fire
- `description` (string, optional): short description shown in the ack message

#### Scenario: Required fields present
- **WHEN** the agent invokes `monitor` with `{cmd: "kubectl", args: ["logs", "-f", "my-pod"], pattern: "ERROR|FATAL"}`
- **THEN** the tool validates input and proceeds; missing `cmd` or `pattern` returns a tool-validation error

#### Scenario: Default coalesce window
- **WHEN** the agent invokes `monitor` without `min_interval_ms`
- **THEN** the monitor uses a 5000ms coalesce window

#### Scenario: Default event cap
- **WHEN** the agent invokes `monitor` without `max_events`
- **THEN** the monitor stops itself after firing 200 `monitor-event` notifications

### Requirement: Monitor spawn and ack
On invocation, the monitor tool SHALL:
1. Validate the input (including compiling `pattern` as `regexp.Compile`); return a validation error if the regex is invalid.
2. Allocate a task ID with `monitor_` prefix and an output file under `<data.dir>/tasks/<task_id>.out`.
3. Start the subprocess with stdout and stderr both redirected through a tee that (a) writes every byte to the output file and (b) line-scans against the compiled pattern.
4. Register the task in the background-tasks registry with `Kind: KindMonitor` and `Proc: cmd.Process`.
5. Start the coalesce ticker.
6. Return an ack ToolResult containing the `task_id`, `output_file` path, `pattern`, `min_interval_ms`, `max_events`, and a hint that matched-line notifications will arrive asynchronously.

#### Scenario: Successful spawn
- **WHEN** a valid monitor call is made
- **THEN** the tool returns within milliseconds with an ack containing the task_id; the subprocess continues running in the background

#### Scenario: Invalid regex
- **WHEN** `pattern` is `[unclosed`
- **THEN** the tool returns a tool-validation error; no subprocess is spawned; no task is registered

#### Scenario: Spawn failure
- **WHEN** `cmd` does not exist on PATH
- **THEN** the tool returns a tool-execution error; no task is registered

### Requirement: Coalesce-window batching
While a monitor task is running, the system SHALL collect matched lines into a per-monitor in-memory buffer. A ticker with period `min_interval_ms` SHALL fire once per window. On each tick:
- If the buffer is empty, do nothing.
- Otherwise, drain the buffer atomically into a local slice; reset the buffer; call `task.EnqueueTaskCompletion` with `Status: StatusMonitorEvent`, `Content` formatted as:
  ```
  <N> match(es) in window:
  <line1>
  <line2>
  ...
  ```
  where `<N>` is the count and lines preserve the original order.

#### Scenario: Burst is coalesced
- **WHEN** during a single 5000ms window the subprocess emits 12 lines matching `pattern`
- **THEN** the next tick fires exactly one `monitor-event` notification carrying all 12 lines in order

#### Scenario: Silent window emits nothing
- **WHEN** during a 5000ms window no lines match
- **THEN** no notification is fired; the buffer remains empty

#### Scenario: Lines arriving mid-window are batched into the next tick
- **WHEN** a matched line arrives 0.1s before a tick and another arrives 0.1s after the tick
- **THEN** the first line is in the tick's batch; the second line is in the following tick's batch

### Requirement: Event-cap enforcement
The monitor task SHALL count the number of `monitor-event` notifications it has fired. When the count reaches `max_events`, the monitor MUST:
1. Cancel the coalesce ticker.
2. Drain any remaining buffered lines into one final `monitor-event` notification (subject to the cap; if the cap is exactly reached, the drain is skipped).
3. Send SIGTERM to the subprocess.
4. Wait for subprocess exit.
5. Fire a TERMINAL `StatusKilled` notification with summary "Monitor stopped: max_events reached (<max_events>)".

#### Scenario: Cap is reached during a chatty stream
- **WHEN** `max_events: 50` and the 50th `monitor-event` notification fires
- **THEN** the next tick is cancelled, the subprocess is terminated, and a `StatusKilled` notification is injected with the "max_events reached" summary

### Requirement: Natural exit and stream-ended notification
When the monitor subprocess exits on its own (without `taskstop` and without hitting `max_events`), the monitor SHALL:
1. Drain any remaining buffered lines into a final `monitor-event` notification.
2. Fire a TERMINAL notification with `StatusCompleted` (if exit code 0) or `StatusFailed` (if exit code != 0).
3. The summary line MUST be "Monitor stream ended" (success) or "Monitor script failed (exit <code>)" (failure).

#### Scenario: Tailed log stream ends cleanly
- **WHEN** `kubectl logs -f <pod>` exits with code 0 (pod removed)
- **THEN** any remaining matched lines are flushed in a `monitor-event` notification, then a `StatusCompleted` notification with summary "Monitor stream ended"

#### Scenario: Monitor script crashes
- **WHEN** the monitored command exits with code 137 (SIGKILL by OOM)
- **THEN** the final notification is `StatusFailed` with summary "Monitor script failed (exit 137)"

### Requirement: taskstop integration
When `taskstop` is invoked against a monitor task, the monitor SHALL:
1. Cancel the coalesce ticker.
2. Drain any remaining buffered lines into a final `monitor-event` notification.
3. Send SIGTERM to the subprocess.
4. After subprocess exit (or 5 second grace, whichever first), fire a TERMINAL `StatusKilled` notification with summary "Monitor stopped by taskstop".

#### Scenario: Operator-initiated stop
- **WHEN** the agent invokes `taskstop` with the monitor's task_id while the monitor is running
- **THEN** the subprocess receives SIGTERM, any buffered matches are flushed, and a `StatusKilled` notification appears in the session

### Requirement: Output file completeness
The output file under `<data.dir>/tasks/<task_id>.out` SHALL contain the COMPLETE merged stdout+stderr stream regardless of `pattern`. The `Read` tool MUST be able to access the full output even if no lines matched the regex.

#### Scenario: No matches but full output captured
- **WHEN** a monitor runs with `pattern: "ERROR"` over a stream that emits 100 INFO lines and no ERRORs
- **THEN** no `monitor-event` notification fires, but the output file contains all 100 INFO lines; `Read` returns the full content

### Requirement: Permission gate at spawn time only
The `monitor` tool's spawn-time permission MUST be checked against the `monitor` permission rule key (default `ask`). Once the spawn is approved, all subsequent events (per-event notifications, terminal notification) MUST NOT trigger further permission checks. The terminal kill-on-cap or kill-on-stop similarly does not require a fresh permission check.

#### Scenario: Permission denied at spawn
- **WHEN** the agent invokes `monitor` and the user/policy denies
- **THEN** no subprocess is spawned; no task is registered; no output file is created; an error tool result returns

#### Scenario: Headless allow covers monitor events
- **WHEN** `permissionMode: allow` is set, the agent invokes `monitor`, and the monitor fires 50 events over 5 minutes
- **THEN** no individual event triggers a permission check; the system processes each event without prompting

### Requirement: Monitor tasks are NOT auto-killed in non-interactive mode

A `monitor` task has indefinite lifetime by design — it streams events until the spawned subprocess exits, until `max_events` is reached, or until `taskstop` is called explicitly. In non-interactive mode (`agent.RunWith(..., NonInteractive: true)`), monitor tasks SHALL be treated identically to `bash run_in_background` and `task async` tasks: included in the end-of-turn wait set, NOT auto-killed at turn end.

The rationale: monitor's primary use case in flow-driven execution is "wait for event X in a stream" (e.g. wait for an external CI pipeline to emit a `BUILD_COMPLETED` line). Auto-killing monitors would force agents to fall back to blocking `bash sleep` loops — the exact anti-pattern monitor was designed to replace.

To bound a monitor's lifetime in non-interactive contexts the agent SHALL use one of:

1. **`max_events: N`** — the monitor self-terminates after N coalesce-windows containing matching events. For a flow step waiting on a build, `max_events: 1` with a pattern like `BUILD_PASSED|BUILD_FAILED` is canonical.
2. **A finite-running `cmd`** — e.g. `kubectl logs <pod>` without `-f`, or `tail -n 100 ...`.
3. **Explicit `taskstop`** within the same agent turn before emitting the terminal turn.

If none of the above applies AND the flow step has no `timeout`, the wait blocks until the orchestrator's surrounding context cancels. The synthetic Assistant timeout note (see `background-tasks` spec delta) will be injected and visible to the model on any subsequent invocation.

#### Scenario: Monitor with max_events terminates within a flow step's wait

- **GIVEN** a flow step's agent invokes `monitor` with `cmd: "tail", args: ["-F", "/tmp/build.log"], pattern: "BUILD_COMPLETED|BUILD_FAILED", max_events: 1`
- **AND** the agent then emits `struct_output`
- **WHEN** the tail subprocess emits a line matching the pattern
- **THEN** the monitor's coalesce loop MUST fire one `monitor-event` notification AND terminate the subprocess (max_events reached → SIGTERM)
- **AND** the terminal `StatusKilled` ("max_events reached") synthetic notification MUST land in the session
- **AND** the non-interactive wait MUST observe the monitor's terminal state
- **AND** the outer agentic loop MUST re-cycle so the model can emit a final struct_output referencing the matched event(s)

#### Scenario: Monitor without bound + no step timeout blocks until orchestrator cancels

- **GIVEN** a flow step's agent invokes `monitor` with `cmd: "tail", args: ["-F", "/tmp/never-appears.log"], pattern: "DONE"`
- **AND** the agent emits `struct_output` without calling `taskstop`
- **AND** the flow step has no `timeout` and no `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` is set
- **WHEN** the orchestrator's surrounding ctx cancels (e.g. overall flow deadline)
- **THEN** the wait MUST unblock with `ctx.Err()`
- **AND** the runtime MUST inject the synthetic Assistant timeout note into the session log (see background-tasks spec)
- **AND** the outer loop MUST break
- **AND** `agent.RunWith` MUST return the pre-wait `AgentEvent` so the flow runner can surface the result it has

#### Scenario: Monitor in interactive mode is unaffected

- **WHEN** a TUI session invokes the agent and the agent spawns a monitor
- **AND** the agent emits `end_turn`
- **THEN** `agent.Run` MUST return immediately (no wait, no auto-kill)
- **AND** the monitor MUST continue running until its natural termination condition
- **AND** the eventual terminal synthetic notification MUST auto-resume a new `agent.Run` via `task.deps.ResumeSession` as today
