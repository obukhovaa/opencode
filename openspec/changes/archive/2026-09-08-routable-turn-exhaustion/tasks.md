## 1. Spec

- [x] 1.1 Add the `flow-turn-exhaustion-routing` capability spec.

## 2. Flow spec surface

- [x] 2.1 Add `Fallback.OnTurnsExhausted` (`on_turns_exhausted`) plus the
      `OnTurnsExhaustedAccept` / `OnTurnsExhaustedFail` constants and the
      `Step.FailsOnTurnsExhausted()` helper (`internal/flow/flow.go`).
- [x] 2.2 Reject unknown values at flow load time with
      `ErrInvalidOnTurnsExhausted` (`internal/flow/registry.go`).

## 3. Runtime

- [x] 3.1 Add `turnsExhaustedError`, `errTurnsExhausted`,
      `mergeStructOutputIntoArgs` and `turnsExhaustedOutcome`
      (`internal/flow/turns_exhausted.go`).
- [x] 3.2 Raise the outcome at the end of the attempt loop so it consumes
      `fallback.retry` and falls through to the failure path
      (`internal/flow/service.go`).
- [x] 3.3 Broaden the forced-wrap-up exemption from
      `structOutputTurnsExhausted` to `errTurnsExhausted`.
- [x] 3.4 Merge the wrap-up document into the args the `fallback.to` step
      inherits.

## 4. Tests

- [x] 4.1 No opt-in ⇒ an exhausted run still completes the step (compatibility).
- [x] 4.2 `retry: 1` ⇒ a second attempt on the same session rescues the step.
- [x] 4.3 Retries spent ⇒ step fails and routes to `fallback.to`.
- [x] 4.4 `retry: 0` ⇒ routes immediately, no extra attempt.
- [x] 4.5 The `fallback.to` step inherits the wrap-up document's fields.
- [x] 4.6 An exhausted run with no document keeps `missingStructOutputError`.
- [x] 4.7 Error message shapes; `errTurnsExhausted` over both shapes;
      `mergeStructOutputIntoArgs` edge cases; load-time validation;
      `FailsOnTurnsExhausted`; `turnsExhaustedOutcome`.
- [x] 4.8 Turn exhaustion is not classified as a transient provider error.

## 5. Docs

- [x] 5.1 `docs/flows.md` — fallback field table + "Turn-budget exhaustion".
- [x] 5.2 `.agents/skills/flow-creator/references/flow-spec.md` — same, sized
      for the skill.
- [x] 5.3 `.agents/skills/flow-creator/SKILL.md` — name the `maxTurns` trap in
      the Error Recovery section the authoring agent reads first.

## 6. Review follow-ups

- [x] 6.1 Prompt a post-exhaustion retry as a continuation
      (`turnsExhaustedRetryPrompt`) instead of re-sending the original prompt
      verbatim, which invited the agent to restate its wrap-up document and so
      complete the step with the same unfinished work.
- [x] 6.2 Hold the last `*turnsExhaustedError` outside the attempt loop: a
      later attempt's unrelated error (`ErrSessionBusy`, a step timeout) used
      to overwrite `lastErr` and take the cut-off run's report with it, and
      could let the forced wrap-up complete the step.
- [x] 6.3 Carry a prose-only cut-off run's closing text on the error, and stop
      claiming (in code comments, docs and design) that only the
      has-a-document shape is affected — a prose-only run and a schema-less
      step both reach this branch too.
- [x] 6.4 Veto the `resume_after` park whenever any attempt exhausted, so the
      extra attempts the opt-in buys cannot draw a transient error and park
      into a fresh workspace.
- [x] 6.5 Merge the wrap-up document into the fallback step's own args copy
      rather than the map already published on the failed `FlowState`.
- [x] 6.6 Publish the in-flight `flow.step.retrying` transition on an
      exhaustion retry (new reason string), so the claim in `proposal.md` is
      true and an orchestrator is not left watching dead air.
- [x] 6.7 Count exhausted attempts rather than the loop index in the error
      message; drop the unused `TurnsExhausted()` method whose comment
      described an extension point that did not work.
- [x] 6.8 Tests: de-vacuum the fallback-inheritance test (it passed with the
      production merge deleted — the stub replayed the exhausted event into
      the salvage step's own completion merge); record `sessionID`/`maxTurns`
      on the stub so the same-session / fresh-budget claim is actually
      asserted; add end-to-end coverage for the forced-wrap-up exemption, the
      no-park guard, the exhaustion-outranks-later-error rule and the
      prose-only shape. All new guards verified by mutation.
- [x] 6.9 Document the sharp edges: `fail` with no `to`, the run still
      reporting `flow.failed` when salvage succeeds, per-attempt `timeout`
      multiplication, and `extends` dropping the key when a step overrides
      `fallback`. Add the `maxTurns` trap to the flow-creator `SKILL.md`.
