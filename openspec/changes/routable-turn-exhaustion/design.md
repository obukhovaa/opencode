## Context

Three facts about the existing code determine the shape of this change.

**D1 — the signal already exists.** `AgentEvent.TurnsExhausted`
(`internal/llm/agent/agent.go`) is set by both budget gates: the inner-loop
max-turns wrap-up and the outer-cycle (background-task re-entry) cap. No
agent-runtime change is needed; the gap is entirely in `internal/flow`.

**D2 — retrying is nearly free and lands in the right place.** The attempt loop
in `runStep` calls `agentSvc.RunWith(runCtx, sess.ID, prompt, step.MaxTurns, …)`
with the same `sess.ID` on every attempt. A retry therefore appends to the
existing session — full message history, same pod, same working tree — and
`step.MaxTurns` is applied per call, so each attempt gets a fresh budget. This
is why "just retry" is the load-bearing fix rather than a workaround: the agent
resumes mid-task instead of starting over.

**D3 — only one shape is currently silent.** An exhausted run that produced no
usable document already becomes a `missingStructOutputError` and already
consumes `fallback.retry`. The shape that completes silently is
*exhausted + usable `struct_output`* — the one the forced wrap-up produces.

## Goals / Non-Goals

- **Goal:** let a step declare that being cut off is not success.
- **Goal:** reuse `fallback.retry` / `fallback.to` verbatim — no second retry
  mechanism, no new terminal status.
- **Non-goal:** changing behaviour for steps that do not opt in.

## Decisions

### D4 — Opt-in, not a new default

`on_turns_exhausted` defaults to `accept`. Turn exhaustion is a legitimate
terminal state for an analysis step, where the wrap-up summary genuinely is the
result; flipping the default would convert such steps into failures across
every flow at once, with no way for a reviewer to see the blast radius. The
steps that need `fail` are identifiable by hand (they clone, edit, or build),
and the docs say so.

*Alternative rejected:* infer the opt-in from the step having a `fallback.to`.
Too implicit — `fallback.to` is widely set for ordinary error routing, so this
would silently change behaviour for steps whose authors never considered turn
exhaustion.

### D5 — One knob with two values, not a three-way mode

`fail` means "treat exhaustion as a step failure", and the existing machinery
then supplies both behaviours the reported problem needs: retries first
(`fallback.retry`), routing second (`fallback.to`). A separate
`retry`-vs-`route` distinction would be redundant — `retry: 0` already
expresses "route immediately", and `retry: N` already expresses "spend N budgets
first".

### D6 — A distinct error type, not a reused one

`turnsExhaustedError` carries `StepID`, `MaxTurns`, `Attempts` and the wrap-up
document. Reusing `missingStructOutputError` would be wrong twice: its message
("expects structured output but agent produced empty response") is matched by
orchestrators and tests, and it describes the opposite situation — here the
agent *did* produce a document.

`MaxTurns` is the step override only. When the step inherits the agent/global
budget the flow layer does not resolve it, so the message says "its turn
budget" rather than printing a wrong number.

### D7 — Both exhaustion shapes share one predicate

The re-prompt and forced-wrap-up exemptions rest on "there is no turn budget
left to spend", which is true of both `turnsExhaustedError` and a
`missingStructOutputError` raised on an exhausted run. `errTurnsExhausted`
covers both so those call sites cannot drift apart; the existing
`structOutputTurnsExhausted` stays as the type-specific accessor beneath it.

### D8 — Merge the wrap-up document into the fallback step's args

The completion path already does `maps.Copy(args, structData)` before handing
args to the next step. The failure path passes `copyArgs(args)` — the inbound
args — which for a salvage step means losing the summary that names the branches
it needs to push. `mergeStructOutputIntoArgs` applies the same shallow merge on
the exhaustion failure path only. Other failures have no document, so they are
unaffected. It is best-effort: a non-object or unparseable document leaves args
untouched.

Note this does not change the persisted `flow_states.args`, which is marshalled
at step entry on both paths today; the merge affects what the next step
receives, exactly as on the completion path.

### D9 — Exhaustion must not read as transient

`isTransientProviderError` matches on message substrings, so the error text
deliberately avoids the provider signatures (`"maximum retry attempts
reached"`, `"rate limit"`, …). This matters because `implement`-shaped steps
carry `resume_after`, and a transient classification would park the step for
timed auto-resume — which resumes in a **fresh workspace**, guaranteeing the
loss this change exists to prevent. A test pins it.

## Risks / Trade-offs

- **A retry costs a second turn budget.** For an expensive step that is real
  money, and a step that exhausts twice pays twice before routing. Mitigated by
  it being opt-in and per-step: the author chooses `retry` knowing the step's
  cost.
- **A step that legitimately ends at its budget now fails if it opts in.**
  That is the intended reading for state-leaving steps, and `accept` remains
  available for anything where it is not.
