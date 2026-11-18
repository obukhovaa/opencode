## Purpose

Bound stalled model stream recovery while allowing queued input to guide a safe resend after the stream ends.

## ADDED Requirements

### Requirement: Stalled model turns SHALL be resent at most twice

For Anthropic, OpenAI and Gemini, a stalled model turn SHALL have at most two stall resends. Exhaustion SHALL fail the turn with a clear stall error. Other recovery paths within that turn SHALL NOT reset the stall budget. A successful agent iteration starts a new stall budget. The existing inactivity timeout, HTTP/transient retry policies and flow step retry SHALL remain unchanged.

#### Scenario: Repeated stalled calls
- **WHEN** the initial call and both resends stall
- **THEN** exactly three stalled calls occur and the turn fails

#### Scenario: A different recoverable error occurs between stalls
- **WHEN** a peer-reset or overflow recovery occurs within a stalled turn
- **THEN** it does not grant additional stall resends

#### Scenario: HTTP failures retain their existing retry policy
- **WHEN** a provider receives a rate limit or other error that its current policy retries
- **THEN** the same status classification, backoff and retry limit apply

### Requirement: A stall resend SHALL exclude unfinished assistant output

A stall resend SHALL discard partial output before constructing the next request. Unexecuted tool calls SHALL NOT run during recovery, and persisted calls SHALL have matching results if partial-message deletion fails.

#### Scenario: Partial response stalls
- **WHEN** a model streams part of a response and then stalls
- **THEN** the resend excludes that partial response and executes no partial tool call
