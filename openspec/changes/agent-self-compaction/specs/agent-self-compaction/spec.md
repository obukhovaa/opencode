## ADDED Requirements

### Requirement: Self-compaction is configured per agent

The system SHALL accept `agents.<name>.selfCompact` in `.opencode.json` and `selfCompact` in agent markdown frontmatter, an object with `enabled` (boolean) and `minTokens` (integer). A configuration value SHALL replace a frontmatter value, and an unset one SHALL keep it. A negative `minTokens` SHALL be warned about and read as no floor. The field SHALL appear in the generated configuration JSON schema.

#### Scenario: Set in frontmatter

- **WHEN** a markdown-defined agent sets `selfCompact: {enabled: true, minTokens: 150000}` in its frontmatter and has no JSON entry
- **THEN** the agent's self-compaction is enabled with a floor of 150000 tokens

#### Scenario: Negative floor

- **WHEN** `minTokens` is negative
- **THEN** a warning is logged and no floor applies

### Requirement: The tool is registered only when enabled

The system SHALL register the `compact_session` tool only for a primary agent whose `selfCompact.enabled` is true and whose tool settings do not exclude `compact_session`.

#### Scenario: Enabled primary agent

- **WHEN** a primary agent has `selfCompact.enabled` true
- **THEN** the agent has the `compact_session` tool

#### Scenario: Not enabled

- **WHEN** an agent has no `selfCompact`, or `enabled` is false
- **THEN** the agent has no `compact_session` tool

#### Scenario: Subagent

- **WHEN** a subagent sets `selfCompact.enabled`
- **THEN** the tool is not registered and a warning is logged

### Requirement: The tool records a request and does not compact

The `compact_session` tool SHALL take an optional `keep` string and SHALL count the session's context with the same count every auto-compaction decision uses. When the count is below the agent's `minTokens`, it SHALL return `skipped: <n> tokens < floor <m>` and record nothing. Otherwise it SHALL record a pending compaction request carrying the count and the `keep` text, durably with the session's history, and SHALL return the count. It SHALL NOT compact the session during the turn that calls it.

#### Scenario: Below the floor

- **WHEN** the agent calls `compact_session` while its session counts 90000 tokens and its floor is 150000
- **THEN** the tool returns `skipped: 90000 tokens < floor 150000`
- **AND** no compaction runs after the turn or before the next one

#### Scenario: Above the floor

- **WHEN** the agent calls `compact_session` with a `keep` text while its session is above the floor
- **THEN** the tool returns the context size and a request is recorded
- **AND** no summarizer call is made before the turn ends

### Requirement: A request is served right after the turn

The system SHALL serve a pending request after the turn that recorded it has ended and its terminal event has been sent, before the run is reported finished, while the session stays reserved for that run. When the caller marked the run to have its terminal event delivered first, the compaction SHALL wait for that signal, bounded. This SHALL hold for every entry point but a flow step. A turn that ended in an error or was cancelled SHALL leave the request pending.

#### Scenario: Reply first, then compaction

- **WHEN** a chat-bridge turn ends after the agent recorded a request
- **THEN** the turn's reply is posted to the chat before the summarizer is called
- **AND** the next inbound message for that session starts its run only after the compaction is done

#### Scenario: Failed turn

- **WHEN** a turn records a request and then ends in an error
- **THEN** no compaction runs after it, and the request stays pending

### Requirement: Only the agent ends a post-turn compaction

Once the turn's terminal event is sent, the post-turn compaction SHALL NOT be ended by the caller's context. Only cancelling the agent's session, or a bound on the compaction's duration, SHALL end it.

#### Scenario: Caller stops listening

- **WHEN** a caller cancels its context as soon as it has the turn's terminal event
- **THEN** the compaction still runs to completion

#### Scenario: Agent cancelled

- **WHEN** the agent's session is cancelled while the post-turn compaction waits or runs
- **THEN** the compaction stops, the request stays pending, and no failure is recorded in the backoff

