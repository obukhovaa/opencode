## ADDED Requirements

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
