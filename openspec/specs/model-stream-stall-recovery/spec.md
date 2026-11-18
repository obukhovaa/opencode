# Model Stream Stall Recovery Specification

## Purpose

Bound stalled model stream recovery while allowing queued input to guide a safe resend after the stream ends.

## Requirements

### Requirement: Stalled model turns SHALL be resent at most twice

For Anthropic, OpenAI and Gemini, a stalled model turn SHALL have at most two stall resends. Exhaustion SHALL fail a normal turn with a clear stall error. An exhausted max-turns wrap-up SHALL instead retain the forced-tool-choice contract: return previously accepted structured output (if any), without a hard error, and set `TurnsExhausted` so the flow exhaustion policy applies. Other recovery paths within that turn SHALL NOT reset the stall budget. A successful agent iteration starts a new stall budget. The existing inactivity timeout, HTTP/transient retry policies and flow step retry SHALL remain unchanged.

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

A stall resend SHALL discard partial output before constructing the next request. Unexecuted tool calls SHALL NOT run during recovery, and persisted calls SHALL have matching results if partial-message deletion fails. Every failed forced wrap-up SHALL close unfinished tool calls with matching error results, including cancellation and non-stall failures, while retaining earlier accepted structured output and turn-exhaustion bookkeeping.

#### Scenario: Partial response stalls
- **WHEN** a model streams part of a response and then stalls
- **THEN** the resend excludes that partial response and executes no partial tool call

#### Scenario: Forced wrap-up stalls
- **WHEN** the max-turns wrap-up exhausts its stall resends
- **THEN** the response preserves earlier accepted structured output and reports turn exhaustion without a hard error

#### Scenario: Forced wrap-up fails after a partial tool call
- **WHEN** a forced wrap-up emits a tool call and fails with cancellation or another non-stall error, including after a stall resend
- **THEN** the stored tool call has a matching error result and its assistant message is finished
- **AND** earlier accepted structured output and turn-exhaustion bookkeeping remain available

### Requirement: Auxiliary summary calls SHALL have bounded stall recovery

Recap, synchronous compaction and manual summarization SHALL each permit at most two stall resends per request, with a budget independent of the main agent iteration. Cancellation and non-stall errors SHALL NOT trigger a stall resend. Partial response output SHALL NOT be retained across resends.

#### Scenario: Summary recovers
- **WHEN** a summary request stalls twice and then succeeds
- **THEN** the result contains only the successful response

#### Scenario: Summary keeps stalling
- **WHEN** the initial summary request and both resends stall
- **THEN** the request fails with the stall cause after exactly three sends