### Requirement: A flow step's request waits for the next turn

A flow step's run SHALL NOT hold its session for a post-turn compaction, since the flow runner reuses the session as soon as it has the terminal event. Its request SHALL be left pending for the session's next turn.

#### Scenario: Flow step

- **WHEN** a flow step's turn records a request
- **THEN** no compaction runs after the turn and the session is released at once
- **AND** the step's next turn on that session compacts before its first model call

### Requirement: Background completions survive the post-turn compaction

A background task completion written while the session is held for a self-compaction SHALL remain in the history after the summary, its tool call and result kept together, and the session SHALL be auto-resumed for it once it is released, as it would have been had the completion found the session idle.

#### Scenario: Completion during the compaction

- **WHEN** a background task finishes while the session is held for a post-turn compaction
- **THEN** its completion appears after the summary in the history every later turn loads
- **AND** the session is auto-resumed once, after it is released, with the task's requester and job args

### Requirement: A pending request is served before the next turn

When a turn starts with a pending request, the system SHALL compact the session before the turn's first model call and before the turn's user message is persisted, keeping an auto-resume turn's trailing synthetic completion messages after the summary. This SHALL not depend on `autoCompact`. A pending request SHALL survive a process restart.

#### Scenario: Request survives a restart

- **WHEN** the process stopped after a turn recorded a request and before it was served, and a new message arrives after the restart
- **THEN** the session is compacted before the turn's first model call
- **AND** the first model request starts with the summary and the new user message is persisted after it

### Requirement: The keep notes reach the summarizer

Every compaction SHALL append the `keep` texts of all pending requests it serves to the compaction prompt, in the order they were recorded and without exact duplicates, with an instruction to carry them forward verbatim. This SHALL apply whatever started the compaction.

#### Scenario: Notes in the prompt

- **WHEN** a request with `keep` "open: review the change" is served
- **THEN** the summarizer's prompt ends with an instruction to carry the notes forward verbatim followed by "open: review the change"

#### Scenario: Another compaction serves the request

- **WHEN** an auto-compaction at the threshold runs while a request is pending
- **THEN** its summarizer prompt carries the request's notes, and the request is no longer pending afterwards

### Requirement: A session is never compacted twice for its requests

A compaction SHALL serve every request recorded before it, and a served request SHALL no longer be pending. While a request is being served, no other run, synchronous compaction or asynchronous summarization of the session SHALL start.

#### Scenario: Two requests in one turn

- **WHEN** the agent calls `compact_session` twice in one turn
- **THEN** exactly one compaction runs after the turn, carrying both notes
- **AND** the next turn starts without a compaction

#### Scenario: Concurrent entry points

- **WHEN** a run, a synchronous compaction or a summarization of the session is requested while a request is being served
- **THEN** it is refused as busy

### Requirement: Self-compaction honours the failure backoff

A self-compaction SHALL be subject to the per-session compaction failure backoff, and its outcome SHALL be recorded there, since a pending request is retried without the agent asking again. A compaction cancelled through its context SHALL NOT be recorded as a failure.

#### Scenario: Summarizer failing

- **WHEN** a self-compaction fails
- **THEN** the request stays pending, the failure is recorded, and the next attempt waits for the backoff

### Requirement: Self-compactions are distinguishable

The summarizer trace of a self-compaction SHALL be named after the agent (or agent, flow and step) followed by `/self-compaction`, its input SHALL be `self-compaction requested by agent over <n> messages` where generation input is logged, and its generation's `compaction.trigger` SHALL be `self_after_turn` or `self_pre_turn`. The system SHALL log at INFO when a self-compaction starts and when it completes, with the context size before and, on completion, after.

#### Scenario: Trace and log

- **WHEN** a request is served after a turn
- **THEN** the summarizer trace is named `<agent>/self-compaction` with trigger `self_after_turn`
- **AND** an INFO line names the context size before and after
