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
