## ADDED Requirements

### Requirement: A zero-match rule evaluation is logged

When a step declares routing rules and none of them match, the runtime SHALL log a warning naming the
step and the number of rules evaluated, whether or not the step opts into `require_route`.

The quiet case is currently the dangerous one: a predicate that ERRORS on the same code path already
warns, while a predicate set that merely selects nothing says nothing at all — so the outcome that ends
the run without doing the work is the one that leaves no trace.

#### Scenario: Rules select no successor

- **GIVEN** a step declaring one or more routing rules
- **WHEN** every rule evaluates false
- **THEN** the runtime MUST log a warning identifying the step and the rule count
- **AND** the log line MUST be emitted before the step's terminal state is published, so the warning and
  the state appear in causal order

#### Scenario: Step declares no rules at all

- **GIVEN** a terminal step declaring neither `rules` nor `fallback`
- **WHEN** it completes
- **THEN** the runtime MUST NOT log a zero-match warning, because selecting no successor is that step's
  entire purpose

### Requirement: A step can declare that routing is mandatory

The runtime SHALL support an optional step field `require_route: bool`. When a step sets it `true` and
that step's rules select no successor, the runtime SHALL treat the outcome as a step error rather than as
a completion, so that the step's declared `fallback.to` receives control and the run reaches a step that
can report what happened.

Absent or `false` — the default — the runtime SHALL preserve existing behaviour: no successor is
scheduled and the run ends through the normal terminal-status path.

The default is deliberate. A zero-match is a sanctioned way for a bounded loop to end: a self-route
guarded by `${step.iteration} != N` selects nothing at iteration N, and that loop terminated normally.
Making zero-match an error for every step would convert that documented pattern into a failure.

#### Scenario: `require_route` step cannot route and declares a fallback

- **GIVEN** a step with `require_route: true` and `fallback.to: report-problem`
- **WHEN** its rules select no successor
- **THEN** the runtime MUST route to `report-problem` as it would for any step error
- **AND** the error MUST name the step and state that no rule matched, so the receiving step can explain
  the stop rather than inventing a reason

#### Scenario: `require_route` step cannot route and declares no fallback

- **GIVEN** a step with `require_route: true` and no `fallback`
- **WHEN** its rules select no successor
- **THEN** the run MUST end through the failure path, not as a completion

#### Scenario: Bounded loop ends by predicate

- **GIVEN** a step that does NOT declare `require_route`, whose only rule self-routes while
  `${step.iteration} != 3`
- **WHEN** iteration 3 evaluates the rule false and selects no successor
- **THEN** the runtime MUST end the run through the normal terminal-status path, exactly as before this
  change
- **AND** MUST NOT treat the loop's normal termination as an error

#### Scenario: Field is absent from every existing flow

- **GIVEN** any flow authored before this change
- **WHEN** it runs
- **THEN** its observable behaviour MUST be unchanged apart from the new warning log line

### Requirement: A run that stops without delivering is distinguishable from one that succeeded

A run whose last scheduled step opted into `require_route` and could not route SHALL NOT be reported as a
successful completion. Whoever triggered the job SHALL be able to tell, from the run's reported outcome,
that it stopped without finishing.

This is the point of the change. A flow author who has written a step that must always select a successor
gains a way to say so, and the runtime stops presenting that step's silence as success.

#### Scenario: Triggering party is told the run stopped

- **GIVEN** a `require_route` step that cannot route, in a flow whose fallback chain reaches a step that
  notifies the triggering party
- **WHEN** the run ends
- **THEN** the reported outcome MUST NOT be a plain success
- **AND** the notifying step MUST receive the accumulated args, so it can name the step that stopped

#### Scenario: Legitimate completion is unaffected

- **GIVEN** a flow that reaches a declared terminal step normally
- **WHEN** the run ends
- **THEN** it MUST still be reported as a successful completion
