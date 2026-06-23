# Design — flow-resume-semantics

## Context

Two distinct use cases collide on a single code path in `internal/flow/service.go::Run`:

1. **Crash recovery / postpone wake** — an opencode pod died mid-step, or a step parked itself with `postpone: true`, or an interactive step is `waiting_for_input` from a reviewer. A re-invocation of `Run` for the same `session.prefix` must continue from where the prior process left off; redoing earlier completed steps would replay side-effects (re-create MRs, re-post Jira comments) that already happened.
2. **External-event re-trigger** — a flow is keyed by an external identity (Jira issue key, PR number, Slack thread) and re-fires when that external entity changes. A new Jira comment arrives, the orchestrator launches `Run` for the same `session.prefix`; the flow must read the world afresh, re-route from step 0 (e.g. status-based `pick-workflow`), and produce new work informed by the new event. The accumulated per-step LLM messages should remain visible so the agent has cumulative context.

Pre-`9b6c590`, only the second use case was supported. Post-`9b6c590`, only the first is — and the second silently no-ops. Production evidence (job `dee2005a7c20860b`): three re-triggers of CD-4676 after the original 34-minute run each completed in ~107 seconds without invoking a single LLM call. Every step's `flow_states.status` was already `completed`, so `collectResumableSteps` skipped them all and the flow exited.

The fix must support both use cases on the same code path, keyed on a property of the existing state — not on a new caller-supplied flag (the orchestrator can't reliably know whether a given re-trigger is a crash recovery or a fresh external event — and even if it could, push that decision to the flow runtime where the state already lives).

## D1: pending-work detection drives the resume gate

The discriminating property is **whether any work is still owed to the prior run**. A flow whose most recent rows are all terminal AND whose completed rows' rules have nothing left to schedule has nothing to recover; otherwise resume.

**Decision** — `Run` enters `collectResumableSteps` iff `hasResumableWork(states, flow, resumeOnFailure)` returns true. Otherwise it constructs a single fresh `stepWork{step: f.Spec.Steps[0], ...}` exactly as the pre-`9b6c590` code did.

`hasResumableWork` folds two concerns into one predicate:

**(a) status-driven** — any row in an in-flight status short-circuits:

- `running` — pod crashed mid-step. Always resumable.
- `postponed` — step parked itself awaiting external event/timer. Always resumable; the re-trigger is the wake signal.
- `waiting_for_input` — interactive step parked awaiting reviewer reply. Always resumable; the inbound reply or the re-trigger itself is the wake signal.
- `failed` — *conditionally* resumable, controlled by the flow YAML's `session.resume_on_failure` (see D2).

**(b) rule-walk-driven** — for completed rows, evaluate the step's routing rules against the row's persisted args+output+iteration and check whether any rule produces a pending target:

- A self-route from a completed row means the next iteration was scheduled in-memory but the prior process died before writing it. The crash window between writing "iter N completed" and "iter N+1 running" is narrow but real, and `TestSelfLoop_ResumeAfterCompletedIterationCrash` codifies the expected recovery behavior.
- A forward route to a step whose flow_states row is absent (= step never ran) or non-terminal means the downstream step hasn't been scheduled yet.
- A forward route to a step that's already terminal (completed; or failed under default `resume_on_failure: false`) is NOT pending — the DAG fully traversed that branch already.

A completed row whose rules don't match (predicate flipped false; e.g. a self-loop that hit its terminating iteration count) and rows with no rules at all yield no targets — not pending.

The predicate is pure: it deserializes `row.Args` and `row.Output` locally to reconstruct the args-at-end-of-step that `runStep` would have used when evaluating the rules in-process. It calls `findStep`/`resolveNextSteps` — the same helpers `collectResumableSteps` itself uses — so the two functions agree on what "the next steps from this row" means and the predicate never lies to its caller.

Pseudocode:

```go
func hasResumableWork(states []db.FlowState, f *Flow, resumeOnFailure bool) bool {
    terminal := make(map[string]bool)
    rowByStep := make(map[string]db.FlowState)
    for _, st := range states {
        switch st.Status {
        case running, postponed, waiting_for_input:
            return true
        case failed:
            if resumeOnFailure { return true }
            terminal[st.StepID] = true
        case completed:
            terminal[st.StepID] = true
            rowByStep[st.StepID] = st
        }
    }
    for stepID, row := range rowByStep {
        for _, ns := range evaluateRowNextSteps(row, stepDef, allSteps) {
            if ns.step.ID == stepID { return true } // self-route pending
            if !terminal[ns.step.ID] { return true } // forward target pending
        }
    }
    return false
}
```

The existing `hasRunning` early-return at `service.go:169-187` (the "running states only" replay-and-exit fast path) is preserved as-is. Its purpose is different — it's the path where another instance of `Run` is already executing this flow and we just want to fan the existing state rows out to `flowStates` without creating new work. That stays.

**Rejected alternative — purely status-based gate.** The first draft of this change checked only the in-flight statuses (running / postponed / waiting_for_input / failed-with-resume). That broke the two `TestSelfLoop_Resume*` tests because a self-loop that crashed between writing iter N's completed row and iter N+1's running row has only a single `completed` row, and the recovery requires walking that row's rules to discover the pending next iteration. Folding the rule-walk into the gate restores crash-recovery for self-loops without leaking the regression that motivated this change (forward DAGs with all rows completed must restart, not short-circuit).

## D2: `resume_on_failure` is per-flow, not per-call

A flow author knows whether their pipeline is **retry-from-failure-safe**. A long, expensive multi-step build pipeline whose step 3 failed because of a transient network blip should resume from step 3, not redo steps 1–2. A "react on Jira comment" flow where step 3 failed should restart from step 1, because the new comment may want a different workflow path entirely.

**Decision** — Add `ResumeOnFailure bool` to `FlowSession` (the `flow.session:` YAML block), default `false`. Read at `Run` entry, passed into `hasInProgressState`. No per-call override; the caller's `fresh=true` is still the universal escape hatch.

YAML:

```yaml
flow:
  session:
    prefix: ${args.build_id}
    resume_on_failure: true
  steps: [...]
```

**Why per-flow and not per-call:**

- The decision is a property of the flow's idempotency guarantees, not of the caller's intent. Mixing modes per-call would make behavior unpredictable for ops debugging a stuck flow.
- The orchestrator's existing `fresh_start` boolean already covers "I explicitly want a clean slate." Adding a second per-call knob (`resume_on_failure`) would force callers to reason about a 2×2 matrix; per-flow collapses it to the dimension that actually matters.
- Webhook and Slack triggers pass the same args every time. There's no place in the orchestrator's call path that naturally varies "do you want failure-resume this time."

## D3: postpone and waiting_for_input wake on re-trigger

`postpone` and `waiting_for_input` are explicit pause points; the only way for the flow to advance is for *something* to wake them. Re-trigger of the same flow is that wake — the same way an inbound message into a `waiting_for_input` step's session wakes it.

**Decision** — Re-trigger resumes the postponed/waiting step in place. This is the current behavior in `collectResumableSteps` (lines 791-797) and is preserved by D1's design — those statuses are in-progress, so `Run` goes through `collectResumableSteps`, which already handles them correctly.

**Rejected alternative** — "restart from step 0 even when postponed." Loses the postpone's intentional state (saved args, iteration counter). The flow author who wrote `postpone: true` was asking for resumption, not a do-over; if they wanted a do-over they wouldn't have postponed.

## D4: session preservation is the runtime invariant

`fresh=true` already deletes the entire session tree (`s.sessions.DeleteTree`). That stays. The change introduced here is that the **restart-from-step-0 path does NOT delete sessions** — it just routes work to step 0 with the existing `flow_states.status` rows in place; as each step runs, its row transitions back to `running` and is overwritten on completion.

**Decision** — The restart path goes through the same per-step session as before. Step 0's session already has prior messages from the previous run; the agent sees them as conversation history. This is the desired "preserved context" behavior.

**Edge case** — the flow_states row for step 0 transitions from `completed` → `running` → `completed` again. Concurrent `Run` invocations for the same prefix are guarded by the orchestrator's `FindJobByIssueAndFlow` (returns 200 with existing job ID, doesn't double-spawn) and by `hasRunning` inside opencode. No additional lock is needed.

