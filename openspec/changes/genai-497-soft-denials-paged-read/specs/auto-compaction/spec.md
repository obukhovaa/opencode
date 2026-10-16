## ADDED Requirements

### Requirement: A call rejected as too long recovers once

When the main model call of a turn fails, the error is not a cancellation, `autoCompact` is on, and the failure is classified as a context overflow — the error carries a known provider overflow message (`prompt is too long`, `input is too long`, `exceed context limit`, `context_length_exceeded`, `maximum context length`, `exceeds the maximum number of tokens`), or the last context count was at least 90% of the window — the system SHALL, if the compaction backoff allows it, run one auto-compaction with trigger `overflow_retry` and send the same call again on the compacted history. The failed call's message SHALL be finished as an error, with error results for any tool calls it streamed, before compacting. The retry SHALL NOT count as a turn. At most one such recovery SHALL run per agent run. When the compaction fails, or the retry fails for a reason other than cancellation, the run SHALL fail with the original error and the retry's error SHALL be logged. The recovery SHALL log its cause.

#### Scenario: Recovered turn

- **WHEN** a call fails with `prompt is too long` and the compaction succeeds
- **THEN** the summarizer is called once and the same call is retried on a history headed by the summary, ending with the turn's user message (or the previous tool call and result)

#### Scenario: Overflow again

- **WHEN** the retry also fails with `prompt is too long`
- **THEN** the run fails with the original error after exactly two model calls and one summarizer call

#### Scenario: No recovery

- **WHEN** `autoCompact` is off, the backoff forbids a compaction, the compaction fails, or the error is not an overflow far from the window
- **THEN** no retry is made and the run fails with the original error
