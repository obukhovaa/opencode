# flow-runtime-resume Specification

## Purpose
TBD - created by archiving change flow-resume-semantics. Update Purpose after archive.
## Requirements
### Requirement: Flow runtime selects resume vs restart based on prior step state

When `flow.Service.Run` is invoked for a `(sessionPrefix, flowID)` pair that already has `flow_states` rows in the orchestrator MySQL store, the runtime MUST decide whether to **resume** (route initial work via `collectResumableSteps`, honoring per-step cached outputs for previously-completed steps) or **restart** (route initial work to step 0 with a fresh `stepWork`).

The decision is governed by a "resumable work" predicate over the existing rows that folds two concerns:

**(a) Status-driven** — any row in an in-flight status short-circuits the predicate to true:

- `running` — crash recovery
- `postponed` — explicit pause point awaiting wake
- `waiting_for_input` — interactive step awaiting reviewer reply
- `failed` — opt-in only, controlled by `flow.session.resume_on_failure`

**(b) Rule-walk-driven** — for `completed` rows, the runtime MUST evaluate the step's routing rules using the row's persisted args (merged with the row's struct output when `IsStructOutput` is true) and persisted iteration as `${step.iteration}`. The predicate is true if any rule evaluates to a target that is EITHER:

- the same step (self-route — the next iteration was never scheduled), OR
- a different step whose flow_states row is absent or non-terminal (`completed` and `failed`-without-resume are terminal; anything else is non-terminal).

A `completed` row whose rules produce no target (no rule matches; or no rules at all) does not contribute to the predicate. Rows for steps not present in `f.Spec.Steps` (stale state from a flow whose step IDs changed) are ignored.

`failed` rows do NOT participate in the rule walk: when `resume_on_failure` is true the status-driven branch above returns true unconditionally, and when it is false the failed row is treated as terminal. The implementation therefore exits via the status branch on every `failed` row and never reconstructs its rule context.

When the gate decides "resume" but the resume planner produces no work — possible when a self-loop's predicate depends on a caller arg that changed between the prior run and the re-trigger, so the gate's row-args walk matches but the planner's caller-args walk does not — the runtime MUST fall back to restart-from-step-0 instead of closing the channels empty. The gate is advisory in this direction; the planner's view of the current caller args is authoritative.

The runtime MUST call `collectResumableSteps` iff the predicate is true AND the caller did not pass `fresh = true`. Otherwise the runtime MUST construct initial work as a single `stepWork{step: f.Spec.Steps[0], args: copyArgs(args), iteration: 1}`.

The runtime MUST NOT delete per-step sessions (`s.sessions.DeleteTree` or `s.sessions.Delete`) on the restart-from-step-0 path. Per-step sessions are deleted ONLY when the caller passes `fresh = true`.

The `fresh = true` path is unchanged from the prior contract: existing `flow_states` rows are deleted via `DeleteFlowStatesByRootSession`, the session tree is deleted via `s.sessions.DeleteTree(rootSessionID)`, `existingStates` is set to nil, and initial work is routed to step 0.

The existing `hasRunning` early-return path (where `Run` fans the existing in-progress rows out to the `flowStates` channel without spawning new step work) is preserved as-is — it serves the cross-process replay case where another instance of `Run` is currently executing the flow, distinct from the re-trigger case covered here.

#### Scenario: Re-trigger of cleanly-completed prior run restarts from step 0

- **GIVEN** a flow `F` with `flow.session.prefix: "${args.k}"` and steps `[s0, s1, s2]`, and the orchestrator MySQL holds `flow_states` rows for `prefix-F-s0`, `prefix-F-s1`, `prefix-F-s2` all with `status = completed`, and the per-step sessions are non-empty
- **WHEN** `Run(ctx, sessionPrefix, F.ID, args, fresh=false)` is invoked
- **THEN** the runtime MUST emit a `flow.step.started` event for `s0` (restart from step 0), MUST NOT call `s.sessions.DeleteTree`, MUST NOT call `DeleteFlowStatesByRootSession`, and the per-step session for `s0` MUST retain its prior messages (cumulative LLM context)

#### Scenario: Re-trigger of run that ended in failure restarts when resume_on_failure is false

- **GIVEN** a flow `F` with `flow.session.resume_on_failure` unset (default `false`), and `flow_states` rows `[s0=completed, s1=failed]`
- **WHEN** `Run(…, fresh=false)` is invoked
- **THEN** the runtime MUST restart from step 0; the failed row for `s1` MUST NOT be re-used as the entry point

#### Scenario: Re-trigger of run that ended in failure resumes from failed step when resume_on_failure is true

- **GIVEN** the same `F` as the previous scenario except `flow.session.resume_on_failure: true`, with rows `[s0=completed, s1=failed]`
- **WHEN** `Run(…, fresh=false)` is invoked
- **THEN** the runtime MUST enter `collectResumableSteps`, MUST skip `s0` (status `completed` is routed via the existing skip path), and MUST schedule `s1` as initial work with the args persisted on its `failed` row

#### Scenario: Re-trigger of run with stuck running step recovers that step

- **GIVEN** rows `[s0=completed, s1=running]` (opencode pod crashed mid-step-1)
- **WHEN** `Run(…, fresh=false)` is invoked
- **THEN** the runtime MUST take the `hasRunning` early-return path and fan the existing rows out to the `flowStates` channel; the caller is expected to either let the existing process complete or call `Abort` and retry — this scenario does NOT route to restart, because the in-progress row represents work that may still be active

#### Scenario: Re-trigger wakes a postponed step

- **GIVEN** rows `[s0=completed, s1=postponed]` with `s1.iteration = 3` (the step parked itself awaiting an external event/timer)
- **WHEN** `Run(…, fresh=false)` is invoked
- **THEN** the runtime MUST enter `collectResumableSteps`; the first `stepWork` emitted MUST be for `s1` with `iteration = 3` and `prevStep` referencing the postponed row; `s0`'s completed output MUST be loaded into args via the existing skip-completed merge

#### Scenario: Re-trigger wakes a waiting_for_input step

- **GIVEN** rows `[s0=completed, s1=waiting_for_input]` (an interactive step awaiting reviewer reply)
- **WHEN** `Run(…, fresh=false)` is invoked
- **THEN** the runtime MUST enter `collectResumableSteps`; the first `stepWork` emitted MUST be for `s1`; the bridge bind (if any) is re-established via the existing interactive-step path

#### Scenario: fresh = true wipes everything regardless of status

- **GIVEN** any non-empty set of `flow_states` rows and a non-empty session tree under `rootSessionID`
- **WHEN** `Run(…, fresh=true)` is invoked
- **THEN** the runtime MUST call `DeleteFlowStatesByRootSession(rootSessionID)`, MUST call `s.sessions.DeleteTree(rootSessionID)`, MUST set the in-memory `existingStates` to nil, and MUST schedule initial work as a single `stepWork` for step 0; per-step sessions, including their messages, are gone

#### Scenario: First-ever run has no prior state

- **GIVEN** no `flow_states` rows for this `(sessionPrefix, flowID)`
- **WHEN** `Run(…, fresh=false)` is invoked
- **THEN** the runtime MUST schedule initial work for step 0 directly (the resumable-work predicate is false on an empty set); no resume logic engages

#### Scenario: Self-loop crash between iter-N-completed and iter-N+1-running resumes

- **GIVEN** a flow with a single step `loop` whose rules unconditionally self-route, and a single `flow_states` row `[loop: completed, iteration=2]` (the prior process completed iter 2 and died before writing iter 3's running row)
- **WHEN** `Run(…, fresh=false)` is invoked
- **THEN** the rule-walk branch of the predicate MUST evaluate `loop`'s rules against iteration=2, detect the self-route target `loop`, return true; the runtime MUST enter `collectResumableSteps`, which MUST schedule iter 3 (or trip `maxIterations` and route to the step's `fallback` when iter+1 exceeds the cap, preserving the contract of `TestSelfLoop_ResumeRespectsMaxIterationsCap` and `TestSelfLoop_ResumeAfterCompletedIterationCrash`)

#### Scenario: Self-loop terminated by predicate restarts on re-trigger

- **GIVEN** a flow with a step `loop` whose rule is `${step.iteration} != 3 → loop`, and a single completed row `[loop: completed, iteration=3]` (predicate flips false at iter 3, loop terminated normally)
- **WHEN** `Run(…, fresh=false)` is invoked
- **THEN** the rule-walk branch MUST find no matching rule at iter=3 and return no targets; the predicate MUST be false overall; the runtime MUST restart from step 0 (re-trigger semantics — the prior loop completed cleanly, a new trigger means a new run)

#### Scenario: Gate-vs-planner disagreement falls back to restart

- **GIVEN** a flow with a self-loop step `loop` whose rule is `${args.continue} == "yes" → loop`, a single completed row `[loop: completed, iteration=N, args={continue: "yes"}]` (the prior run was looping happily), and a re-trigger with `args = {continue: "no"}`
- **WHEN** `Run(…, fresh=false)` is invoked
- **THEN** the gate's rule-walk on the row's persisted `{continue: "yes"}` finds the self-route and returns true; `collectResumableSteps` walks the same rule with the CALLER's new `{continue: "no"}` and returns an empty work set; the runtime MUST detect the empty work set, log a `WARN`, and fall back to scheduling `[loop: iteration=1]` so the re-trigger executes against the new args instead of silently no-op'ing

### Requirement: FlowSession schema admits `resume_on_failure`

The `FlowSession` struct in `internal/flow/flow.go` MUST expose a `ResumeOnFailure bool` field tagged `yaml:"resume_on_failure,omitempty"`. The zero value (`false`) is the default behavior — `failed` rows count as terminal, and a re-trigger restarts from step 0.

`validateFlow` MUST accept `resume_on_failure` as a valid key inside the `session:` block AND MUST reject any other unrecognized key under `session:` with `ErrInvalidYAML`, naming the unknown key in the error message.

#### Scenario: Flow YAML with resume_on_failure: true is accepted

- **GIVEN** a flow YAML containing:
  ```yaml
  flow:
    session:
      prefix: "${args.build_id}"
      resume_on_failure: true
    steps: [...]
  ```
- **WHEN** the flow is loaded via the registry
- **THEN** `validateFlow` MUST succeed, and the loaded `FlowSpec.Session.ResumeOnFailure` MUST equal `true`

#### Scenario: Flow YAML with typo'd session key is rejected

- **GIVEN** a flow YAML containing `session: { prefix: "${args.x}", resume_on_fail: true }` (note the typo: missing trailing `ure`)
- **WHEN** the flow is loaded via the registry
- **THEN** `validateFlow` MUST return an error wrapping `ErrInvalidYAML`, and the error message MUST name the unknown key `"resume_on_fail"` so the author can fix the typo

#### Scenario: Flow YAML with no session block defaults to no failure resume

- **GIVEN** a flow YAML with no `session:` block at all
- **WHEN** the flow is loaded
- **THEN** `FlowSpec.Session.ResumeOnFailure` MUST be `false`, and a re-trigger of a failed run MUST restart from step 0 per the gating requirement above

### Requirement: Restart-from-step-0 preserves cumulative LLM context

The motivating use case for stable `session.prefix` is that successive re-triggers of the same external event-keyed flow accumulate LLM conversation history per step. The runtime MUST preserve this invariant.

When `Run` takes the restart-from-step-0 path under `fresh = false`:

- The runtime MUST NOT call `s.sessions.Delete`, `s.sessions.DeleteTree`, or any other operation that removes the messages, files, or other content of any per-step session.
- Each step's session is reused. When `runStep` resolves the session via `s.resolveSession`, the existing session is found and its message history is intact.
- The agent invoked at step 0 sees the prior turns as conversation context when the runtime calls `agent.Run` on that session.
- The `flow_states.status` row for each step transitions from `completed` → `running` (overwritten at `Run` entry, lines around the initial-work loop) → `completed` (overwritten at step end), so a future `ListFlowStatesByRootSession` reflects the latest run's outcomes.

#### Scenario: Re-trigger preserves per-step messages

- **GIVEN** a completed prior run for a flow with a single step `s0`, and `messages` table contains 10 rows for the session `prefix-F-s0`
- **WHEN** `Run(…, fresh=false)` is invoked again with new args
- **THEN** before `s0`'s agent runs, the session `prefix-F-s0` MUST still contain its 10 prior messages; after `s0`'s agent runs, the count MUST be strictly greater (the new run added its own turns)

#### Scenario: fresh = true wipes per-step messages

- **GIVEN** the same setup as the previous scenario
- **WHEN** `Run(…, fresh=true)` is invoked
- **THEN** `s.sessions.DeleteTree(rootSessionID)` MUST be called, which cascades to delete the messages; after `s0`'s agent runs, the message count for `prefix-F-s0` reflects only the new run's turns (the prior 10 are gone)

### Requirement: Flow steps invoke `agent.RunWith` with `NonInteractive: true`

Every flow step that delegates to the agent SHALL invoke the new `agent.Service.RunWith` entry point with `RunOptions{NonInteractive: true}`. This ensures the agent's end-of-turn wait engages for background tasks the step's agent spawns, so the step's `AgentEvent` (and any `struct_output` it produces) reflects the post-completion state rather than the immediate pre-completion ack.

Headless `cmd/flow.go`, `cmd/acp.go` one-shot invocations, and any other entry point whose lifetime ends with a single `agent.Run` return MUST also set `NonInteractive: true`. Interactive entry points (TUI loop, `internal/bridge/service/dispatch.go`) MUST leave the flag false (the default) so their existing auto-resume semantics are preserved.

#### Scenario: Per-step agent invocation in flow.Service.Run

- **GIVEN** a flow definition with a step whose agent spawns a background task
- **WHEN** the flow runner reaches that step
- **AND** invokes `agentSvc.RunWith(ctx, sessionID, prompt, step.MaxTurns, RunOptions{NonInteractive: true})`
- **THEN** the resulting `AgentEvent` delivered to the flow runner MUST reflect the agent's response AFTER the background task completed
- **AND** the flow runner MUST advance to the next step using that post-completion response

#### Scenario: Headless CLI flow invocation

- **WHEN** the user runs `opencode flow <name>` from the shell (non-TUI mode)
- **AND** the flow's steps use background tasks
- **THEN** the CLI entry point MUST set `NonInteractive: true` on every per-step agent invocation
- **AND** the CLI MUST exit only after every step's background work has completed (subject to the step / env-var timeout chain — see next requirement)

#### Scenario: ACP one-shot agent invocation

- **WHEN** an ACP client triggers a one-shot agent invocation via the ACP server
- **AND** the agent spawns background tasks
- **THEN** the ACP server MUST set `NonInteractive: true` on the `agent.RunWith` call
- **AND** the SSE stream MUST emit the synthetic completion `message.created` events AND the post-completion final assistant message
- **AND** the response delivered to the ACP client MUST be the post-completion state

### Requirement: Per-step `timeout` field bounds the non-interactive wait

The `Step` struct in `internal/flow/flow.go` SHALL gain a `Timeout` field, parseable from step YAML as a Go duration string (e.g. `"15m"`):

```yaml
steps:
  - id: integration-test
    agent: coder
    prompt: "Run the integration suite and produce a struct_output with results"
    timeout: 30m
```

When `Step.Timeout > 0`, the flow runner SHALL wrap the agent invocation's ctx with `context.WithTimeout(parentCtx, step.Timeout)` before calling `agent.RunWith`. The deadline applies to the entire `RunWith` invocation — including the inner agentic loop AND the non-interactive end-of-turn wait — and cascades through to `WaitForActiveTasks`.

#### Scenario: Step.Timeout caps the wait

- **GIVEN** a flow step with `timeout: 30s` whose agent spawns a 10-minute background bash task
- **WHEN** the model emits `struct_output` and the non-interactive wait begins
- **THEN** the wait MUST unblock at the 30-second mark via `ctx.Err()`
- **AND** the synthetic Assistant timeout note MUST be injected into the session
- **AND** the outer agentic loop MUST break and `agent.RunWith` MUST return

#### Scenario: Step without timeout inherits ENV default if set

- **GIVEN** `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT=5m` is set in the process environment
- **AND** a flow step has no explicit `timeout` field
- **WHEN** the flow runner builds the ctx for this step
- **THEN** the runner MUST apply `context.WithTimeout(parent, 5m)` to bound the step
- **AND** the wait inside `agent.RunWith` MUST respect that 5-minute deadline

#### Scenario: Step timeout always wins over ENV default

- **GIVEN** `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT=5m` is set
- **AND** a step has `timeout: 1h`
- **WHEN** the flow runner builds the ctx
- **THEN** the runner MUST apply `context.WithTimeout(parent, 1h)` (the step value)
- **AND** the ENV default MUST be ignored for that step

#### Scenario: Neither step timeout nor ENV default = unbounded wait

- **GIVEN** no `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` is set
- **AND** the step has no `timeout` field
- **WHEN** the flow runner builds the ctx
- **THEN** the runner MUST pass the parent ctx unwrapped (only the orchestrator's surrounding deadline applies)
- **AND** if no orchestrator deadline exists, the wait blocks until the work completes or the process exits

### Requirement: Interactive entry points leave `NonInteractive: false`

The TUI loop, the chat bridge dispatch loop, and the auto-resume callback in `task.deps.ResumeSession` MUST NOT set `NonInteractive: true`. These callers rely on the existing auto-resume semantics where background completions trigger a fresh `agent.Run` that publishes a new assistant message to the broker. Forcing the wait inside these callers would unnecessarily serialize work that the message broker already handles asynchronously.

#### Scenario: TUI agent.Run uses the original 4-arg signature

- **WHEN** the TUI submits user input to the agent
- **AND** invokes `agentSvc.Run(ctx, sessionID, content, maxTurns)` (or `RunWith` with zero-value options)
- **AND** the agent spawns a background task
- **THEN** `Run` MUST return as today (immediately after the inner agentic loop exits)
- **AND** the eventual synthetic completion MUST auto-resume via `task.deps.ResumeSession`

#### Scenario: Bridge dispatch uses the original 4-arg signature

- **WHEN** the chat bridge receives an inbound message
- **AND** invokes `agentSvc.Run(ctx, sessionID, content, maxTurns)` via its dispatch loop
- **AND** the agent spawns a background task
- **THEN** `Run` MUST return as today
- **AND** the eventual synthetic completion MUST auto-resume, with the new assistant message fanned out to the chat platform via the existing parts-broker subscriber
