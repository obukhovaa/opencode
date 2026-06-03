# In-Process Step Loops & `${step.iteration}`

**Date**: 2026-06-02
**Status**: Implemented

## Implementation Progress

- [x] Phase 1 — Schema migrations (mysql + sqlite) + canonical schema
- [x] Phase 2 — sqlc queries + regenerate + MySQLQuerier adapter
- [x] Phase 3 — Step.MaxIterations + validation
- [x] Phase 4 — service.go core changes (stepWork.iteration, FlowState.Iteration, narrowed diamond guard, step scope, max-iter pre-check, resume carries iteration, FlowStepIterationContextKey)
- [x] Phase 5 — Tests (10 new tests: scope predicate, scope template, not-in-args, in-process loop, diamond preserved, max-iter cap, template substitution, postpone stores iteration). Plus stub mutex hardening to fix `-race` flakes on parallel branches.
- [x] Phase 6 — CLI iteration indicator (added `iteration` to `cmd/flow.go` stepResult JSON; no live TUI flow component exists so spinner-title update was descoped)
- [x] Phase 7 — Langfuse telemetry (`flow_step_iteration` metadata + trace-name suffix `#N` when iter > 1)
- [x] Phase 8 — Docs update (Step fields table extended w/ `maxIterations` + `maxTurns`, Diamond Convergence amended, Self-loops section added, Template Substitution + predicate scope docs, Output JSON example)
- [x] Phase 9 — Skill update (`.agents/skills/flow-creator/` updated with `maxIterations`, `maxTurns`, `${step.iteration}`, in-process self-loop pattern)
- [x] Phase 10 — `make test` green (`-race`, full suite); coverage in `internal/flow` is 80.1% after post-review regression tests

## Post-Review Fixes

Two bugs and three concerns surfaced by code review were resolved in a follow-up pass:

### Bug fixes

- **`service.go:540-563` — postpone-self at iteration N>1 lost the counter.** The successor-enqueue branch only carried iteration for the in-process self-route case; the postpone-self case fell into the default branch and reset iteration to 1. The persisted `postponed` row therefore recorded iteration 1 regardless of which iteration actually decided to postpone, so resume restarted at iter 1. Fix: explicit `switch` covers three cases — in-process self bumps, postpone-self carries unchanged, foreign target resets to 1. Regression test: `TestSelfLoop_PostponeAtIterationNGreaterThanOne`.
- **`service.go:660-688` — resume after crash between iter-N-completed and iter-N+1-running writes dropped the loop.** `collectResumableSteps` marked `visited[step.ID]=true` before walking the completed step's rules; the recursive call for a self-route hit the visited guard and returned nil, leaving the flow with no scheduled iter N+1. Narrow window in practice but a real silent stall. Fix: when a self-route is resolved from a completed iteration, emit the next iteration's `stepWork` directly with `iteration = existing.Iteration + 1` instead of recursing. Regression test: `TestSelfLoop_ResumeAfterCompletedIterationCrash`.

### Concerns addressed

- **`agent.go:1717-1721` — double truncation could strip the `#N` iteration suffix.** When the agent/flow/step concatenation already hit `maxTraceNameLen`, appending `#N` and re-truncating clipped the suffix. Now the full name (including suffix) is composed once and truncated once.
- **`TestSelfLoop_InProcess` assertion strengthened.** Beyond agent call count, the test now snapshots prompts and verifies each iteration's prompt carries the correct `${step.iteration}` value in order — catches off-by-one bugs in the bump path.
- **`docs/flows.md` `required:` wording corrected.** The previous text implied the flow runner enforces the output schema's `required` after the fact. Updated to clarify that enforcement comes from the model's structured-output API, not the runner.
- **Resume path now respects `MaxIterations` cap (`service.go:701-708` + new `failResumedSelfLoop`).** Originally, the resume direct-emit path scheduled iter N+1 unconditionally. If iter N had been the cap-tripping iteration (process crashed before the pre-check ran), the resumed iter N+1 would run the agent (one wasted LLM call) before the post-step check finally failed the step. Now `collectResumableSteps` checks the cap before emitting; if exceeded it persists the failed row, emits the failed state, and enqueues the fallback — mirroring normal-path semantics exactly. Regression test: `TestSelfLoop_ResumeRespectsMaxIterationsCap`.
- **`maxIterations` semantics documented.** Clarified in `docs/flows.md` and skill spec that the cap counts in-process iterations only (postpone-self is not bounded by it), and that the cap is a post-step check (with `maxIterations: N`, exactly N agent calls happen).

