## 1. FlowSession schema and validation

- [x] 1.1 Add `ResumeOnFailure bool \`yaml:"resume_on_failure,omitempty"\`` to `FlowSession` in `internal/flow/flow.go` (alongside the existing `Prefix` field). Default zero-value `false` matches the proposal's default-deny-failure-resume semantic.
- [x] 1.2 In `internal/flow/registry.go`, add a `validateFlowSessionKeys(data []byte)` helper called from `parseFlowFile` that parses the raw `session:` block into a `map[string]any` (separately from the typed decode) and rejects any key not in the package-level `knownSessionKeys` allow-list (`{"prefix", "resume_on_failure"}`). Returns `fmt.Errorf("%w: unknown key %q in flow.session", ErrInvalidYAML, key)`. Documented at the function level so adding a new `FlowSession` field reminds the author to extend the allow-list.
- [x] 1.3 Add sub-test `TestParseFlowFile/typo_in_session_block_is_rejected_with_ErrInvalidYAML` covering a YAML with `session: { resume_on_fail: true }` (typo) — must return `ErrInvalidYAML` AND name the unknown key.
- [x] 1.4 Add sub-test `TestParseFlowFile/session.resume_on_failure_is_accepted_and_round-trips` covering `session: { prefix: "${args.id}", resume_on_failure: true }` — must succeed and round-trip the field onto `FlowSpec.Session.ResumeOnFailure`.
- [x] 1.5 Add sub-test `TestParseFlowFile/session.resume_on_failure_defaults_to_false_when_omitted` and `TestParseFlowFile/no_session_block_is_accepted` to lock in zero-value defaults and the no-session-block-at-all case.

## 2. Extract `hasResumableWork` helper

- [x] 2.1 In `internal/flow/service.go`, add `func hasResumableWork(states []db.FlowState, f *Flow, resumeOnFailure bool) bool` per `design.md::D1`. Keep it package-private and pure (no I/O, no state mutation) so the matrix test in §4 can call it directly without setting up a service.
- [x] 2.2 Add a sibling helper `evaluateRowNextSteps(row, stepDef, allSteps) []resolvedStep` that reconstructs end-of-step args (`row.Args` merged with `row.Output` when `IsStructOutput`) and evaluates the step's rules — same shape `collectResumableSteps` would use.
- [x] 2.3 First pass on the status set MUST short-circuit on the first in-flight row (running/postponed/waiting_for_input, or failed when `resumeOnFailure`). Only if no status hit do we walk completed rows' rules.
- [x] 2.4 Place both helpers adjacent to `collectResumableSteps` so the relationship is visible at-a-glance.

## 3. Rewire `Run` to use the resumable-work gate

- [x] 3.1 In `internal/flow/service.go::Run`, around the existing `if len(existingStates) > 0 && !fresh { … collectResumableSteps … } else { … fresh stepWork … }` block (lines 193-205 at HEAD), change the `if` condition to `if !fresh && hasResumableWork(existingStates, f, f.Spec.Session.ResumeOnFailure)`. Keep the else branch exactly as it is (single-step initial work at step 0).
- [x] 3.2 Leave the `if fresh { delete flow_states + DeleteTree + existingStates=nil }` block at lines 156-167 untouched.
- [x] 3.3 Leave the `hasRunning` early-return at lines 169-187 untouched (it serves the cross-process replay path, not the re-trigger path).
- [x] 3.4 Verify `collectResumableSteps` itself does not need changes — when `Run` enters it now, the gate guarantees there's pending work, but rows for already-completed prior steps are still routed via the existing `Skipping completed step during resume` path so postpone-wake / waiting-for-input-wake continue to work as before.
- [x] 3.5 Add an `INFO`-level log line at the gate decision: when entering restart-from-step-0 path, log `"Restarting flow from step 0 (no in-progress state)"` with `flow`, `existing_steps`, `resume_on_failure`. When entering resume path, the existing `"Resuming flow from previous state"` log already covers it — add `resume_on_failure` to its keyvals.

## 4. Add matrix test `service_retrigger_test.go`

