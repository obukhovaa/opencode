## Context

`runStep` resolves successors with `resolveNextSteps(step.Rules, …)`, which walks every rule and returns
every match. When the returned slice is empty, nothing is sent to `nextSteps`, the step's `completed`
state is published, the channels drain, and the terminal selector in the API runner falls through to
`flow.completed` — its default when no error is recorded and no step is postponed.

Three properties combine to make this worse than it sounds:

- **No warning is logged.** The same loop logs a `WARN` when a predicate *errors*, so the case that ends
  the run without doing the work is quieter than the case that merely mis-evaluates.
- **No flow file can catch it.** `evaluatePredicate` resolves the args path first and returns
  `(false, nil)` on an absent key before reaching the operator or `sizeof`, so every atom mentioning that
  key is false — `== true`, `!= true`, `sizeof … != 0` alike. A rule with no `if` is not an `else`: under
  all-match it fires alongside whichever conditional matched, so a catch-all rule would fire on healthy
  runs too. `fallback` fires on a step error, and this is not an error.
- **The runtime already rejects the same shape one layer over.** On the resume path an empty work set is
  required to WARN and fall back rather than no-op (`flow-runtime-resume`, "Gate-vs-planner disagreement
  falls back to restart"). Mid-run routing never got the equivalent.

The trigger for fixing it now is GENAI-170 in the piano-developer workspace, where interactive steps gate
their success rules on a field declared `required` with no `default`. That is the correct way to make a
field's presence a real signal, but it means a step producing no structured output leaves the key absent,
every rule false, and the run reports success having authored nothing.

## Goals / Non-Goals

**Goals:**

- A flow author can declare that a step must always select a successor.
- When such a step cannot, the run reaches a step that can tell the triggering party what happened.
- A zero-match is visible in logs for every step, opted in or not.
- Zero behaviour change for existing flows.

**Non-Goals:**

- Validating struct output against the declared schema. An out-of-enum value still matches no rule; this
  change makes that reportable, not impossible.
- The interactive-step exemption from the forced-`struct_output` wrap-up turn. It is one way a step ends
  up producing nothing, but it is a separate concern with its own risks, and a previous attempt to bundle
  it into the GENAI-170 flow work was withdrawn for being unrelated to that incident.
- Changing what `flow.completed` means for a run that legitimately ends with no match.
- Adopting the field in any flow. That is a separate change in the workspace repo, and it must land after
  this one is deployed.

## Decisions

### D1: Opt-in per step, not a global behaviour change

`require_route: bool` on `Step`, absent by default.

The alternative — treat every zero-match as an error — is what the problem statement invites, and it is
wrong. A zero-match is a *sanctioned* way for a bounded loop to end: `flow-runtime-resume` documents a
self-route guarded by `${step.iteration} != 3` selecting nothing at iteration 3 as "loop terminated
normally", with a scenario asserting the runtime restarts cleanly on the next trigger. An unconditional
error would convert that documented pattern into a failure, and any flow relying on "no rule matches means
done" would break on upgrade.

So the runtime cannot infer intent, and the flow author states it. A step that must always route says so;
a loop that ends by predicate says nothing and keeps working.

### D2: Reuse the step-error path rather than inventing a terminal state

When a `require_route` step selects nothing, call `handleStepError` with a descriptive error.

This is the whole reason the design is small. `handleStepError` already routes to `step.Fallback.To` when
declared, already persists the failed state, and already publishes the events the API runner turns into a
terminal status. So the reporting the requirement asks for — an issue comment, a message in the chat
thread — comes from the flow author's existing fallback chain reaching a step that already does those
things. No new state, no new event type, no orchestrator change.

*Alternative considered:* a new run-level flag on `FlowResult` (`EndedWithoutRoute`) for the orchestrator
to surface. Rejected: it needs a matching change in a second repo before anything is reported, and it
leaves the flow author no way to choose where the explanation comes from.

*Alternative considered:* a new `flow.dead_end` terminal status. Rejected as a larger blast radius — every
consumer of the terminal status would need to learn it, to express something the failure path already
expresses.

### D3: Warn on every zero-match, including steps that have not opted in

The log line is independent of the flag, because the flag only exists for authors who already suspect a
step can dead-end. The steps most likely to do it silently are the ones nobody thought about.

This mirrors the resume path, which already warns on an empty work set, and it costs one line per
occurrence. A legitimate loop exit will log it too; that is acceptable — a bounded loop ends once per run.
A terminal step declaring no rules at all must NOT warn, since selecting no successor is its purpose.

### D4: The error message names the step and the cause

The receiving fallback step gets the accumulated args but not the error text, so the message exists for the
log and for the persisted state rather than for the model. It still needs to say which step stopped and
that no rule matched, so that a human reading the flow state can tell this apart from an agent failure —
they are diagnosed completely differently.

## Risks / Trade-offs

- **A flow adopts `require_route` on a step whose rules are legitimately non-exhaustive** → the run starts
  failing where it used to complete. Mitigated by opt-in: the author has to add the field, and the loop
  patterns that rely on zero-match are exactly the ones that will not.
- **Warning noise on flows with bounded loops** → one line per loop exit, at WARN. Acceptable; the
  alternative is the current silence.
- **The field is silently ignored by an older binary.** Both this loader and the c2-agent orchestrator use
  plain `yaml.Unmarshal` with no `KnownFields`, so a flow that sets `require_route` against a runtime that
  predates this change gets no error and no protection. That makes adoption forgiving of ordering but
  invisible when it is wrong, so the adoption change in the workspace repo must state the dependency.
- **This does not make silent completion impossible, only declarable.** A step that has not opted in still
  ends the run quietly. Closing it fully would mean validating output against the schema, or making
  zero-match an error everywhere — the first is out of scope, the second is D1's rejected option.

## Migration Plan

Additive and opt-in: no existing flow changes behaviour, so there is nothing to migrate and no ordering
requirement for this change itself. Ships as an image, deployed the normal way.

Adoption is a separate change in the workspace repo, on the steps whose rules gate on a required-no-default
field, and it must land after this is deployed — not because it would break (the key is ignored) but
because it would appear to work while doing nothing.

## Open Questions

- Whether `c2-agent`'s flow structs need the field added for its own validation surface. It reads the same
  YAML with its own types and ignores unknown keys, so nothing breaks, but its flow-authoring
  documentation and any lint it grows should know the field exists. Not blocking.
- Whether the warning should carry the evaluated predicate strings as well as the rule count. Useful when
  diagnosing, but the predicates can be long and the args are already persisted on the step state.
