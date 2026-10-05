## MODIFIED Requirements

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

**Crash recovery (`RunOptions.RecoverRunning`).** A `running` row is normally taken to mean another process is executing the flow right now, so the runtime fans the existing rows out to the `flowStates` channel without scheduling work (the `hasRunning` early return below). `Service.RunWithOptions(…, RunOptions{RecoverRunning: true})` is the caller's assertion that the owner of those rows is dead — an orchestrator has observed the pod executing the step get OOM-killed, evicted or deadline-killed. With it the runtime MUST skip the early return and let the running rows reach the predicate above, which treats them as in-flight; `collectResumableSteps` then MUST re-enter each running step in its own session with the row's persisted args and iteration, completed rows keep their cached outputs, and the runtime MUST delete nothing. `Run(…, fresh)` is the back-compat shim for `RunWithOptions(…, RunOptions{Fresh: fresh})`. A caller that cannot vouch for the owner's death MUST NOT set `RecoverRunning`: two live processes would execute the same step.

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
- **THEN** the runtime MUST take the `hasRunning` early-return path and fan the existing rows out to the `flowStates` channel; the caller is expected to either let the existing process complete, call `Abort` and retry, or — when it knows the owning process is dead — re-run with `RecoverRunning: true` (next scenario). This scenario does NOT route to restart, because the in-progress row may represent work that is still active

#### Scenario: Orchestrator recovers a step whose pod was killed

- **GIVEN** rows `[s0=completed, s1=running]` and the orchestrator has observed the pod executing `s1` terminate (OOMKilled, evicted or deadline-killed)
- **WHEN** `RunWithOptions(…, RunOptions{RecoverRunning: true})` is invoked with the same session prefix
- **THEN** the runtime MUST NOT take the `hasRunning` early return; it MUST enter `collectResumableSteps`, skip `s0` via the completed path, and schedule `s1` as initial work with the args and iteration persisted on its `running` row, in `s1`'s existing session; it MUST NOT delete any flow state or session; `s1` runs to a terminal state

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
