## ADDED Requirements

### Requirement: Request-bound question replies preserve router state
`POST /router/questions/{requestID}/reply` SHALL use the router's existing HTTP authentication and require an expected `sessionId` and non-empty `answers`. The router SHALL verify that the exact request is live in that session, settle it through the in-process question service, clear its pending/nudge state and remember answered labels before returning 200. A missing, stale, wrong-session or conflicting newer request SHALL return 409 without answering another question or enqueueing inbound. An unavailable router SHALL return 503. Malformed bodies SHALL return 400.

#### Scenario: A recovered selected answer settles the router
- **WHEN** a live request is answered through the route with its exact session
- **THEN** the waiting Ask receives the answer, future nudge sweeps do not send reminders for it, and duplicate legacy answer labels are suppressed

#### Scenario: A replacement question uses the same label
- **WHEN** the original request is answered again after a replacement question is pending
- **THEN** the route refuses the stale ID and the replacement remains pending

#### Scenario: Reply races fan-out
- **WHEN** a live service request is answered before its Created event is processed
- **THEN** the reply succeeds and the delayed event does not restore pending state or send a stale question

#### Scenario: Older runner has no route
- **WHEN** an orchestrator's request reaches an older runner without this route
- **THEN** the request fails closed and the caller SHALL NOT fall back to raw question-service HTTP or generic inbound
