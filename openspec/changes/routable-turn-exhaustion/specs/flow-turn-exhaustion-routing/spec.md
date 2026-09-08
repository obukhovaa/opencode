## Purpose

Lets a flow step declare that being cut off at its turn budget is not success.
A run the agent runtime forced to wrap up at `maxTurns` produces a valid
`struct_output` document, and today the step completes on it — indistinguishable
from a run the model chose to end. For a step that leaves state on a disposable
pod (a clone, a branch, an uncommitted diff) that is a silent data-loss path:
the safety-net step wired to `fallback.to` never runs. This capability routes
turn exhaustion through the step's existing fallback machinery instead —
retrying on the same session with a fresh turn budget first, routing to the
fallback step once that budget is spent.

## ADDED Requirements

### Requirement: `fallback.on_turns_exhausted` declares what turn exhaustion means for a step

A step's `fallback` block SHALL accept an optional `on_turns_exhausted` key with
exactly two valid values: `accept` and `fail`. An absent or empty value SHALL be
equivalent to `accept`. A flow declaring any other value SHALL be rejected at
load time with an error identifying the step and the two valid values, so a typo
is a load failure rather than a silently-ignored setting.

#### Scenario: Absent key preserves existing behaviour
- **GIVEN** a step whose `fallback` declares `retry` and `to` but no `on_turns_exhausted`
- **WHEN** its agent run ends on the turn budget having produced a usable `struct_output`
- **THEN** the step completes on that document and routes on its rules, and no retry is spent

#### Scenario: Unknown value is a load-time failure
- **GIVEN** a flow with `on_turns_exhausted: retry`
- **WHEN** the flow is validated
- **THEN** validation fails naming the step and the valid values `accept` and `fail`

#### Scenario: Explicit accept behaves as the default
- **GIVEN** a step with `on_turns_exhausted: accept`
- **WHEN** its agent run ends on the turn budget with a usable `struct_output`
- **THEN** the step completes, exactly as with the key absent

### Requirement: `fail` makes turn exhaustion consume the retry budget on the same session

For a step with `on_turns_exhausted: fail`, a run that ended on its turn budget
(`AgentEvent.TurnsExhausted`) while producing a usable `struct_output` SHALL be
treated as a step failure by the attempt loop, consuming one `fallback.retry`
attempt. Each retry attempt SHALL re-run the step against the SAME session id
and the same per-step turn budget, so the agent continues on the same pod and
the same working tree with a fresh budget rather than starting over.

#### Scenario: One retry rescues a step that was one push short
- **GIVEN** a step with `maxTurns: 200`, `fallback: {retry: 1, to: salvage, on_turns_exhausted: fail}`
- **AND** its first run ends on the turn budget reporting work committed but not pushed
- **WHEN** the retry attempt runs and completes within its fresh budget
- **THEN** exactly two runs of that step occur, the step completes on the retry's document, and the fallback step does not run

#### Scenario: Retry reuses the session rather than starting a new one
- **GIVEN** a step retried after turn exhaustion
- **WHEN** the retry attempt is issued
- **THEN** it is issued against the same session id as the exhausted attempt, with the step's own `maxTurns` applied to that attempt

### Requirement: Exhaustion that outlives the retry budget fails the step and routes to `fallback.to`

When every attempt of a step with `on_turns_exhausted: fail` ends on the turn
budget, the step SHALL reach terminal `failed` status and SHALL route to
`fallback.to` when one is declared. The recorded failure SHALL state that the
step was cut off and its work is incomplete, SHALL name the step's turn budget
when the step declares one, and SHALL record how many attempts were spent when
more than one.

#### Scenario: Retries spent routes to the salvage step
- **GIVEN** a step with `fallback: {retry: 1, to: salvage, on_turns_exhausted: fail}`
- **AND** both attempts end on the turn budget
- **WHEN** the second attempt returns
- **THEN** the step is `failed`, its output names the exhausted budget and the two attempts, and the `salvage` step runs

#### Scenario: `retry: 0` routes without a second turn budget
- **GIVEN** a step with `fallback: {retry: 0, to: salvage, on_turns_exhausted: fail}`
- **WHEN** its only run ends on the turn budget
- **THEN** exactly one run of that step occurs, the step is `failed`, and the `salvage` step runs

### Requirement: The fallback step inherits the cut-off run's wrap-up document

When a step fails through turn exhaustion and routes to `fallback.to`, the
top-level fields of the exhausted run's `struct_output` document SHALL be merged
into the args the fallback step receives, using the same shallow merge the
completion path applies. Merging SHALL be best-effort: a document that is absent,
unparseable, or not a JSON object SHALL leave the args unchanged. Failures other
than turn exhaustion carry no document and SHALL be unaffected.

#### Scenario: Salvage step sees what the cut-off run reported
- **GIVEN** an exhausted run whose document carries a `summary` naming unpushed commits and one entry in `gitlab_links`
- **WHEN** the step routes to its fallback step
- **THEN** that step's args carry both fields, alongside the inbound args

#### Scenario: Unusable document leaves args untouched
- **GIVEN** an exhausted run whose captured document is empty, not JSON, or a JSON array
- **WHEN** the step routes to its fallback step
- **THEN** that step's args are exactly the inbound args

### Requirement: Turn exhaustion is never treated as a transient provider error

A turn-budget exhaustion SHALL NOT be classified as a transient
provider/gateway error. A step declaring `resume_after` SHALL therefore fail and
route on exhaustion rather than parking for timed auto-resume, because a resume
runs in a fresh workspace where the step's half-finished working tree no longer
exists.

#### Scenario: A resume_after step does not park on exhaustion
- **GIVEN** a step with `resume_after: 30m` and `on_turns_exhausted: fail`
- **WHEN** its final attempt ends on the turn budget
- **THEN** the step reaches terminal `failed` and routes to `fallback.to`, and is not stored as `postponed`

### Requirement: Turn-exhausted runs are exempt from work that needs turn budget

Recovery paths that spend a model turn — the interactive missing-`struct_output`
re-prompt and the non-interactive last-ditch forced `struct_output` turn — SHALL
be skipped when the step outcome is a turn exhaustion, in either of the two
shapes that produce one: a turn-exhausted run that produced no document, or a
turn-exhausted run treated as a failure under `on_turns_exhausted: fail`. A run
with no budget left cannot pay for another turn.

#### Scenario: No forced wrap-up on a turn-exhausted failure
- **GIVEN** a non-interactive schema-bearing step that failed through `on_turns_exhausted: fail`
- **WHEN** the failure path is reached
- **THEN** no forced `struct_output` turn is issued

#### Scenario: Empty exhausted run keeps its existing failure
- **GIVEN** a schema-bearing step whose run ended on the turn budget with neither `struct_output` nor prose
- **WHEN** the attempt returns
- **THEN** the step fails with the existing missing-structured-output error, which carries the agent's last assistant text
