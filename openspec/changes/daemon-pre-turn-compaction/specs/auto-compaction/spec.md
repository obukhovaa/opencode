# auto-compaction (delta)

Delta spec for the `daemon-pre-turn-compaction` change. This capability is new, so every requirement below is added.

## Purpose

Defines when a session's history is compacted automatically, how the context size is counted for that decision, which threshold applies, what the summarizer is given, and how a call that failed near the window is reported.

## ADDED Requirements

### Requirement: A turn is checked before its first model call

The system SHALL, when `autoCompact` is enabled, count a session's history plus the incoming user message before the user message is persisted and before the turn's first model call, and SHALL compact the session synchronously when the count reaches the effective threshold. When it compacts, the system SHALL persist the user message after the summary message. The check SHALL apply to every agent run, regardless of entry point (chat bridge, daemon, ACP, CLI, TUI, flow step). A compaction failure SHALL be logged as a warning and the turn SHALL continue.

#### Scenario: Session over the threshold compacts before the first call

- **WHEN** a session whose history is over the effective threshold receives a new user message
- **THEN** the summarizer runs before the agent's first model call of that turn
- **AND** the first model request starts with the summary
- **AND** the persisted user message has a later sequence number than the summary message

#### Scenario: Session under the threshold is not compacted

- **WHEN** a session's history plus the incoming user message is under the effective threshold
- **THEN** no summarizer call is made before the turn's first model call

#### Scenario: Auto-resume turn

- **WHEN** a turn has no user content and no attachments (auto-resume)
- **THEN** only the existing history is counted, and no user message is persisted

#### Scenario: Cycle one is not compacted twice

- **WHEN** the pre-turn check compacts the session
- **THEN** the in-loop check does not compact again before the first model call of the same cycle

### Requirement: The context count is floored by reported usage

The system SHALL compute the context size for compaction decisions as the maximum of the provider's token estimate and the usage the provider reported for the session's last call (`PromptTokens + CompletionTokens`) plus a local estimate of the messages after the last assistant message. When the history holds no assistant message, the reported usage SHALL be ignored. The threshold comparison SHALL use this final count. A context window of 0 SHALL never trigger compaction.

#### Scenario: Undercounting estimate

- **WHEN** the provider estimate is below the session's reported usage plus the tail estimate
- **THEN** the reported usage plus the tail estimate is used, and compaction triggers if it reaches the threshold

#### Scenario: After compaction

- **WHEN** the session's reported prompt usage is 0 after a compaction
- **THEN** the floor reflects only the summary size and the provider estimate is used

### Requirement: Compaction threshold is configurable per agent

The system SHALL accept `agents.<name>.compactionThreshold` in `.opencode.json` and `compactionThreshold` in agent markdown frontmatter, a number in (0, 1]. A value outside that range SHALL be warned about and ignored. The effective threshold SHALL be: the flow step's `compact.threshold` when set, else the agent's `compactionThreshold` when set, else 0.95. The per-agent value SHALL NOT enable compaction when `autoCompact` is off. The field SHALL appear in the generated configuration JSON schema.

#### Scenario: Agent threshold applies to daemon turns

- **WHEN** `agents.neo.compactionThreshold` is 0.4 and a run carries no flow-step threshold
- **THEN** compaction triggers at 40% of the model's context window

#### Scenario: Flow step wins

- **WHEN** the agent's `compactionThreshold` is 0.4 and the flow step's `compact.threshold` is 0.7
- **THEN** the effective threshold is 0.7

#### Scenario: Invalid value

- **WHEN** the agent's `compactionThreshold` is outside (0, 1]
- **THEN** a warning is logged and the default 0.95 applies

### Requirement: Summarizer input is bounded

The system SHALL build the summarizer's input from the history starting at the session's current summary message (the prior summary kept as the head), not from the full message log. When that input plus the compaction prompt and the summarizer's system prompt reaches 90% of the summarizer model's context window, the system SHALL drop the oldest messages until it fits, SHALL NOT leave a tool result without its tool call at the head of the input, and SHALL log a warning with the number of dropped messages. A summarizer context window of 0 SHALL disable trimming.

#### Scenario: Already-overflowed session recovers

- **WHEN** a session's post-summary history exceeds the summarizer's window
- **THEN** the oldest messages are dropped, the summary is produced, and the next model call starts from the new summary

### Requirement: Call failures near the window are diagnosable

The system SHALL, when a model call fails for a reason other than cancellation and the last context count was at least 90% of the model's context window, log a warning naming a likely context overflow, with the session, agent, estimated tokens, context window, ratio and error.

#### Scenario: Stream reset on a near-full context

- **WHEN** a model call fails with a stream error and the last estimate was 97% of the window
- **THEN** a warning names the likely context overflow and suggests compacting or resetting the session
