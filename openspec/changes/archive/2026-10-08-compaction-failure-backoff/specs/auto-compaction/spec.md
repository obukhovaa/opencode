## MODIFIED Requirements

### Requirement: The context count is floored by reported usage

The system SHALL compute the context size for compaction decisions as the maximum of the provider's token estimate and the usage the provider reported for the session's last call (`PromptTokens + CompletionTokens`) plus a local estimate of the messages after the last assistant message. When the history holds no assistant message, the reported usage SHALL be ignored. The reported usage SHALL also be ignored when it exceeds the model's context window or when the floor exceeds 1.5 times the provider estimate; the ignored report SHALL be logged with the reported and estimated values, as a warning when the ignored floor would have reached the threshold and at debug level otherwise. The stored session usage and the telemetry usage SHALL be unchanged. The threshold comparison SHALL use this final count. A context window of 0 SHALL never trigger compaction.

#### Scenario: Undercounting estimate

- **WHEN** the provider estimate is below the session's reported usage plus the tail estimate, and within 1.5 times of it
- **THEN** the reported usage plus the tail estimate is used, and compaction triggers if it reaches the threshold

#### Scenario: After compaction

- **WHEN** the session's reported prompt usage is 0 after a compaction
- **THEN** the floor reflects only the summary size and the provider estimate is used

#### Scenario: Cached prompt tokens are counted once

- **WHEN** a provider reports a prompt of which part was served from cache (Gemini `PromptTokenCount` includes `CachedContentTokenCount`)
- **THEN** the recorded usage counts the cached part once, as cache-read tokens, and the floor equals the real prompt plus output

#### Scenario: Doubled usage report

- **WHEN** the last call's reported usage is about twice the estimate of the same history, and taken as the floor it would reach the threshold
- **THEN** a warning names the ignored report, the estimate is used, and compaction does not trigger on the report alone

#### Scenario: Report within the margin still floors

- **WHEN** the last call's reported usage is above the estimate but within 1.5 times of it
- **THEN** the reported usage plus the tail estimate is used

### Requirement: Summarizer input is bounded

The system SHALL build the summarizer's input from the history starting at the session's current summary message (the prior summary kept as the head), not from the full message log, rendered as a text transcript: each message becomes one text entry with its role kept, tool calls render as `[tool_call <name> id=<id>] <input>`, tool results as `[tool_result <name> id=<id>] <content>`, a server tool search as one line with its query and the tools it found (or its error), a tool result without a name takes its call's, reasoning is dropped, and each tool input or result over 2,000 estimated tokens keeps its head and tail around a marker naming the omitted size. The summarizer request SHALL be one user message holding the transcript followed by the compaction prompt, with no tool, tool-search or reasoning blocks. The budget SHALL be 90% of the summarizer model's context window, lowered to the effective `summarizerMaxInputTokens` when that is set and smaller. When the transcript plus the compaction prompt and the summarizer's system prompt reaches the budget, the system SHALL drop the oldest entries until it fits, SHALL NOT leave a tool result without its tool call at the head of the input, and SHALL log a warning with the number of dropped messages. The size SHALL be the local estimate, with the budget scaled by the session's reported usage when that usage exceeds the local estimate of the same raw history. When the dropped entries would include the turn's prompt (the latest user message that is not synthetic and has text), the system SHALL keep that entry, after the prior summary and before the note that reports the dropped messages, and SHALL count it in the budget, unless it alone exceeds half of the budget left after the prior summary and the fixed parts, or keeping it would leave the input over budget or no entry after the cut. A summarizer context window of 0 and no `summarizerMaxInputTokens` SHALL disable trimming.

#### Scenario: Already-overflowed session recovers

- **WHEN** a session's post-summary history exceeds the summarizer's window
- **THEN** the oldest messages are dropped, the summary is produced, and the next model call starts from the new summary

#### Scenario: The turn's prompt survives a trim

- **WHEN** the oldest messages are dropped and the cut passes the turn's prompt, such as a flow step's task
- **THEN** the summarizer input still carries that prompt, after the prior summary when there is one, followed by the note that reports the dropped messages

#### Scenario: Undercounting estimate is calibrated

- **WHEN** the local estimate of the history fits the summarizer's window but the session's reported usage for the same history does not
- **THEN** the oldest messages are dropped as if the history were the reported size

#### Scenario: Tool-heavy history with deferred tools and thinking