- [x] 4.1 Create `internal/flow/service_retrigger_test.go` next to `service_fresh_test.go`. Reuse the existing `stubQuerier`, `stubSessions`, and helper builders directly (all in `package flow`, no export needed).
- [x] 4.2 Add `TestRunRetrigger_Gating` table-driven over an 8-row matrix covering every status combination plus the `fresh=true` and no-prior-state paths. Each row asserts the chosen path via the agent's prompt order (`prompt-a` first → restart from step-a; `prompt-b` first AND no `prompt-a` → resume-skipped-step-a; empty prompts → `hasRunning` early-return; `deletedTreeIDs` non-empty → fresh path).
- [x] 4.3 Add `TestRunRetrigger_PreservesSessionsOnRestart` — assert no `Delete`/`DeleteTree`/`DeleteFlowStatesByRootSession` calls under restart (`fresh=false` with all-completed prior states), AND that the restart actually re-runs both steps from step 0. Locks in the D4 session-preservation invariant.
- [x] 4.4 Add `TestRunRetrigger_WakesPostponedStep` — seed a postponed row at step-b with iteration=3, assert the agent runs once with prompt-b (skipping step-a's cached row), AND the persisted iteration on the wake is 3 (not reset to 1). Covers the D3 wake contract.
- [x] 4.5 Add `TestHasResumableWork` covering the helper directly — status short-circuits (running/postponed/waiting/failed×opt-in), linear DAG with all rows completed (not resumable), linear DAG with downstream row missing (resumable), self-loop with mid-iteration completed row (resumable via self-route).
- [x] 4.6 Add `TestHasResumableWork_SelfLoopTerminatedByRule` — explicit regression for the predicate's rule-evaluation branch: a self-loop step whose predicate flipped false at the terminating iteration (e.g. `${step.iteration} != 3` at iter=3) MUST NOT be resumable.
- [x] 4.7 Run `go test ./internal/flow/... -race -count=1` — all pass after the helper refinement that fixes `TestSelfLoop_ResumeRespectsMaxIterationsCap` and `TestSelfLoop_ResumeAfterCompletedIterationCrash`. Run `go test ./... -count=1` across the whole opencode repo — clean.

## 5. Audit for hidden assumptions

- [x] 5.1 grep `internal/flow/` for any code path that assumes "if existingStates non-empty AND not fresh, the flow is resuming" — should be just the `Run` gate touched in §3. Confirm no other helpers branch on this. **Confirmed**: all `existingStates` references live inside `service.go::Run` (lines 148–251); no helper outside `Run` branches on the assumption.
- [x] 5.2 grep `internal/api/` and `cmd/` for any caller of `Run` that synthesises `fresh` from the existence of prior state. None expected; confirm. **Confirmed**: `cmd/flow.go::runFlowNonInteractive` takes `fresh` as a CLI parameter (line 114); `internal/api/handler_flow.go` reads `Fresh bool` from the JSON request body (line 231) and threads it through `Start`→`run`→`svc.Run` unchanged. No synthesis.
- [x] 5.3 Inspect `internal/flow/service.go::runStep` and `handleStepError` — these write `flow_states.status = failed` on error. With `resume_on_failure: false` (default), a subsequent re-trigger restarts; with `true`, it resumes from the failed step. No code change needed in these paths, but confirm by reading. **Confirmed**: `handleStepError` (line 738) writes the `failed` row with args persisted; `hasResumableWork` treats it as terminal under default, and `collectResumableSteps` routes it via the "non-completed status" branch when `resume_on_failure: true`.
- [x] 5.4 Inspect `internal/flow/service.go::resolveSession` — the step session lookup MUST find the existing session on restart (it's keyed by `sessionID`). Confirm by reading; no change expected since `fresh=false` does not delete sessions. **Confirmed**: `resolveSession` (line 1072) calls `s.sessions.Get(ctx, sessionID)` first; with `fresh=false` the session is not deleted, so `Get` succeeds and returns the existing session with cumulative LLM history intact.

## 6. Documentation

- [x] 6.1 Update `docs/flows.md` `session:` section: document `prefix` (existing) and `resume_on_failure` (new) with the matrix from `design.md::D1`. Add a short note: "A re-trigger of a cleanly-terminated flow restarts from step 0 by default. Per-step sessions are preserved so the agent retains prior conversation context." **Done**: `### Re-trigger semantics` section now spells out the status-driven and rule-walk-driven branches matching D1.
- [x] 6.2 Add an example flow YAML snippet showing `resume_on_failure: true` for a build-pipeline use case. **Done**: build-pipeline example with `download-artifacts → build → publish` lives in `#### resume_on_failure` subsection.
- [x] 6.3 Cross-link from `docs/flows.md` to the new capability spec `openspec/specs/flow-runtime-resume/spec.md` (added after archive in §8). **Done**: blockquote with relative link at the end of `### Re-trigger semantics`. The path resolves to the post-archive location; until archive lands the link is dangling (verified in §8.2).

## 7. Manual verification against the cited regression

- [x] 7.1 Build opencode against this branch. **Done**: `go build ./...` clean. `make test` green (all unit + race-detector tests pass under `go test ./internal/flow/... -race -count=1`).
- [ ] 7.2 Spin up a local opencode + MySQL with a minimal flow that has `session.prefix: ${args.id}` and three trivial steps.
- [ ] 7.3 `POST /flow {id: "x", fresh: false}`. Confirm all three steps run and `flow_states` rows are `completed`.
- [ ] 7.4 `POST /flow {id: "x", fresh: false}` again. **MUST** re-run all three steps from step 0; wall-clock approximately equal to first run; per-step session message count strictly greater than after the first run (cumulative LLM history preserved).
- [ ] 7.5 `POST /flow {id: "x", fresh: true}`. MUST delete sessions and re-run; per-step session message count equals first-run count (history wiped).
- [ ] 7.6 Repeat §7.4 with a flow whose step 2 always fails. With default `resume_on_failure: false`, the re-trigger MUST restart from step 0. With `resume_on_failure: true` in the YAML, the re-trigger MUST start at step 2.

> §7.2–§7.6 require a live opencode server + MySQL and real-world flow YAML. Cannot be executed from this session — left for the change author to run before merge. The integration coverage in `service_retrigger_test.go` (8-row matrix + `TestRunRetrigger_GatePlannerMismatchFallsBackToRestart` + `TestRunRetrigger_PreservesSessionsOnRestart` + `TestRunRetrigger_WakesPostponedStep`) exercises the same predicates and session-preservation contract in-process; the manual run is the final confidence pass against the cited CD-4676 regression.

## 8. Archive

- [ ] 8.1 After merge, run `openspec archive flow-resume-semantics` (or equivalent script) to move `openspec/changes/flow-resume-semantics/specs/flow-runtime-resume/` to `openspec/specs/flow-runtime-resume/`.
- [ ] 8.2 Verify the cross-link added in §6.3 still resolves after archive.