### CLI output semantics for self-loops

The user asked how a multi-iteration step appears in `runFlowNonInteractive`'s JSON envelope. The dedup-by-step-ID logic at `cmd/flow.go:205-216` predates this change and naturally lines up with the spec's "one row per step" design:

- One entry per step ID. The latest event for that step ID wins.
- `iteration` reflects how many times the step ran before reaching the terminal state.
- `status` is the terminal status (`completed`, `failed`, `postponed`).
- `output` is from the last iteration only.
- `cost` and `context_size` are session-level totals, which aggregate naturally because all iterations share one session.

Documented in code (cmd/flow.go) and in `docs/flows.md` Output section. Intermediate iteration outputs are not surfaced via the envelope; to inspect them, read the step's session messages or the per-iteration Langfuse traces (distinguishable by the `#N` trace-name suffix).

## Problem

A flow step that routes back to itself without `postpone: true` is silently dropped on
the second iteration. The scheduler at `internal/flow/service.go:213-218` was
designed to prevent **diamond convergence** (`A → D, B → D` should run `D` once),
but its single-boolean `startedSteps` map can't distinguish a diamond from an
intentional self-loop. The carve-out for `!work.postpone` lets postpone-based
loops survive because the postponed branch only marks state and returns immediately;
the next invocation re-enters with a fresh `startedSteps` map.

Concretely, observed on session `1780307268-composer-developer-build-dependencies-build-current-level`:
the step completes iteration 1 with `has_more_levels=true`, the rule routes back
to itself with `postpone: false`, the scheduler hits `loaded=true && !postpone`,
calls `wg.Done()`, the `WaitGroup` drains, channels close — the flow ends
without an error and without progressing.

Even with the guard removed, three more problems prevent in-process loops from
working end-to-end:

1. **Same session and same `flow_states` row across iterations.** The session ID is
   deterministic in `step.ID`. Each iteration would overwrite the prior flow_state
   row, and `collectResumableSteps` couldn't tell which iteration to resume.
2. **No way for the agent to know which iteration it's on.** Without exposing the
   iteration counter to the template and the predicate evaluator, flow authors
   can't condition behaviour or rules on it.
3. **No safety cap.** A flow with a buggy termination predicate loops until the
   orchestrator job times out (default 45 min) and burns through tokens.

The session reuse is actually desirable (the agent gets memory of prior iterations,
session cost aggregates naturally, transcript compaction handles growth) — the
issues above are about making the loop legible, observable, and bounded.

## Goals

1. Allow a step to route back to itself in-process and execute repeatedly within a
   single flow invocation, while keeping the diamond-convergence guard for
   unrelated steps.
2. Track the iteration counter on `flow_states` so it survives postpone → resume
   and crash-recovery.
3. Expose `${step.iteration}` (1-based) to both the prompt template and the
   predicate evaluator so flow authors can render it into prompts and terminate
   loops via rules.
4. Add an optional `maxIterations` field on `Step` that, when set, fails the
   step (and takes its fallback) if a self-loop would exceed the cap. When
   unset, iterations are unbounded.
5. Keep the public-facing `FlowState` contract unchanged — one row per step.
   Stats aggregate at the `sessions` level (already true), so cost/tokens
   accumulate across iterations for free.

## Non-Goals

