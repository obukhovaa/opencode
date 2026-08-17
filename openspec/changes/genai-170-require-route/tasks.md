## 1. Step field

- [x] 1.1 Add `RequireRoute bool` to `Step` in `internal/flow/flow.go` with `yaml:"require_route,omitempty" json:"require_route,omitempty"`, documented as opt-in and defaulting to today's behaviour.
- [x] 1.2 Confirm no registry validation is needed — the field has no cross-field constraints and no interaction with `rules`, `fallback`, `maxIterations` or `postpone`. If `registry.go` validates unknown/required combinations per step, check it does not reject the new key.
- [x] 1.3 No code change needed: `inheritableStepKeys()` derives from the struct field index with a deny-list, so a new field is inheritable automatically. The two-part-rule coverage test DID fail until a sample was added (`include_test.go`), which is the guard working as designed — its message says to add a sample, not to curate an allow-list.

## 2. Runtime behaviour

- [x] 2.1 In `internal/flow/service.go`, at the `resolveNextSteps` call site in `runStep`, detect the empty-result case for a step that declares rules.
- [x] 2.2 Log a WARN naming the step and the rule count, unconditionally, before the step's terminal state is published. Match the phrasing style of the existing empty-work-set warning on the resume path.
- [x] 2.3 When `step.RequireRoute` is true, call `handleStepError` with an error naming the step and stating that no routing rule matched, so the declared `fallback.to` receives control (and terminal failure results when no fallback is declared).
- [x] 2.4 Verify placement against the existing control flow: it must not fire for a step that declares no rules, must not pre-empt the `maxIterations` check, and must not interfere with the postpone or transient-provider-error paths above it.

## 3. Tests

- [x] 3.1 `require_route: true` + rules all false + a declared fallback ⇒ the fallback step runs, the run does not report a plain success.
- [x] 3.2 `require_route: true` + rules all false + no fallback ⇒ run ends through the failure path.
- [x] 3.3 Field absent + rules all false ⇒ unchanged behaviour: no successor, run completes. This is the regression guard for the documented bounded-loop exit.
- [x] 3.4 A bounded self-loop guarded by `${step.iteration} != N`, without the field, still terminates normally at iteration N — asserted against the `flow-runtime-resume` scenario it is drawn from, so the documented pattern is pinned.
- [x] 3.5 A step declaring no rules at all does NOT log the zero-match warning.
- [x] 3.6 Covered by 3.1/3.2/3.3 — all three use an absent-key predicate (`${args.decision} == go` with `decision` never in args), which is the realistic trigger and exercises the same path.

## 4. Verification

- [x] 4.1 `go build ./...` clean; `gofmt`/`go vet` clean on touched packages.
- [x] 4.2 `go test ./internal/flow/ ./internal/api/ -count=1` → 467 passed. Includes the pre-existing self-loop and resume tests named in `flow-runtime-resume`.
- [x] 4.3 `openspec validate genai-170-require-route --strict` passes.
- [x] 4.4 Confirm no existing flow in this repo's test fixtures sets `require_route`, so the default path is what the whole suite exercises.

## 5. Hand-off, not implemented here

- [ ] 5.1 Adoption in the workspace repo: set `require_route: true` on `developer-react-on-jira-openspec`'s interactive steps (whose success rules gate on a required-no-default field) and on any step whose rules are meant to be exhaustive. Must land AFTER this change is deployed — the key is silently ignored by an older runtime, so early adoption looks fine and protects nothing.
- [ ] 5.2 Decide whether `c2-agent` should declare the field in its own flow structs. Nothing breaks without it (plain `yaml.Unmarshal`, unknown keys dropped), but its flow-authoring reference should document it.
