## Why

A step whose routing rules all evaluate false ends the run silently. `resolveNextSteps` returns an empty
slice, nothing is scheduled, no warning is logged, the step is persisted `completed`, and the terminal
selector announces **`flow.completed`** — indistinguishable from a flow that finished its work. Whoever
triggered the job is told it succeeded. Nothing says which step stopped, or why.

This is not hypothetical and it is not new. It is reachable whenever a step's rule set does not cover an
output shape the model can produce, and nothing validates output types, so an out-of-enum string or a
boolean-shaped field returned as text is enough. It also became reachable in a new way in
`developer-react-on-jira-openspec` (GENAI-170), where interactive steps gate their success rules on a
field declared `required` with no `default`: on a step that produces no structured output at all, that
field is absent, every predicate referencing it is false, and the run reports success having done
nothing.

**There is no way to fix this in a flow file.** `evaluatePredicate` resolves the args path first and
returns `(false, nil)` on an absent key before any operator or `sizeof` runs, so every atom mentioning
that key is false — `== true`, `!= true`, `sizeof … != 0` alike. A rule with no `if` is not an `else`:
under all-match evaluation it fires *alongside* whichever conditional matched, so using one as a catch-all
would post a "this went nowhere" notice on every healthy run. And `fallback` cannot help, because it fires
on a step *error* and a zero-match is not an error.

The runtime already holds the opposite position one layer over. On the RESUME path, an empty work set is
explicitly required to WARN and be handled rather than silently no-op (`flow-runtime-resume`, "Gate-vs-planner
disagreement falls back to restart"). Mid-run routing simply never got the same treatment.

## What Changes

- **A new optional step field `require_route: bool`.** When `true` and a step's rules produce no match,
  the runtime treats it as a step error, so the step's declared `fallback.to` fires and the flow reaches
  a real terminal step — one that can comment on the issue and post to the chat thread. With no
  `fallback`, it is a terminal failure, which is still louder than a false success.
- **A zero-match evaluation is logged at WARN regardless of the flag**, naming the step and the rule
  count, matching what the resume path already does for an empty work set. Today it logs nothing at all,
  while a *predicate error* on the same code path does warn — so the quieter case is the more dangerous
  one.
- **Absent by default, so no existing flow changes behaviour.** This matters more than it looks: a
  zero-match is a *sanctioned* way for a loop to end. `flow-runtime-resume` documents
  `${step.iteration} != 3 → loop` going false at iteration 3 as "loop terminated normally". Making
  zero-match an error unconditionally would turn that documented pattern into a failure, which is why
  this is opt-in rather than a behaviour change.

Out of scope:

- Validating output types against the schema (an out-of-enum `correction_path` would still match no rule;
  with `require_route` it would at least be reported).
- The interactive-step exemption from the forced-`struct_output` wrap-up. Related — it is one way a step
  produces nothing — but a separate concern with its own trade-offs.
- Any change to what `flow.completed` means for runs that legitimately end with no match.

## Capabilities

### New Capabilities

- `flow-dead-end-detection`: what the runtime does when a step's rules select no successor — when that is
  a legitimate end, when it is a dead end, how a flow author declares the difference, and what the run
  reports in each case.

### Modified Capabilities

None. `flow-runtime-resume` covers the resume-path empty work set and is unchanged; this adds the mid-run
counterpart as its own capability rather than widening that one.

## Impact

- `internal/flow/flow.go` — one optional field on `Step`.
- `internal/flow/service.go` — the zero-match branch at the `resolveNextSteps` call site in `runStep`.
- `internal/flow/registry.go` — no change expected; the field needs no cross-field validation.
- Flow authors: opt-in per step. `developer-react-on-jira-openspec` in the piano-developer workspace is
  the first intended consumer, on the steps whose rules gate on a required-no-default field.
- The `c2-agent` orchestrator reads the same YAML with its own structs and ignores keys it does not
  declare, so an opencode-only field is safe there. Worth confirming before the flow side adopts it.
- No API, event or schema changes. `flow.completed` still means what it means; a `require_route` step that
  cannot route now reports through the failure path instead.
