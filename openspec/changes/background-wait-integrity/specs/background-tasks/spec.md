## ADDED Requirements

### Requirement: Registry exposes a session-and-children scoped pending lookup

The task registry SHALL expose a pending-task lookup covering the queried session **plus
sessions whose parent is that session** (one level of descent), alongside the existing
exact-session lookup.

The scope is deliberately NOT the flow `root_session_id`. A flow assigns one root to every
step and steps run concurrently, so root scope would let a `sleep` in one parallel branch
block on an unrelated branch's background work. One level of descent covers the
parent-agent → subagent case without that blast radius; deeper nesting degrades to
exact-session behavior, which is the pre-existing behavior and therefore not a regression.

Two lookups exist with distinct, non-interchangeable callers:

| Caller | Scope | Rationale |
|---|---|---|
| End-of-turn drain (`agent.Run` non-interactive hold) | exact session | A parent MUST NOT block at end of turn on a child's tasks. |
| Foreground-wait redirect (`bash` sleep interception) | session + direct children | The model explicitly asked to wait; its own children's pending work is what it is waiting for. |

Resolving "children of the caller" requires the caller's session identity, which the tool
layer cannot derive from a session ID alone — `internal/llm/tools` holds no session
service. The runtime SHALL therefore place the caller's session identity on the
tool-execution context alongside the non-interactive marker, at the same site where the
session row is already loaded. An implementation that omits this plumbing degrades the
lookup to exact scope silently; a test MUST cover the plumbing, not only the registry
method.

Introducing this lookup MUST NOT change the drain's scope or behavior.

#### Scenario: Lookup spans parent and its direct child sessions

- **WHEN** a subagent session whose parent is `S` has one running bash task
- **AND** the lookup is called with `S`
- **THEN** the subagent's task is returned

#### Scenario: Parallel sibling sessions are excluded

- **GIVEN** sessions `A` and `B` are sibling steps of one flow sharing a `root_session_id`
- **AND** `B` has a running bash task
- **WHEN** the lookup is called with `A`
- **THEN** `B`'s task is NOT returned

#### Scenario: A session with no children degrades to exact scope

- **WHEN** the lookup is called with a session that has no child sessions
- **THEN** it returns exactly what the exact-session lookup returns

#### Scenario: Drain scope is unchanged

- **WHEN** a parent session's turn ends while only a child session's task is running
- **THEN** the parent's end-of-turn drain does NOT wait on it

## MODIFIED Requirements

### Requirement: Task registry exposes a wait primitive

The `task.Registry` interface SHALL expose these methods:

```
PendingForSession(sessionID string, filter func(*Task) bool) []*Task
PendingForSessionTree(sessionID string, filter func(*Task) bool) []*Task
WaitForActiveTasks(ctx context.Context, sessionID string, opts WaitOptions) error
```

Where `WaitOptions` is:

```
type WaitOptions struct {
    IncludeMonitor bool // default true in non-interactive mode (see monitor-tool spec)
    Scope          WaitScope // ScopeExactSession (zero value) | ScopeSessionAndChildren
}
```

`Scope` selects which tasks the snapshot contains. `ScopeExactSession` — the zero value, and therefore the behavior of every existing caller — includes only tasks whose owning session equals `sessionID`. `ScopeSessionAndChildren` additionally includes tasks owned by sessions whose parent is `sessionID` (one level of descent; NOT the flow `root_session_id`, which is shared by every step of a flow including parallel siblings).

`Scope` MUST be honored by BOTH the pending lookup and the internal re-snapshot inside `WaitForActiveTasks`. A caller that widens its pre-check without widening the wait would return immediately having waited for nothing, while reporting tasks it never observed.

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

#### Scenario: Scope defaults to exact session

- **WHEN** `WaitForActiveTasks` is called with a zero-value `WaitOptions`
- **THEN** only tasks owned by `sessionID` itself are included, identical to prior behavior

#### Scenario: Child-owned tasks are included under ScopeSessionAndChildren

- **GIVEN** a subagent session whose parent is `S` has one running bash task
- **WHEN** `WaitForActiveTasks(ctx, "S", WaitOptions{Scope: ScopeSessionAndChildren})` is called
- **THEN** the wait blocks on the child's task

#### Scenario: Parallel sibling sessions are NOT included

- **GIVEN** sessions `A` and `B` are sibling steps of one flow, sharing a `root_session_id`
- **AND** `B` has a running bash task
- **WHEN** `WaitForActiveTasks(ctx, "A", WaitOptions{Scope: ScopeSessionAndChildren})` is called
- **THEN** `B`'s task MUST NOT be included in the wait set