## D5: the existing `fresh=true` path is unchanged

`fresh=true` continues to:

1. Delete `flow_states` rows for `rootSessionID`.
2. Delete the entire session tree via `s.sessions.DeleteTree`.
3. Set `existingStates = nil`.
4. Fall through to step 0.

This is the user's hard reset: clear everything, including LLM message history. It remains the right escape hatch for "session messages went off the rails, start truly clean." The new restart-on-retrigger behavior covers the soft case (clear flow_states-driven cache, keep messages); `fresh=true` covers the hard case (clear everything).

## D6: validation hardening for `flow.session`

Today `FlowSession{Prefix string}` is a struct with a single field; YAML keys other than `prefix` are silently dropped by yaml.v3's strict-or-loose default. After adding `resume_on_failure`, a typo (`resume_on_fail: true`) would silently be ignored, leaving the flow on default behavior with no warning.

**Decision** — Extend `validateFlow` to enumerate the allowed keys in `session:` and return `ErrInvalidYAML` with the offending key name if any unknown key is present. This is consistent with how `validateFlow` already rejects unknown step IDs in rule targets.

Implementation: parse `session:` into `map[string]any` during validation (separately from the typed `FlowSession`) and assert each key is in `{"prefix", "resume_on_failure"}`. If the registry's existing YAML decode path uses strict mode, this comes for free; if not, the check is a 10-line addition to `validateFlow`.

## D7: no protocol changes

The `POST /flow` request body keeps `{flowID, args, fresh}`. The `--flow-fresh` CLI flag is unchanged. SSE events are unchanged. The orchestrator's `Job.FreshStart` mapping to `AGENT_FLOW_FRESH_START` → `agent.sh --flow-fresh` is unchanged.

The only observable change at the API boundary is the wall-clock duration and LLM cost of re-triggers where the prior run was cleanly `completed`. Those re-triggers now do real work — which is the intent.

## Out of scope

- Per-step `resume_on_failure` overrides. Possible future extension but not justified by current use cases; the flow-level flag is sufficient.
- A separate `resume_on_completion` knob to opt into "skip completed steps on re-trigger." If a flow genuinely wants checkpoint-cache behavior (rare; the cited regression IS this behavior), it can encode that by including a salt in the prefix (`${args.jira_issue_id}-${args.run_token}`) so each run gets its own state.
- Surfacing the resume vs restart decision in SSE events. Operators can already infer it from the `flow.step.started` event for step 0 — if it fires, it was a restart; if the first event is for a later step, it was a resume.
- Migration of the existing CD-4676-style data. Existing `flow_states` rows in production are valid; the new code reads them with the new rules from the next re-trigger onwards. No backfill.
