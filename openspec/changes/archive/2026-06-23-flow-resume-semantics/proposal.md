## Why

A stable flow `session.prefix` (e.g. `${args.jira_issue_id}`) is intended to mean **"every re-trigger for the same external key re-runs the flow from step 0 against the current world, with per-step session messages preserved as cumulative LLM context."** That was the runtime behavior prior to commit `9b6c590` (`feat(tool_and_flow):improve woding, fix flow concurrency race`, Feb 23 2026), which introduced `collectResumableSteps` and changed the default to "skip every step whose prior `flow_states` row is `completed`."

The regression silently breaks "react on Jira comment / Slack message / webhook" flows: a re-trigger of an already-completed run finds all 7 step rows still `completed`, short-circuits every step, exits in ~100 seconds without any LLM call, and the new external event (comment text, status change) is never read. Production evidence: job `dee2005a7c20860b` (CD-4676, developer-react-on-jira) — orchestrator status `completed`, duration 1m47s, zero new MRs, despite a fresh Jira comment requesting follow-up work; flow_states timestamps belong to a run that ended 3 hours earlier.

The existing escape hatch `fresh=true` is too coarse — it also `DeleteTree`s every per-step session, which destroys the LLM message history the stable prefix is supposed to preserve. There is currently no mode that says "restart the flow but keep step sessions intact."

This change restores the pre-`9b6c590` semantics for cleanly-terminated prior runs while preserving the genuine crash-recovery and explicit-pause use cases that `collectResumableSteps` was added to serve.

## What Changes

- **Flow runtime resume gate**: at `internal/flow/service.go::Run`, replace the current `len(existingStates) > 0 && !fresh` condition for entering `collectResumableSteps` with an explicit **resumable-work** check (`hasResumableWork`). The predicate is true when EITHER (a) at least one prior `flow_states` row has `status ∈ {running, postponed, waiting_for_input}` (also `failed` when `resume_on_failure` is true), OR (b) a `completed` row's routing rules — evaluated against the row's persisted args+output+iteration — still produce a pending target (self-route, or forward target whose step is absent / non-terminal). Otherwise the flow starts at step 0 from a fresh `stepWork`. The rule-walk branch preserves crash-recovery for self-loops (iter N completed, iter N+1 was scheduled in-memory but the process died before its running write) — without it, `TestSelfLoop_ResumeRespectsMaxIterationsCap` and `TestSelfLoop_ResumeAfterCompletedIterationCrash` regress to spinning the loop from iter 1 on every retrigger. Per-step sessions are NOT deleted in either path — that remains exclusive to `fresh=true`.
- **`resume_on_failure` flag on `FlowSession`**: optional `resume_on_failure: bool` field (default `false`) in the flow YAML's `flow.session:` block. When true, `failed` rows join the in-progress set, so a re-trigger continues from the failed step (current behavior of `collectResumableSteps`, which already re-runs steps whose status is not `completed`/`postponed`). When false (default), `failed` is treated as terminal and the re-trigger restarts from step 0. The flag is read-only at runtime — it does not affect how a step transitions into `failed`, only how a subsequent re-trigger interprets that row.
- **Postpone / waiting_for_input wake semantics**: unchanged from current behavior. A re-trigger of a flow whose latest step is `postponed` or `waiting_for_input` resumes that step in place — the re-trigger is the wake signal. Documented explicitly so the contract is no longer implicit.
- **Validation**: extend `validateFlow` to accept `resume_on_failure` and reject unknown keys inside `flow.session:` (today the YAML parser silently ignores typos in this block).
- **No DB migration**. `flow_states` schema, `sessions` schema, and the `fresh` HTTP/CLI flag are unchanged. The change is pure runtime semantics in the flow service plus a new YAML field on `FlowSession`.

## Capabilities

### New Capabilities

- `flow-runtime-resume`: defines when `flow.Service.Run` resumes a prior `flow_states` set vs restarts at step 0. Specifies the in-progress status set (`running`, `postponed`, `waiting_for_input`), the terminal status set (`completed`, plus `failed` when `resume_on_failure` is false), the per-step session preservation invariant ("sessions are deleted only under `fresh=true`; restart-from-step-0 keeps them so prior LLM turns remain visible"), and the `FlowSession.resume_on_failure` opt-in for retry-from-failure pipelines. Includes the wake-on-retrigger contract for `postponed` and `waiting_for_input`.

### Modified Capabilities

(None. The `flow-api` spec governs the HTTP surface (`POST /flow`, `GET /flow/status`, SSE events) and is unaffected — the `fresh` request field, status payloads, and event shapes do not change. Resume semantics live below that API in the flow runtime and have no existing spec coverage.)

## Impact

**Code**
- Modified: `internal/flow/service.go` — `Run` body around the existing `collectResumableSteps` entry condition; helper `hasInProgressState(states, resumeOnFailure)` extracted for testability.
- Modified: `internal/flow/flow.go` — `FlowSession` gains `ResumeOnFailure bool \`yaml:"resume_on_failure,omitempty"\``.
- Modified: `internal/flow/registry.go` — `validateFlow` permits the new key and rejects unknown keys inside `session:`.
- New: `internal/flow/service_retrigger_test.go` — table-driven tests over the (existing-state set × resume_on_failure × fresh) matrix; covers every status combination plus the postpone-wake and waiting_for_input-wake scenarios; asserts both the chosen entry path (restart vs resume) and the session-preservation invariant (sessions NOT deleted on restart).
- Existing tests in `service_fresh_test.go` and `service_loop_test.go` continue to pass without modification — `fresh=true` behavior is unchanged, and crash-recovery via `running` rows still resumes.

**APIs**
- No HTTP API change. `POST /flow {fresh: bool, …}` keeps current semantics.
- No CLI flag change. `--flow-fresh` keeps current semantics.

**Schema**
- No migration. `flow_states.status` enum and `sessions` table unchanged.

**Operator-visible behavior changes**
- Re-triggers of cleanly-terminated flows (every prior step `completed`) now re-execute all steps from step 0 instead of returning in seconds with cached output. Wall-clock for such re-triggers rises to roughly the original first-run duration; LLM cost rises accordingly. Flows that depend on the old "second trigger is free" behavior must either set `resume_on_failure: true` (only useful when prior run terminated in failure — does NOT cache `completed` outputs) or be re-keyed with a more unique `session.prefix` so a true re-run gets its own state.
- Re-triggers where the most recent terminal row is `failed` now restart by default. Pipelines that want the old "retry from failed step" must opt in via `resume_on_failure: true` in their flow YAML.