- New session per iteration. The session ID stays `<prefix>-<flow-id>-<step-id>`.
  Reusing it is what enables agent memory, cost aggregation, and resume.
- Per-iteration `flow_states` history. The single row keeps the latest
  iteration's snapshot; args accumulate across iterations (existing behaviour).
- Parallel self-loops. A step routing two simultaneous self-edges is undefined
  and stays undefined.

## Design

### 1. Narrow the diamond-convergence guard

`internal/flow/service.go:213-218`:

```go
isSelfLoop := work.prevStep != nil && work.prevStep.StepID == work.step.ID
if !isSelfLoop {
    if _, loaded := startedSteps.LoadOrStore(work.step.ID, true); loaded && !work.postpone {
        logging.Debug("Step already started, skipping (diamond convergence)", "step", work.step.ID)
        wg.Done()
        continue
    }
}
```

Self-loop work items bypass the `startedSteps` check. They arrive sequentially
by construction (only after the prior iteration's completion enqueues them),
so racing against themselves is not a concern.

### 2. Iteration counter on `flow_states`

Add a new column:

```sql
ALTER TABLE flow_states
  ADD COLUMN iteration INT NOT NULL DEFAULT 1;
```

Migration files:
- `internal/db/migrations/mysql/20260602120000_add_flow_states_iteration.sql`
- `internal/db/migrations/sqlite/20260602120000_add_flow_states_iteration.sql`

Updates to the canonical schema in `internal/db/schema/mysql.sql` and the
sqlite equivalent.

Semantics: the column stores the **currently-running or most-recently-running
iteration** of that step. Initialised to `1` on first `CreateFlowState`.
Incremented to `N+1` *before* the (N+1)th iteration's `UpdateFlowState`. This
gives clean resume behaviour:

- Iteration N postponed → row says `iteration=N, status=postponed`. Resume
  picks up at iteration N (the same iteration that postponed itself, which now
  re-evaluates after external state changed).
- Iteration N crashed mid-flight → row says `iteration=N, status=running`. Resume
  picks up at iteration N.
- Iteration N completed and routed to self → outgoing `stepWork` carries
  `iteration=N+1`; row is updated to `iteration=N+1, status=running` when the
  next iteration enters `runStep`.

### 3. `sqlc` query and Go type changes

- `internal/db/sql/mysql/flow_states.sql` and `internal/db/sql/flow_states.sql`:
  - `CreateFlowState`: accept `iteration` (default 1 on caller side).
  - `UpdateFlowState`: accept `iteration` so we can bump it.
- Regenerate `internal/db/flow_states.sql.go` and friends via `make generate`.
- `flow.FlowState` struct (`internal/flow/service.go:38-49`) gains
  `Iteration int`. `dbFlowStateToFlowState` and the SSE/JSON encoders pick it up.
- `flow.stepWork` struct (`internal/flow/service.go:84-89`) gains
  `iteration int`. Default 1 in the initial-work constructors; carried through
  the channel; written into the persisted row in `runStep`.

**All persistence sites must pass iteration**, not only the one inside `runStep`.
The early `CreateFlowState`/`UpdateFlowState` block in `Run()` (`service.go:175-203`,
which seeds rows for initial work before any step has actually run) writes
`iteration=1` for new rows and re-uses `existing.Iteration` for resumed rows.
The bump-on-self-route happens inside `runStep`'s update block — see §4.

### 4. `Step.MaxIterations` field

`internal/flow/flow.go`:

```go
type Step struct {
    ID             string      `yaml:"id"`
    Agent          string      `yaml:"agent,omitempty"`
    Session        StepSession `yaml:"session,omitempty"`
    Prompt         string      `yaml:"prompt"`
    Output         *StepOutput `yaml:"output,omitempty"`
    Rules          []Rule      `yaml:"rules,omitempty"`
    Fallback       *Fallback   `yaml:"fallback,omitempty"`
    MaxIterations  int         `yaml:"maxIterations,omitempty"`
}
```

`0` (the YAML default) means unbounded — capped only by flow timeout.

Enforcement point: in `runStep`, **after** the agent run succeeds but **before**
the success path persists `completed` and publishes the `completedState` event.
Resolving the next steps first lets us detect a max-iter-tripping self-loop and
take the failure path instead — keeping the event stream clean (one terminal
status per session per execution, no `completed → failed` flip).

Pseudocode for the patch around `service.go:479-512` (replacing the linear
"persist completed → publish → resolve → enqueue" block):

```go
nextResolved := resolveNextSteps(step.Rules, f.Spec.Steps, args, stepVars)

// Pre-check: if any matched rule self-loops past the cap, fail the step.
for _, rs := range nextResolved {
    isSelfLoop := rs.step.ID == step.ID && !rs.postpone
    if isSelfLoop && step.MaxIterations > 0 && iteration+1 > step.MaxIterations {
        lastErr = fmt.Errorf("step %q exceeded maxIterations (%d)", step.ID, step.MaxIterations)
        s.handleStepError(ctx, step, sessionID, rootSessionID, f.ID, args, lastErr,
            wg, agentEvents, flowStates, nextSteps, f)
        return
    }
}

// Persist completed + publish (unchanged) ...

// Enqueue successors with bumped iteration on self-route.
for _, rs := range nextResolved {
    newIter := iteration
    if rs.step.ID == step.ID && !rs.postpone {
        newIter = iteration + 1
    }
    wg.Add(1)
    nextSteps <- stepWork{
        step:      rs.step,
        args:      copyArgs(args),
        prevStep:  completedState,
        postpone:  rs.postpone,
        iteration: newIter,
    }
}
```

**Parallel-branch semantic on cap exhaustion**: if a step resolves to
`[self-loop, sibling-step]` and the self-loop trips the cap, the entire step
fails — *no* siblings are scheduled. Rationale: the loop's failure invalidates
the assumption under which the sibling branches would have run; surfacing one
failure is clearer than running half the fan-out.

`handleStepError` handles "mark failed + take fallback + emit event" — we route
through it for consistency with all other step failure paths. The `flow_states`
row ends in `failed` status with the max-iter error message in `output`.

### 5. Step-scoped variables: `${step.iteration}`

Introduce a separate, non-persistent scope used only for prompt and predicate
substitution. It is **never merged into args** and never written to
`flow_states.args` — preventing leakage into downstream steps.

#### Predicate evaluator

`predicateRegex` at `internal/flow/service.go:700` widens to:

```go
var predicateRegex = regexp.MustCompile(
    `^(?:(sizeof)\s+)?\$\{(args|step)\.([^}]+)\}\s*(==|!=|=~)\s*(.+)$`,
)
```