- **WHEN** the history holds tool calls, tool results, server tool-search blocks and signed thinking blocks
- **THEN** the summarizer request is one text-only user message: tool activity appears as tagged lines, the found tools as one line, and no thinking text

#### Scenario: Oversized tool result

- **WHEN** a tool result is larger than 2,000 estimated tokens
- **THEN** the transcript keeps its head and tail with an omission marker in between

## ADDED Requirements

### Requirement: Summarizer input cap is configurable per agent

The system SHALL accept `agents.<name>.summarizerMaxInputTokens` in `.opencode.json` and `summarizerMaxInputTokens` in agent markdown frontmatter, a positive integer of estimated tokens. A negative value SHALL be warned about and ignored. The effective value for a compacting agent SHALL be its frontmatter/registry value, else its `agents.<name>` value, else the `summarizer` agent's value; unset means no cap beyond the window. The field SHALL appear in the generated configuration JSON schema.

#### Scenario: Cap below the summarizer window

- **WHEN** an agent's `summarizerMaxInputTokens` is 300,000 and its summarizer's window is 1,000,000
- **THEN** its compactions send at most about 300,000 estimated tokens, the oldest entries dropped first

#### Scenario: Set on the summarizer

- **WHEN** only `agents.summarizer.summarizerMaxInputTokens` is set
- **THEN** every agent's compaction uses it, and an agent with its own value keeps its own

### Requirement: Failed auto-compactions back off

The system SHALL track consecutive failed compactions per session, in memory. After a failure, automatic compaction SHALL be skipped for the rest of that turn and, in later turns, for 1 minute after the first failure, doubling with each further consecutive failure up to 30 minutes. A successful compaction SHALL reset the count.

#### Scenario: Summarizer keeps failing

- **WHEN** the summarizer fails on the first model call of a turn that makes four model calls
- **THEN** the summarizer is called once in that turn, and not in the next turn inside the backoff

#### Scenario: Wait doubles

- **WHEN** a session's second consecutive compaction fails
- **THEN** no automatic compaction runs for the next 2 minutes

#### Scenario: Backoff expires

- **WHEN** the first failure was 61 seconds ago and a new turn starts over the threshold
- **THEN** the summarizer is tried again, and a second failure sets the wait to 2 minutes

### Requirement: Growth and explicit requests override the backoff

The system SHALL attempt an automatic compaction despite the backoff when the context count has grown by at least 10% of the context window since the last failure, whether or not the failure happened in the current turn. A compaction requested explicitly SHALL ignore the backoff, and its success SHALL reset it.

#### Scenario: Session keeps growing

- **WHEN** a session in backoff grows by 10% of its context window since the last failure
- **THEN** the next check attempts compaction regardless of the wait

#### Scenario: Long turn keeps growing

- **WHEN** a compaction failed earlier in the turn and the tool loop has since grown the context by 10% of the window
- **THEN** the in-loop gate attempts compaction again in that turn

#### Scenario: Manual compaction

- **WHEN** a session in backoff is compacted on request and the summary succeeds
- **THEN** the backoff is cleared

### Requirement: Backoff decisions are logged

The system SHALL log a skipped automatic compaction at debug level, and a failed one as a warning with the agent, the trigger, the failure count, the earliest next attempt, the estimated summarizer input and whether it was likely an overflow of the summarizer's window.

#### Scenario: Failure logged

- **WHEN** an automatic compaction fails
- **THEN** a warning names the failure count, when the next attempt may run, and the estimated input size

### Requirement: Summarizer calls carry diagnostics

The system SHALL attach to the summarizer's telemetry generation the compaction trigger (`pre_turn`, `in_loop` or `manual`), the estimated input tokens, the input message count, the trimmed message count, the truncated tool payload count and the consecutive failure count, without recording the input itself.

#### Scenario: Failed summarizer call

- **WHEN** a summarizer call fails with a stream reset
- **THEN** its generation shows the trigger, the input size and the failure count, and the input is not logged

### Requirement: Summarizer overflow is named

The system SHALL log a warning naming a likely context overflow when a summarizer call fails and its estimated input was at least 90% of the summarizer's context window.

#### Scenario: Summarizer input near its window

- **WHEN** a summarizer call fails with an estimated input at 95% of its window
- **THEN** a warning names the likely overflow and points at `summarizerMaxInputTokens`
