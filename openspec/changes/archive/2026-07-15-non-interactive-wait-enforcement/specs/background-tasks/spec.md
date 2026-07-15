## MODIFIED Requirements

### Requirement: Non-interactive `agent.Run` MUST hold the turn until pending background tasks complete

When `agent.Service.RunWith` is invoked with `RunOptions{NonInteractive: true}`, the runtime SHALL NOT return until every running background task associated with the session (regardless of `Kind` — bash, task, AND monitor) has reached a terminal state (`StateCompleted`, `StateFailed`, or `StateKilled`), or until the surrounding `ctx` is cancelled.

This guarantee MUST NOT be bypassable by model behavior. It is enforced through two complementary mechanisms:

1. **End-of-turn drain.** After the model emits a terminal turn (`end_turn` or `struct_output`) for the current agentic cycle, and BEFORE the `AgentEvent` is delivered to the caller, the runtime calls `WaitForActiveTasks`. On a `nil` return the runtime re-reads the session's pending tasks and, if any remain (e.g. tasks spawned in a later cycle after an earlier wait's snapshot), waits again — looping until the session has zero pending tasks or `ctx` is cancelled. After each successful wait the runtime reloads the session's message history and re-enters the agentic loop for at least one additional cycle so the model can react to the just-arrived synthetic completion(s). The `WaitForActiveTasks` primitive keeps its snapshot-at-start semantics; the drain loop lives in the agent.

2. **Anti-spin.** While the session has pending non-monitor background tasks (bash or task), the runtime SHALL NOT allow the model to consume wall-clock time in a foreground self-wait. The canonical case — a foreground `bash` command whose sole effect is to sleep — MUST be redirected to `WaitForActiveTasks` rather than executed as a sleep (see `bash-background-mode`). This ensures the guarantee holds even when the model never voluntarily emits a terminal turn but instead attempts to poll. (Long-lived monitors are excluded from the redirect; they are bounded by the end-of-turn drain above, not by a mid-turn sleep.)

The wait MUST NOT impose its own timeout — the surrounding `ctx` is the sole deadline source. See `flow-runtime-resume` for how callers derive the ctx deadline from `Step.Timeout` and the `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` env var.

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

## ADDED Requirements

### Requirement: No-poll guidance is delivered independent of the agent's system prompt

The runtime guidance that background tasks are event-driven and MUST NOT be polled (i.e. the model must not `sleep` and re-check, and a synthetic completion arrives automatically) SHALL be present in the assembled system prompt for EVERY agent that has tool access (per the agent registry's `HasTools`), including agents that supply a custom system prompt via `info.Prompt`. It MUST NOT live exclusively in a base prompt (such as `CoderPrompt`) that is skipped when a custom prompt is set. Tool-less agents (e.g. summarizer, descriptor) are exempt — they cannot spawn or poll background work, so the guidance would be dead prompt weight on every title/summarize call.

This is defense-in-depth: the anti-spin enforcement (see the hold-the-turn requirement and `bash-background-mode`) makes polling harmless, but correct guidance reduces wasted cycles and duplicate task dispatch.

#### Scenario: Custom-prompt agent still receives the no-poll contract

- **GIVEN** an agent registered with a non-empty custom `info.Prompt` (e.g. `composer-developer`)
- **WHEN** the system prompt is assembled for a run
- **THEN** the assembled prompt MUST include the "background tasks are event-driven; do NOT poll/sleep; completions arrive as synthetic tool results" guidance
- **AND** this MUST hold even though the base `CoderPrompt` is not appended for a custom-prompt agent

#### Scenario: Async ack does not frame the output file as a poll target

- **WHEN** the `task async` ack is returned to the parent agent
- **THEN** the ack MAY include the `output_file` path for resume/inspection semantics
- **AND** the ack MUST NOT instruct or imply that the agent should read the output file to poll for progress
- **AND** the ack MUST state that in a non-interactive step the runtime holds the turn until the subagent completes