**Match-group indices shift** — today's parser reads `[1]=sizeof, [2]=key,
[3]=op, [4]=expected`. After the widening it becomes `[1]=sizeof, [2]=scope,
[3]=key, [4]=op, [5]=expected`. The `evaluatePredicate` body and any tests that
slice `matches` need to be updated together.

`evaluatePredicate` gains a scope dispatch. For `step` scope, lookups read
from a `stepVars` map (`{"iteration": iterationInt}`) passed alongside args.

**Unknown `${step.X}` keys error rather than silently match false.** Today
unknown `${args.X}` returns `(false, nil)` because the agent may legitimately
omit an optional field. For the `step.` scope the namespace is closed —
only the variables we explicitly expose are valid. An unknown `step.` key
is a flow-author bug (typo, or a variable that doesn't exist yet) and must
return `(false, fmt.Errorf("unknown step variable %q", key))` so the flow
fails loudly instead of silently routing the wrong way.

This adds rule forms like:

```yaml
rules:
  - if: ${step.iteration} == 10
    then: failed
  - if: ${step.iteration} != 1
    then: collect-prior-results
```

#### Template substitution

`substituteArgs` becomes `substituteScoped(template, args, stepVars)`. Two-pass:

1. Replace `${step.<key>}` with values from `stepVars`.
2. Replace `${args}` JSON dump and `${args.<key>}` from `args` (unchanged
   behaviour for those).

Prompt example:

```yaml
prompt: |
  Iteration ${step.iteration} of step ${args.flow_step_label}.
  Current state: ${args.snapshot_versions}
