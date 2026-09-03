# background-tasks (delta)

Delta spec for the `mcp-call-deadline-bounds` change. Restates only the requirement
that changes; unchanged requirements are not repeated here. For the full specification
see `openspec/specs/background-tasks/spec.md`.

The wait condition, the two enforcement mechanisms (end-of-turn drain and anti-spin),
and the deadline model are all UNCHANGED — the requirement body below is reproduced
verbatim because a MODIFIED requirement replaces the whole block. The only change is
the progress-log addendum and its three scenarios at the end.

## MODIFIED Requirements

### Requirement: Non-interactive `agent.Run` MUST hold the turn until pending background tasks complete

When `agent.Service.RunWith` is invoked with `RunOptions{NonInteractive: true}`, the runtime SHALL NOT return until every running background task associated with the session (regardless of `Kind` — bash, task, AND monitor) has reached a terminal state (`StateCompleted`, `StateFailed`, or `StateKilled`), or until the surrounding `ctx` is cancelled.

This guarantee MUST NOT be bypassable by model behavior. It is enforced through two complementary mechanisms:

1. **End-of-turn drain.** After the model emits a terminal turn (`end_turn` or `struct_output`) for the current agentic cycle, and BEFORE the `AgentEvent` is delivered to the caller, the runtime calls `WaitForActiveTasks`. On a `nil` return the runtime re-reads the session's pending tasks and, if any remain (e.g. tasks spawned in a later cycle after an earlier wait's snapshot), waits again — looping until the session has zero pending tasks or `ctx` is cancelled. After each successful wait the runtime reloads the session's message history and re-enters the agentic loop for at least one additional cycle so the model can react to the just-arrived synthetic completion(s). The `WaitForActiveTasks` primitive keeps its snapshot-at-start semantics; the drain loop lives in the agent.

2. **Anti-spin.** While the session has pending non-monitor background tasks (bash or task), the runtime SHALL NOT allow the model to consume wall-clock time in a foreground self-wait. The canonical case — a foreground `bash` command whose sole effect is to sleep — MUST be redirected to `WaitForActiveTasks` rather than executed as a sleep (see `bash-background-mode`). This ensures the guarantee holds even when the model never voluntarily emits a terminal turn but instead attempts to poll. (Long-lived monitors are excluded from the redirect; they are bounded by the end-of-turn drain above, not by a mid-turn sleep.)

The wait MUST NOT impose its own timeout — the surrounding `ctx` is the sole deadline source. See `flow-runtime-resume` for how callers derive the ctx deadline from `Step.Timeout` and the `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` env var.

The following addendum is added by this change:

While the non-interactive drain is waiting for pending background tasks, the runtime
SHALL emit a periodic progress log at a fixed interval naming the tasks it is still
waiting on — each task's `task_id`, `Kind`, and age — so that a drain holding a step
open is distinguishable in the process log from a hung or dead process. The log SHALL
NOT be emitted when the drain returns without waiting (zero pending tasks).

The progress log is observability only. It MUST NOT terminate, shorten, or otherwise
influence the wait: the sentence above stating that the wait imposes no timeout of its
own remains in force, and a future reader MUST NOT mistake the interval timer for a
deadline.

#### Scenario: Flow step waits for background bash before returning struct_output

- **WHEN** a flow step invokes `agent.RunWith(..., RunOptions{NonInteractive: true})`
- **AND** the model calls `bash` with `run_in_background: true` mid-turn
- **AND** the model then emits `struct_output` for the step
- **THEN** `agent.RunWith` MUST NOT return immediately
- **AND** the runtime MUST wait for the background bash subprocess to exit and write its synthetic completion pair into the session
- **AND** the runtime MUST re-enter the agentic loop so the model can observe the synthetic Tool result
- **AND** the `AgentEvent.StructOutput` returned to the flow runner MUST reflect the model's response generated AFTER the synthetic completion arrived

#### Scenario: Model attempts to self-poll with sleep while tasks pending

- **GIVEN** a non-interactive flow step has spawned one or more `task async` subagents that are still running
- **WHEN** the model, instead of emitting a terminal turn, issues a foreground `bash` command whose sole effect is `sleep N` (optionally followed by an `echo`)
- **THEN** the runtime MUST NOT execute the sleep
- **AND** the runtime MUST instead wait for the pending background tasks to reach terminal state (bounded by the surrounding `ctx`)
- **AND** the tool result returned to the model MUST summarize the tasks that completed during the wait
- **AND** no foreground process SHALL consume the requested sleep duration

#### Scenario: Drain covers tasks spawned across multiple turns

- **GIVEN** a non-interactive step's agent spawns a first wave of async subagents, then in a later cycle spawns a second wave
- **WHEN** the runtime enters the end-of-turn wait after the first wave and that wave completes
- **THEN** the runtime MUST re-check pending tasks and observe the second wave
- **AND** the runtime MUST wait again until the session has zero pending tasks or `ctx` is cancelled
- **AND** `agent.RunWith` MUST NOT return while any spawned task for the session is still running

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
- **AND** a foreground `bash sleep` MUST execute normally (no anti-spin redirection)
- **AND** the background task's eventual synthetic completion MUST trigger a fresh `agent.Run` via `task.deps.ResumeSession` as today

#### Scenario: Wait respects the surrounding context deadline

- **GIVEN** the caller passes a context with a 30-second deadline (e.g. `flow.Service` wrapped step.Timeout)
- **WHEN** the background task is still running at the 30-second mark
- **THEN** the wait MUST unblock with `ctx.Err()`
- **AND** the runtime MUST inject a synthetic Assistant timeout note into the session log
- **AND** the outer agentic loop MUST break
- **AND** `agent.RunWith` MUST return the latest `AgentEvent` (the pre-wait terminal turn)

#### Scenario: Long drain is observable in the log

- **GIVEN** a non-interactive step's agent has emitted its terminal turn with one
  background task still running
- **WHEN** the task remains running for several multiples of the progress interval
- **THEN** the process log contains repeated progress entries naming that task's
  `task_id`, `Kind`, and age
- **AND** the drain does not return until the task reaches a terminal state or `ctx` is
  cancelled

#### Scenario: Progress log does not bound the wait

- **GIVEN** a non-interactive drain is waiting on a task that never terminates
- **AND** the surrounding `ctx` has no deadline
- **WHEN** many progress intervals elapse
- **THEN** the drain is still waiting
- **AND** no timeout has been imposed by the drain itself

#### Scenario: No progress log when nothing is pending

- **WHEN** the drain is entered for a session with zero pending background tasks
- **THEN** it returns immediately
- **AND** no progress entry is logged
