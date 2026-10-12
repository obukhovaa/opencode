## REMOVED Requirements

### Requirement: The context count is floored by reported usage

**Reason**: The provider estimate it starts from is 3.5x low on agentic histories, so its 1.5x plausibility guard discarded every real report and compaction never triggered.
**Migration**: Replaced by "The context count is the last reported usage". The doubled report the guard was for is handled where it is produced, by "A response's summed sampling passes are recorded as its last pass".

## ADDED Requirements

### Requirement: The context count is the last reported usage

The system SHALL count the context size for every auto-compaction decision as the usage reported for the session's last model call (`PromptTokens + CompletionTokens`) plus a local estimate of only the messages after the last message that report covers. It SHALL NOT send a token-count request or estimate the history before that message. The estimate SHALL price text, tool calls and tool results at 4 bytes per token, an image (an attachment or an image tool result) at 1,600 tokens, and any other attachment at 100 bytes per token with 1,600 as the floor. A context window of 0 SHALL never trigger compaction.

#### Scenario: Reported size over the threshold compacts

- **WHEN** the session's last call reported 756,968 tokens, the local estimate of the same history is 214,983, and the effective threshold is 0.4 of a 1,000,000-token window
- **THEN** the count is 756,968 plus the estimate of the messages after the last assistant message, and compaction triggers

#### Scenario: Reported size under the threshold

- **WHEN** the reported usage plus the delta is under the effective threshold
- **THEN** no summarizer call is made

#### Scenario: Cached prompt tokens are counted once

- **WHEN** a provider reports a prompt of which part was served from cache (Gemini `PromptTokenCount` includes `CachedContentTokenCount`)
- **THEN** the recorded usage counts the cached part once, as cache-read tokens, and the count equals the real prompt plus output

#### Scenario: Attachments in the delta

- **WHEN** the incoming user message carries an image and a 400 KB PDF, or a tool result since the report is an image
- **THEN** each image adds 1,600 tokens and the PDF 4,000, not the length of its base64 text and not a fixed overhead

### Requirement: The report's last message is its anchor

The system SHALL take as the last message a report covers the last assistant message that is not synthetic, is finished, and was not finished as an error, or, when the report is a compaction's (`PromptTokens` 0, `CompletionTokens` above 0), the summary message. A call that failed or never finished reports no usage, so the messages before it stay in the delta.

#### Scenario: Anchored on the last assistant message

- **WHEN** the history ends with an assistant message and the tool results of its calls
- **THEN** the tool results are the delta and nothing before them is estimated

#### Scenario: After compaction

- **WHEN** the session's report is a compaction's summary output
- **THEN** the count is that output plus the estimate of the messages after the summary message

#### Scenario: Background-task completion after the report

- **WHEN** synthetic completion messages follow the last reported assistant message
- **THEN** they are counted in the delta

#### Scenario: A call failed after the report

- **WHEN** a call reported its usage, its tool results pushed the context past the window, and the next call failed with a prompt-too-long error
- **THEN** the next turn's count includes those tool results and the failed message, and the session compacts before calling again

#### Scenario: A cancelled call

- **WHEN** the last assistant message was finished as canceled
- **THEN** it is the anchor: a call cancelled during its tools was reported, and a cancel says nothing about the size

### Requirement: A history without a report is counted by the provider

The system SHALL, when no report covers the history (no usage recorded, no message to anchor the report, or a compaction whose summary reported no output), count the history with the provider's token count for that check (the provider's count endpoint, or its local estimate when the endpoint is unavailable, which undercounts) and decide on it.

#### Scenario: Forked flow step over the threshold

- **WHEN** a forked flow step's session carries its parent's whole message log and no usage, and the provider counts it over the threshold
- **THEN** it compacts before its first model call

#### Scenario: No report under the threshold

- **WHEN** a session has no recorded usage and the provider counts it under the threshold
- **THEN** no compaction triggers, and the first response's usage decides the next check

#### Scenario: Provider that reports no usage

- **WHEN** every response of a provider carries zero usage
- **THEN** each check counts the history with the provider, as before reported usage decided

### Requirement: A response's summed sampling passes are recorded as its last pass

The system SHALL, when a response sampled more than once (a server-side tool such as tool search runs inside one response, and its usage sums every pass), record the usage of its last pass as the session's last-call usage: from the response's per-pass `usage.iterations` when the upstream sends them, otherwise the input, cache-creation and cache-read counts divided by the number of passes, with the output whole. The passes SHALL be counted as one plus each run of server tool results the model sampled after. The cost and the session totals SHALL keep the billed, summed counts. A call's cache read SHALL NOT be bounded by the previous call's report.

#### Scenario: Tool search on a cold cache

- **WHEN** a response ran one tool search over a 9,612-token cached prefix and reports cache creation 9,612 and cache read 9,612
- **THEN** the recorded last-call usage counts the prefix once

#### Scenario: Tool search on a warm cache

- **WHEN** the same response reports a cache read of 19,224 and no cache creation
- **THEN** the recorded cache read is 9,612

#### Scenario: A cache read above the previous report

- **WHEN** a retried call reads from cache the prompt its stalled first attempt cached, more than the previous call's prompt plus output
- **THEN** the cache read is recorded as reported

#### Scenario: First call after a compaction

- **WHEN** the call after a compaction reads the system prompt and tools from cache, more than the summary's output
- **THEN** the cache read is recorded as reported

### Requirement: A prompt above the window is recorded at the window

The system SHALL record a last-call prompt larger than the model's context window at the window, lowering the cache read first, then cache creation, then input, and SHALL log a warning naming the reported counts. No single pass can be larger than the window, and counted at the window the session compacts.

#### Scenario: Prompt above the window

- **WHEN** a call reports a prompt larger than the model's context window
- **THEN** the recorded prompt equals the window and the next check compacts

### Requirement: Compaction checks are logged

The system SHALL log every compaction check at INFO with the session, the agent, the reported tokens, the delta, the total, the threshold and whether the count is over it, or, without a report, the provider's count. With auto-compaction disabled the check only sizes `max_tokens` and SHALL log at DEBUG. A compaction the backoff skips SHALL be logged at INFO.

#### Scenario: Check under the threshold

- **WHEN** a check counts 350,000 reported and 125 delta tokens against a 400,000-token threshold
- **THEN** one INFO line names those numbers and that the count is not over the threshold

#### Scenario: Over the threshold and backed off

- **WHEN** a check is over the threshold while a failed compaction's backoff is in force
- **THEN** an INFO line says the compaction was skipped, with the reason and the retry time

### Requirement: The response budget is lowered per call

The system SHALL size each request's `max_tokens` from the context count, halving the configured value until the count plus `max_tokens` fits the window, and SHALL start from the configured value on every call, so that a lowered value does not carry over to the next call of another session.

#### Scenario: A smaller session after a near-full one

- **WHEN** a call near the window lowered `max_tokens` from 64,000 to 32,000, and the next call of the same agent is for a 50,000-token session
- **THEN** that call is sent with `max_tokens` 64,000