```

Pass site: in `runStep`, after resolving args, build `stepVars := map[string]any{"iteration": iteration}` and call `substituteScoped(step.Prompt, args, stepVars)`.

### 6. Resume path

`collectResumableSteps` (`service.go:574-626`) loads existing rows. The
returned `stepWork` already carries args; extend it to carry iteration from
the row. Use `existing.Iteration` for both postponed and non-completed rows.

For diamond resume (already-completed step revisited), iteration carries from
`existing.Iteration` so a self-loop resumed mid-loop continues at the right
counter.

### 7. Telemetry

- Langfuse trace metadata gains `step.iteration` when injecting flow context
  (`service.go:354-356`):
  ```go
  ctx = context.WithValue(ctx, tools.FlowStepIterationContextKey, iteration)
  ```
  Reader side in `internal/llm/tools` / Langfuse hooks adds the value to
  observation metadata.
- TUI flow progress: keep one row per step, but show iteration count in the
  status line when `iteration > 1` (e.g. `build-current-level · iter 3`).
  Component is `internal/tui/components/flow/` — small render change.

### 8. Backward compatibility

- Existing flows without `maxIterations` and without self-loops behave
  identically.
- Existing flows with `postpone: true` self-loops behave identically — the
  self-loop bypass also covers the postponed-path; predicate scope addition
  is additive.
- Existing `${args.X}` templating and predicates are untouched by the regex
  widening (the `(args|step)` group still matches `args`).
- The new `iteration` column defaults to `1` so existing rows backfill cleanly
  and existing readers that ignore the column still work.

## Code Touch Points

| File | Change |
|---|---|
| `internal/db/migrations/mysql/20260602120000_add_flow_states_iteration.sql` | New — adds `iteration` column |
| `internal/db/migrations/sqlite/20260602120000_add_flow_states_iteration.sql` | New — same for sqlite |
| `internal/db/schema/mysql.sql` | Add `iteration INT NOT NULL DEFAULT 1` to canonical schema |
| `internal/db/sql/mysql/flow_states.sql` | Add iteration param to `CreateFlowState`, `UpdateFlowState` |
| `internal/db/sql/flow_states.sql` | Sqlite mirror of the above |
| `internal/db/flow_states.sql.go` (generated) | Regenerate via sqlc |
| `internal/flow/flow.go` | Add `MaxIterations int` to `Step` |
| `internal/flow/service.go` | Narrow diamond guard; iteration in `stepWork` and `FlowState`; `${step.*}` scope in regex + substitution; maxIterations enforcement; resume path carries iteration |
| `internal/flow/service_test.go` | New tests (see below) |
| `internal/tui/components/flow/` | Render iteration count in step status line |
| `docs/flows.md` | Document in-process self-loops, `${step.iteration}`, `maxIterations` |
| `.agents/skills/flow-creator/SKILL.md` | New — flow-authoring skill for this spec |
| `.agents/skills/flow-creator/references/flow-spec.md` | New — flow YAML reference |

## Tests

`internal/flow/service_test.go` additions:

1. `TestSelfLoop_InProcess` — step routes to itself N times; `args.counter`
   increments each iteration; verify N completions on one invocation, single
   `flow_states` row with `iteration=N`, single session with messages from all
   iterations.
2. `TestSelfLoop_DiamondGuardPreserved` — two parallel branches both route to
   the same downstream non-self step; verify it runs once.
3. `TestSelfLoop_MaxIterationsCap` — step with `maxIterations: 3` and a rule
   always self-routing; verify the step fails with the max-iter error after 3
   iterations and the fallback step runs.
4. `TestStepIteration_TemplateSubstitution` — prompt contains `${step.iteration}`;
   verify it expands to the correct integer per iteration.
5. `TestStepIteration_PredicateRule` — rule `if: ${step.iteration} == 3 then: done`;
   verify routing fires only on iteration 3.
6. `TestSelfLoop_PostponeResume` — iteration 2 postpones; second invocation
   resumes at iteration 2 (`step.iteration == 2`), runs, routes to self →
   iteration 3 starts.
7. `TestStepIteration_NotInArgs` — verify `${step.iteration}` is **not**
   accessible as `${args.iteration}` and is not persisted to `flow_states.args`.

## Documentation Plan

Update `docs/flows.md`:

- Add a "Self-Loops" section after "Postponed steps". Cover in-process vs
  postpone semantics, when to use which, the session-reuse implication (agent
  has memory across iterations; cost aggregates), and `maxIterations` as a
  safety cap.
- Amend the existing "Diamond Convergence" note: clarify that the
  "first-arrival-wins" rule applies to non-self routes only — self-loops are
  exempt and re-enter the step intentionally (link to the new Self-Loops
  section).
- Add `${step.iteration}` to the template-substitution and predicate sections.
- Add `maxIterations` to the step-fields table.
- Note that args accumulation can be a footgun for loops (fields omitted from
  one iteration's output persist from prior iteration).

## Skill Update Plan

Update `.agents/skills/flow-creator/`:

1. `references/flow-spec.md`:
   - Add `maxIterations` to Step fields.
   - Document `${step.iteration}` in Template Substitution and predicate
     operators.
   - Add a "Self-loops (in-process)" subsection with a worked example
     mirroring the build-dependencies pattern.
   - Update "Diamond Convergence" note to mention the self-loop carve-out.
2. `SKILL.md`:
   - Mention `maxTurns` (per-agent config, already exists but not yet in the
     skill) alongside `maxIterations` (per-step) in design guidelines.
   - Update the "Self-loop with Postpone" pattern to also show the
     in-process variant.

## Implementation Steps

1. Schema migration (mysql + sqlite) + schema.sql update.
2. sqlc query update + regenerate.
3. `flow.go`: add `MaxIterations`.
4. `service.go`: stepWork iteration field + FlowState iteration field;
   diamond guard narrowing; stepVars substitution; predicate regex widening;
   maxIterations enforcement; resume path.
5. Persistence updates: `CreateFlowState`/`UpdateFlowState` callers pass
   iteration; bump iteration on self-route enqueue.
6. Tests — all 7 cases above.
7. TUI iteration indicator.
8. Telemetry: iteration in Langfuse metadata.
9. Docs update (`docs/flows.md`).
10. Skill update (`.agents/skills/flow-creator/`).
11. Run `go test ./internal/flow/...` and `make test`.

## Resolved Decisions (from review)

- **Max-iter check is hoisted before publishing `completedState`** — one
  terminal status per session, no `completed → failed` flip in the event stream.
- **Parallel-branch siblings are not scheduled when the cap trips** — failing
  the loop is treated as failing the entire step's fan-out.
- **Unknown `${step.X}` errors loudly** — closed namespace; typos must not
  silently misroute.
- **YAML field is `maxIterations`** — camelCase to match the existing `maxTurns`
  per-agent precedent in `.opencode.json`.

## Open Questions

- Should `${step.iteration}` be available in **all** steps as `1` even when
  the step has no self-loop? Leaning yes — uniform semantics; cheap.
- Should we also expose `${step.id}` and `${step.session_id}`? Out of scope
  here, but the scope mechanism makes them trivial follow-ups.
- TUI render of iteration count — bottom-line status update vs. inline in the
  step title. Defer to TUI taste during implementation.
