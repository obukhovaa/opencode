## ADDED Requirements

### Requirement: Pending input SHALL reach stall resends and resumed runs

The agent SHALL deliver pending session input before each stalled-call resend and before the first model call of a new run on that session, using the existing persistence and delivery acknowledgement mechanism. Input remaining when a turn fails SHALL stay unacknowledged and available to the next run. No model or tool call SHALL be interrupted for inbox delivery. Existing tool-result and natural turn-end drains SHALL remain.

#### Scenario: Input arrives while a model call stalls
- **WHEN** input arrives during a call that subsequently stalls and is eligible for resend
- **THEN** it is delivered after the stream ends and appears in the resend history

#### Scenario: Input is pending before a run
- **WHEN** a new run starts on an existing session with queued input
- **THEN** the first model request includes that input

#### Scenario: Input remains after failure
- **WHEN** input arrives during the final failed call and the run ends
- **THEN** it remains unacknowledged until a subsequent run delivers it in its first model request

## MODIFIED Requirements

### Requirement: Inbox drains SHALL count against the turn budget only when they add a model call

A drain at a tool boundary SHALL NOT consume turn budget of its own. A cycle that runs only because the inbox held input at a run-end point SHALL count against `max_turns` like any other cycle. The forced max-turns wrap-up turn SHALL NOT drain at its start or completion; if its model call stalls, a bounded stall resend SHALL deliver pending input without extending the turn budget.

#### Scenario: Wrap-up turn leaves input waiting
- **WHEN** input arrives during a wrap-up call that does not stall
- **THEN** that call completes without another inbox drain and the input remains unacknowledged

#### Scenario: Wrap-up stall resend
- **WHEN** a wrap-up call stalls with pending input and a stall resend remains
- **THEN** the resend includes the input and the existing two-resend limit still applies
