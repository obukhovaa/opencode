## ADDED Requirements

### Requirement: Request-bound question replies preserve router state
`POST /router/questions/{requestID}/reply` SHALL use existing router HTTP authentication and require an expected sessionId and exactly one of answers or text. Text SHALL use the existing single-prompt numeric/label/multiple/custom codec inside the runner. Answer counts, multiplicity, canonical labels and Custom permission SHALL be validated before settling the exact request/session through the in-process service. Pending/nudge state SHALL be cleared; only canonical option labels SHALL enter stale-click suppression. Malformed input SHALL return 400; lifecycle/session conflicts SHALL return 409, with a machine-readable code. An unavailable router SHALL return 503. No refusal SHALL enqueue generic inbound.

#### Scenario: A recovered selected answer settles the router
- **WHEN** a live request is answered with its exact session
- **THEN** Ask receives canonical answers, future nudge sweeps do not remind for it, and duplicate choice clicks are suppressed

#### Scenario: Custom text is repeated deliberately
- **WHEN** an accepted custom answer is followed by a plain message with the same text
- **THEN** that message is not swallowed by the new route's stale-click cache

#### Scenario: Invalid answer shape or disallowed custom value
- **WHEN** an answer count or multiplicity is wrong, or a value is not allowed by its prompt
- **THEN** the route returns400 with answer_shape or invalid_answer, and the request stays pending

#### Scenario: Reply races buffered fan-out
- **WHEN** a bound reply settles a request before a buffered auto-answer finishes
- **THEN** the buffered message is requeued as follow-up and no settled pending request or nudge is restored

#### Scenario: The step unbinds during a buffered answer
- **WHEN** ClearSession ends ownership while a buffered reply is in progress
- **THEN** its failure cannot restore pending state or input to the ended step

#### Scenario: Queued input precedes fan-out
- **WHEN** id-bearing queued input is already waiting when a bound reply arrives before fan-out
- **THEN** the question closes as unanswered, the reply returns409 queued_input_pending, and the queued input remains

### Requirement: Settled replies have bounded idempotent acknowledgements
The runner SHALL retain at most 256 settled reply receipts for five minutes, with request/session identity and a digest of the submitted representation, never prompt/answer text. An identical replay within that window SHALL return 200 with alreadyAnswered true without another Reply, including while a replacement question is pending. A different session or payload SHALL conflict. First success SHALL return alreadyAnswered false. Receipt expiry, eviction or process loss SHALL NOT allow an old request to answer a newer question.

#### Scenario: Applied answer response is lost
- **WHEN** the same exact request/session/payload is retried while its receipt remains
- **THEN** the caller gets200 alreadyAnswered true and no second answer occurs, without requiring the old pending request to exist

#### Scenario: Old request conflicts with a replacement
- **WHEN** a changed answer is submitted for an old settled request while a replacement is pending
- **THEN** the route returns409 reply_conflict and the replacement stays pending

#### Scenario: Receipt no longer available
- **WHEN** a settled request has no retained receipt or live service request
- **THEN** it returns409 without another answer; the caller must inspect run state rather than use a generic fallback

#### Scenario: Older runner lacks the route
- **WHEN** a caller reaches an older runner without this operation
- **THEN** it fails closed and SHALL NOT fall back to raw question-service HTTP or generic inbound
