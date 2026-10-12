## REMOVED Requirements

### Requirement: The context count is floored by reported usage

**Reason**: The provider estimate it starts from is 3.5x low on agentic histories, so its 1.5x plausibility guard discarded every real report and compaction never triggered.
**Migration**: Replaced by "The context count is the last reported usage"; the doubled-report case moves to "An implausible cache read is capped".

## ADDED Requirements

### Requirement: The context count is the last reported usage

The system SHALL count the context size for every auto-compaction decision as the usage reported for the session's last model call (`PromptTokens + CompletionTokens`) plus a local estimate of only the messages after the last message that report covers. It SHALL NOT send a token-count request or estimate the history before that message. A context window of 0 SHALL never trigger compaction.

#### Scenario: Reported size over the threshold compacts

- **WHEN** the session's last call reported 756,968 tokens, the local estimate of the same history is 214,983, and the effective threshold is 0.4 of a 1,000,000-token window
- **THEN** the count is 756,968 plus the estimate of the messages after the last assistant message, and compaction triggers

#### Scenario: Reported size under the threshold

- **WHEN** the reported usage plus the delta is under the effective threshold
- **THEN** no summarizer call is made

#### Scenario: Cached prompt tokens are counted once

- **WHEN** a provider reports a prompt of which part was served from cache (Gemini `PromptTokenCount` includes `CachedContentTokenCount`)
- **THEN** the recorded usage counts the cached part once, as cache-read tokens, and the count equals the real prompt plus output

### Requirement: The report's last message is its anchor

The system SHALL take as the last message a report covers the last non-synthetic assistant message, or, when the report is a compaction's (`PromptTokens` 0, `CompletionTokens` above 0), the summary message.

#### Scenario: Anchored on the last assistant message

- **WHEN** the history ends with an assistant message and the tool results of its calls
- **THEN** the tool results are the delta and nothing before them is estimated

#### Scenario: After compaction

- **WHEN** the session's report is a compaction's summary output
- **THEN** the count is that output plus the estimate of the messages after the summary message

#### Scenario: Background-task completion after the report

- **WHEN** synthetic completion messages follow the last reported assistant message
- **THEN** they are counted in the delta

### Requirement: A history without a report is not compacted

The system SHALL NOT trigger compaction when no report covers the history: no usage recorded, no message to anchor the report, or a compaction whose summary reported no output. The local estimate of the history SHALL then be used only to bound the response's `max_tokens` and for the overflow diagnostic.

#### Scenario: No report yet

- **WHEN** a session has no recorded usage, such as a forked flow step before its first call, and its local estimate is over the threshold
- **THEN** no compaction triggers, and the first response's usage decides the next check

#### Scenario: Compaction without a summary report

- **WHEN** a compaction stored no output for its summary
- **THEN** the checks before the next model call do not trigger compaction

### Requirement: Compaction checks are logged

The system SHALL log every compaction check at INFO with the session, the agent, the reported tokens, the delta, the total, the threshold and whether it triggers, and a check without a report with the estimate it fell back to.

#### Scenario: Check under the threshold

- **WHEN** a check counts 350,000 reported and 125 delta tokens against a 400,000-token threshold
- **THEN** one INFO line names those numbers and that it does not trigger

### Requirement: An implausible cache read is capped

The system SHALL, when recording a call's usage on a session whose previous report came from a model call (`PromptTokens` above 0), cap the cache-read tokens in the last-call usage at the previous call's `PromptTokens + CompletionTokens`, and SHALL cap them so the recorded prompt does not exceed the model's context window. Each cap SHALL be logged as a warning naming the reported cache read. The cost and the session totals SHALL keep the reported values, and telemetry SHALL be unchanged.

#### Scenario: Doubled cache read

- **WHEN** the previous call's prompt plus output was 691,908 tokens and the next call reports a cache read of 1,383,037
- **THEN** the recorded cache read is 691,908, a warning names the report, and compaction does not trigger on it alone

#### Scenario: Plausible cache read

- **WHEN** a call reports a cache read within the previous call's prompt plus output
- **THEN** the recorded usage is the reported usage

#### Scenario: Prompt above the window

- **WHEN** a call reports a prompt larger than the model's context window
- **THEN** the recorded cache read is lowered until the prompt equals the window

#### Scenario: First call after a compaction

- **WHEN** the call after a compaction reads the system prompt and tools from cache, more than the summary's output
- **THEN** the cache read is recorded as reported