### Requirement: Non-interactive `agent.Run` MUST hold the turn until pending background tasks complete

When `agent.Service.RunWith` is invoked with `RunOptions{NonInteractive: true}`, the runtime SHALL NOT return until every running background task associated with the session (regardless of `Kind` — bash, task, AND monitor) has reached a terminal state (`StateCompleted`, `StateFailed`, or `StateKilled`), or until the surrounding `ctx` is cancelled.

This guarantee MUST NOT be bypassable by model behavior. It is enforced through two complementary mechanisms:

1. **End-of-turn drain.** After the model emits a terminal turn (`end_turn` or `struct_output`) for the current agentic cycle, and BEFORE the `AgentEvent` is delivered to the caller, the runtime calls `WaitForActiveTasks`. On a `nil` return the runtime re-reads the session's pending tasks and, if any remain (e.g. tasks spawned in a later cycle after an earlier wait's snapshot), waits again — looping until the session has zero pending tasks or `ctx` is cancelled. After each successful wait the runtime reloads the session's message history and re-enters the agentic loop for at least one additional cycle so the model can react to the just-arrived synthetic completion(s). The `WaitForActiveTasks` primitive keeps its snapshot-at-start semantics; the drain loop lives in the agent.

2. **Anti-spin.** While the session OR ITS DIRECT CHILD SESSIONS have pending non-monitor background tasks (bash or task), the runtime SHALL NOT allow the model to consume wall-clock time in a foreground self-wait. The canonical case — a foreground `bash` command whose sole effect is to sleep — MUST be redirected to `WaitForActiveTasks` — called with `Scope: ScopeSessionAndChildren` — rather than executed as a sleep (see `bash-background-mode`). The redirect's scope is deliberately wider than the end-of-turn drain's, which stays exact-session: a parent must not block at end of turn on a child's tasks, but a parent that explicitly asks to wait does mean its own children. Because the redirect can now block on a child-owned task, `tasklist` and `taskstop` move to the same scope (see `tasklist-taskstop-tools`) so the model can observe and cancel whatever it is blocked on. This ensures the guarantee holds even when the model never voluntarily emits a terminal turn but instead attempts to poll. (Long-lived monitors are excluded from the redirect; they are bounded by the end-of-turn drain above, not by a mid-turn sleep.)

The wait MUST NOT impose its own timeout: it never returns early on a task that is still pending, and the surrounding `ctx` is the only deadline it applies. See `flow-runtime-resume` for how callers derive the ctx deadline from `Step.Timeout` and the `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` env var.

That bounds the *wait*. The hold on a `task`-kind entry may also end earlier because the task itself reached a terminal state, which `background-task-stall-detection` can bring about — so the deadline sources for the hold are `Step.Timeout`, `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT`, and `backgroundTasks.stallThreshold`. No task that is making progress is ever terminated, regardless of total runtime, and `bash`, `monitor` and `cron` tasks are never terminated on progress grounds at all.

While the non-interactive drain is waiting for pending background tasks, the runtime
SHALL emit a periodic progress log at a fixed interval naming the tasks it is still
waiting on — each task's `task_id`, `Kind`, and age — so that a drain holding a step
open is distinguishable in the process log from a hung or dead process. The log SHALL
NOT be emitted when the drain returns without waiting (zero pending tasks).

The progress log is observability only. It MUST NOT terminate, shorten, or otherwise
influence the wait, and a future reader MUST NOT mistake the interval timer for a
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

#### Scenario: A stalled subagent task ends the hold without ctx cancellation

- **GIVEN** a non-interactive step whose agent has emitted an accepted
  `struct_output`
- **AND** one pending `task`-kind entry that has made no progress for longer
  than `backgroundTasks.stallThreshold`
- **WHEN** the task is terminated as stalled
- **THEN** the drain returns because every pending task is terminal, not because
  `ctx` was cancelled
- **AND** the outer loop re-enters the agentic loop as it does for any other
  terminal task

#### Scenario: An exempt pending task still holds the turn indefinitely

- **GIVEN** stall detection is enabled
- **AND** the only pending task is a `monitor` whose pattern has not matched
- **AND** the surrounding `ctx` has no deadline
- **THEN** the wait does not return until that monitor reaches a terminal state
- **AND** the drain imposes no deadline of its own upon it

