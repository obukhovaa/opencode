## Context

Three facts about the existing code determine the shape of this change.

**D1 — the signal already exists.** `AgentEvent.TurnsExhausted`
(`internal/llm/agent/agent.go`) is set by both budget gates: the inner-loop
max-turns wrap-up and the outer-cycle (background-task re-entry) cap. No
agent-runtime change is needed; the gap is entirely in `internal/flow`.

**D2 — retrying is nearly free and lands in the right place, but only with a
continuation prompt.** The attempt loop in `runStep` calls
`agentSvc.RunWith(runCtx, sess.ID, …, step.MaxTurns, …)` with the same
`sess.ID` on every attempt. A retry therefore appends to the existing session —
full message history, same pod, same working tree — and `step.MaxTurns` is
applied per call, so each attempt gets a fresh budget.

The session state a retry lands in, however, makes the *prompt* load-bearing.
Its tail is the runtime's forced max-turns instruction ("Call the
`struct_output` tool now … Do not reply with prose") followed by the document
the agent produced in reply. Re-sending the step's original prompt verbatim
into that context is ambiguous in a dangerous direction: the agent has just
been told it was finished, so the cheapest consistent reply is to restate that
document — which returns a NON-exhausted result and therefore *completes the
step*, carrying the same unfinished work, one attempt later. The opt-in would
then be strictly worse than `accept`: identical silent green, plus a wasted
turn budget. So the retry sends `turnsExhaustedRetryPrompt`: continue, inspect
the working tree rather than trusting your summary, finish only what remains,
do not duplicate commits or MRs, original task appended for its goal and args.
This mirrors `structOutputRetryPrompt`, which exists for the same reason on
the re-prompt path.

**D3 — three shapes are currently silent, not one.** A schema-bearing run that
produced neither a document nor prose already becomes a
`missingStructOutputError` and already consumes `fallback.retry`; that path is
untouched. Everything else completes silently today and so must fail under the
opt-in:

- *exhausted + usable `struct_output`* — the forced wrap-up's normal output;
- *exhausted + prose only* — a provider that ignored the forced `tool_choice`,
  or an `end_turn` text reply (`agent.go`'s documented degradation path). The
  missing-struct_output check waves this through with a "text fallback"
  warning, so without the opt-in it completes on prose;
- *exhausted on a step with no `output.schema`* — the check above is gated on
  `step.Output != nil` and never runs.

`turnsExhaustedOutcome` therefore does not require a document. It instead
carries off whatever the run reported — `StructOutput` for a document,
`LastAssistant` for prose — so neither the failure message nor the salvage
step's inherited args go empty in the shapes that have the least to show.

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

### D10 — and the converse: exhaustion vetoes the park outright

Avoiding the transient *classification* is not enough, because the opt-in
changes what else the step gets to do. Under `accept` an exhausted run
completed immediately and made no further provider calls, so the park branch
was unreachable from exhaustion by construction. Under `fail` with `retry: N`
the step makes up to N more round-trips — on the heaviest request of the job —
each a fresh chance to draw a genuine 429 or `overloaded`. Since the park is
selected from the FINAL attempt's error, opting in to protect the working tree
would otherwise *raise* the probability of the fresh-workspace resume that
destroys it.

So the park is gated on `exhausted == nil`: once any attempt ran out of turns,
the step will not park, whatever the last attempt died of. Failing to
`fallback.to` — which runs in this process, on this pod, with the tree still on
disk — always beats parking a step that left work behind.

### D11 — `lastErr` cannot be the carrier; `exhausted` is

Every attempt overwrites `lastErr`. So a step that exhausted on attempt 1 and
lost attempt 2 to anything else — `ErrSessionBusy` from the slot-release race
the sibling retry helpers already budget for, a step timeout, a provider error
— would lose the *only* copy of the cut-off run's report, exactly when the
salvage step needs it, and would report the incidental error as the step's
outcome.

`runStep` therefore holds the last `*turnsExhaustedError` in its own variable
and, on the failure path, reports it in preference to whatever the final
attempt tripped over. This also closes a silent-success hole: the last-ditch
forced wrap-up is gated on `exhausted == nil` too, because it sets
`lastErr = nil` on success and would otherwise forgive an exhaustion whenever a
later attempt failed for an unrelated reason.

### D12 — the failure-path merge goes into the fallback step's own copy

The completion path can share the args map with the state it publishes because
it merges *before* building it. The failure path cannot: `failedState` holds
`args` by reference and has already been sent to the `flowStates` channel and
the pubsub broker by the time the merge point is reached. Merging into `args`
there is an unsynchronised write against a value other goroutines can read — a
process-fatal `concurrent map read and map write`, not a recoverable flow error
— and it retroactively rewrites an event already on the wire to advertise args
the step never ran with. So the merge target is `copyArgs(args)`, handed
straight to the `stepWork`. A test asserts the failed step's published args
stay clean.

## Risks / Trade-offs

- **A retry costs a second turn budget.** For an expensive step that is real
  money, and a step that exhausts twice pays twice before routing. Mitigated by
  it being opt-in and per-step: the author chooses `retry` knowing the step's
  cost.
- **A step that legitimately ends at its budget now fails if it opts in.**
  That is the intended reading for state-leaving steps, and `accept` remains
  available for anything where it is not.
- **Each attempt gets a full `Step.Timeout`, not a share of one.** `stepCtx` is
  built inside the attempt loop, so `timeout: 2h` with `retry: 2` bounds the
  step at ~6h rather than 2h, and there is no flow-level or cost-level cap to
  catch it. Documented in `docs/flows.md`; authors opting in on a long step
  should size `retry` accordingly.
- **`fallback.to` reddens the run even when salvage succeeds.** `flowRunner`
  latches the first `failed` step into `state.err` and never clears it, so the
  run reports `flow.failed` even after a salvage step recovers everything. This
  is pre-existing `fallback.to` semantics, not new here, but it is the trade
  the opt-in actually makes: "green with the work lost" becomes "red with the
  work recovered". Documented, and left alone deliberately — changing run
  terminal-status semantics is a separate change with its own blast radius.
- **The merge lets a cut-off run's document overwrite inbound args.** This is
  the same shallow `maps.Copy` the completion path uses, and `struct_output` is
  not schema-validated beyond "is an object with the required keys present", so
  a model-chosen field can clobber an orchestrator-supplied one (`team`,
  `branch`) for the `fallback.to` step. Accepted here for consistency with the
  completion path, but note the asymmetry: on this path the document comes from
  a run the flow layer has itself just classified as incomplete. Worth
  revisiting (prefix the merged keys, or let inbound args win) if salvage steps
  start keying off ground-truth args.
