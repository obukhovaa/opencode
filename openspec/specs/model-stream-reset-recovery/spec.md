# model-stream-reset-recovery Specification

## Purpose
Keep a run alive when the peer resets the model's HTTP/2 stream after the provider
stream loop can no longer replay it: the agent loop resends the turn once (CD-5495).

## Requirements
### Requirement: A turn whose model stream the peer reset is resent once
When a turn's model call fails because the peer reset the HTTP/2 stream with a code the
provider stream loops treat as retryable (`INTERNAL_ERROR`, `REFUSED_STREAM`,
`ENHANCE_YOUR_CALM`, `NO_ERROR`, received from the peer), the agent loop SHALL delete
the partial assistant message and send the same message history again, once per turn,
after a short pause. If the delete fails, the partial message SHALL be closed as a
failed turn's message is. Only an error from the model's response stream qualifies, so
no tool of the failed call has run.

#### Scenario: A reset after partial output is resent
- **WHEN** a model call streams a delta and then ends in a peer `RST_STREAM INTERNAL_ERROR`
- **THEN** the partial assistant message is deleted, the same history is sent once more, and the run continues on that response

#### Scenario: A reset the provider stopped replaying is resent too
- **WHEN** the peer resets every pre-output attempt until the provider stream loop's replay budget runs out
- **THEN** the turn is resent once in the same way, and the resend gets its own replay budget

#### Scenario: The resend is bounded
- **WHEN** the resent call is reset again
- **THEN** the run fails with that error and the turn is not sent a third time

#### Scenario: Other errors are not resent
- **WHEN** the call fails with any other error, including a locally generated stream error or a peer reset with a non-retryable code such as `PROTOCOL_ERROR`
- **THEN** the run fails as before with no resend

#### Scenario: The overflow recovery takes precedence
- **WHEN** the reset is near the context window and the turn went through the overflow compaction and retry
- **THEN** no stream-reset resend follows it

#### Scenario: A cancel during the pause ends the run as cancelled
- **WHEN** the run is cancelled while it waits to resend
- **THEN** no resend is made and the run ends as cancelled

