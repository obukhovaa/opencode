## ADDED Requirements

### Requirement: Non-interactive `agent.Run` MUST hold the turn until pending background tasks complete

When `agent.Service.RunWith` is invoked with `RunOptions{NonInteractive: true}`, the runtime SHALL NOT return until every running background task associated with the session (regardless of `Kind` — bash, task, AND monitor) has reached a terminal state (`StateCompleted`, `StateFailed`, or `StateKilled`), or until the surrounding `ctx` is cancelled.

The wait MUST be performed AFTER the model emits its terminal turn (`end_turn` or `struct_output`) for the current agentic cycle, and BEFORE the `AgentEvent` is delivered to the caller. After the wait completes successfully, the runtime SHALL reload the session's message history and re-enter the agentic loop for at least one additional cycle so the model has a chance to react to the just-arrived synthetic completion(s).

The wait MUST NOT impose its own timeout — the surrounding `ctx` is the sole deadline source. See `flow-runtime-resume` for how callers derive the ctx deadline from `Step.Timeout` and the `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` env var.

#### Scenario: Flow step waits for background bash before returning struct_output

- **WHEN** a flow step invokes `agent.RunWith(..., RunOptions{NonInteractive: true})`
- **AND** the model calls `bash` with `run_in_background: true` mid-turn
- **AND** the model then emits `struct_output` for the step
- **THEN** `agent.RunWith` MUST NOT return immediately
- **AND** the runtime MUST wait for the background bash subprocess to exit and write its synthetic completion pair into the session
- **AND** the runtime MUST re-enter the agentic loop so the model can observe the synthetic Tool result
- **AND** the `AgentEvent.StructOutput` returned to the flow runner MUST reflect the model's response generated AFTER the synthetic completion arrived

#### Scenario: Flow step waits for monitor with max_events to terminate

- **WHEN** a flow step's agent spawns `monitor` with `max_events: 1` pattern matching an expected event
- **AND** the agent emits `struct_output`
- **THEN** the runtime MUST wait until the monitor reaches a terminal state (event matched + max_events triggers SIGTERM, OR subprocess exits naturally, OR taskstop)
- **AND** the final struct_output delivered to the flow runner MUST reflect the post-monitor-completion response

#### Scenario: Interactive agent.Run is unaffected

- **WHEN** `agent.Run` is invoked (the original 4-arg form, or `RunWith` with `NonInteractive: false`)
- **AND** the model spawns a background bash task
- **AND** the model then emits `end_turn`
- **THEN** `agent.Run` MUST return as today (synchronously after the inner agentic loop exits)
- **AND** the background task's eventual synthetic completion MUST trigger a fresh `agent.Run` via `task.deps.ResumeSession` as today

#### Scenario: Wait respects the surrounding context deadline

- **GIVEN** the caller passes a context with a 30-second deadline (e.g. `flow.Service` wrapped step.Timeout)
- **WHEN** the background task is still running at the 30-second mark
- **THEN** the wait MUST unblock with `ctx.Err()`
- **AND** the runtime MUST inject a synthetic Assistant timeout note into the session log (see next requirement)
- **AND** the outer agentic loop MUST break
- **AND** `agent.RunWith` MUST return the latest `AgentEvent` (the pre-wait terminal turn)

### Requirement: Task registry exposes a wait primitive

The `task.Registry` interface SHALL expose two new methods:

```
PendingForSession(sessionID string, filter func(*Task) bool) []*Task
WaitForActiveTasks(ctx context.Context, sessionID string, opts WaitOptions) error
```

Where `WaitOptions` is:

```
type WaitOptions struct {
    IncludeMonitor bool // default true in non-interactive mode (see monitor-tool spec)
}
```

`WaitForActiveTasks` MUST block until every task included in the snapshot transitions to a terminal state, OR until ctx is cancelled. The implementation MUST signal completion via a per-task `done chan struct{}` closed exactly once in `Registry.MarkFinished` and `Registry.Kill`.

The wait MUST use snapshot-at-start semantics: tasks registered AFTER the wait begins are NOT included in the wait set. This keeps the contract bounded and deterministic.

#### Scenario: Wait returns when all pending tasks finish

- **GIVEN** two `bash run_in_background` tasks and one `monitor` task are running for session `S`
- **WHEN** `WaitForActiveTasks(ctx, "S", WaitOptions{IncludeMonitor: true})` is called
- **AND** all three tasks reach terminal state
- **THEN** the wait MUST return `nil` within milliseconds of the last task's completion

#### Scenario: Concurrent task registration is not retroactively waited

- **GIVEN** one task is pending and `WaitForActiveTasks` has been called
- **WHEN** a second task is `Register`'d 50ms later
- **AND** the first task completes 100ms after the wait began
- **THEN** `WaitForActiveTasks` MUST return `nil` at the 100ms mark
- **AND** the second task's lifecycle MUST NOT be observed by this wait call

### Requirement: Synthetic Assistant timeout note on wait cancellation

When the non-interactive wait returns `ctx.Err()` (the surrounding ctx was cancelled or its deadline elapsed) AND there are still tasks pending, the runtime SHALL inject a synthetic Assistant message into the session message log BEFORE breaking the outer agentic loop. The message has Role=`Assistant`, Parts=`[TextContent]`, and `Synthetic: true`.

The text body MUST enumerate every still-pending task's `task_id`, `Kind`, `started_at` (RFC3339), `output_file` path, and a short description. It MUST explicitly state that the step's terminal turn was produced WITHOUT observing these completions, and recommend that any subsequent invocation on this session inspect the `output_file` paths before re-spawning equivalent work.

This message is `Synthetic: true` so the bridge filter (`if ev.Payload.Synthetic { return }`) skips it for outbound chat indicators. Non-bridge consumers (transcript export, ACP SSE replay, the model on re-invocation) observe it as a normal assistant text turn.

#### Scenario: Timeout produces an observable note for the next invocation

- **GIVEN** a non-interactive `agent.RunWith` is waiting for a long-running bash task
- **AND** the surrounding ctx cancels after the step's timeout elapses
- **WHEN** the wait unblocks with `ctx.Err()`
- **THEN** the runtime MUST write exactly one synthetic Assistant text message into the session
- **AND** that message MUST contain the still-pending task IDs, output_file paths, kinds, and start times
- **AND** any subsequent `agent.Run` on this session MUST observe the timeout note in the message history
