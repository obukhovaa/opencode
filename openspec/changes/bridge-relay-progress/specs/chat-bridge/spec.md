## REMOVED Requirements

### Requirement: Optional MessageEditor per-adapter capability

**Reason**: Its scenario "Relay channel receives no progress frames" no longer holds — the relay now receives the card's state in structured form.
**Migration**: Replaced by "Optional MessageEditor and ProgressRelayer per-adapter capabilities", which keeps the `MessageEditor` contract, the failure-line rule and the edit-recovery scenario unchanged.

## ADDED Requirements

### Requirement: Optional MessageEditor and ProgressRelayer per-adapter capabilities

An adapter MAY implement `MessageEditor`: `SendEditable` posts a plain-text message and returns an opaque token; `EditMessage` replaces that message's text in place. The three production adapters (Slack, Telegram, Mattermost) SHALL implement it, and their queued-acknowledgement methods SHALL be built on it so each platform has one edit path. The `external` relay adapter does not implement it.

A peer whose adapter does not implement `MessageEditor` SHALL receive no rendered progress card. A tool failure no earlier update has delivered SHALL reach it once as the failure's one-line reason in plain text, unless the peer's adapter accepted that update as a `RunProgress`, which carries the failure.

#### Scenario: Relay channel receives progress state, not the card

- **WHEN** a session is bound to a Slack peer and an `external` relay peer, and its run's progress card is updated
- **THEN** the Slack message is edited in place and the relay receives one `progress` frame for the update and no text frame

#### Scenario: Relay channel receives failures in the progress state

- **WHEN** the same run's tool call fails
- **THEN** the relay receives a `progress` frame whose failure count and last failure name it, and no text frame; the Slack card is edited to show the failure

#### Scenario: Rejected progress falls back to the failure line

- **WHEN** the relay rejects the progress update that carries a new failure
- **THEN** the relay receives one text frame carrying `✗ <tool>#<id> · <reason>` for it

#### Scenario: Edit failure recovers with a fresh post

- **WHEN** the card's message was deleted and the next update's edit fails
- **THEN** the bridge posts the card as a new message to that peer and edits that message from then on

### Requirement: Relay peers receive structured run progress

The bridge SHALL hand every state it renders for a run's progress card, as a `RunProgress`, to each bound peer whose adapter implements `ProgressRelayer` and not `MessageEditor`, with the session id on the context and with the card's own pacing, coalescing and ordering. The first state SHALL be the `Thinking...` state, and exactly one terminal state SHALL be sent. A failed `SendProgress` SHALL be logged and not retried; the next state supersedes it.

#### Scenario: A run's states reach the relay

- **WHEN** a run bound to an `external` peer starts at `compact`, calls `read`, then `bash`, which fails, and ends normally
- **THEN** the relay receives progress states carrying the session id: the first `running` with nothing done, one with `read` in flight, and exactly one terminal state `ok` with two calls done, one failed, and `bash` as the last failed tool

#### Scenario: No card, no progress

- **WHEN** tool updates are disabled, or the run started at `full` verbosity
- **THEN** the relay receives no progress state

### Requirement: Relay progress frame

The `external` adapter SHALL implement `ProgressRelayer` by POSTing a relay frame with `kind: "progress"` and a `progress` object carrying `status` (`running`, `ok` or `error`), `toolsDone`, `failed`, `running`, `currentTool`, `lastFailedTool`, `lastFailureReason` and `elapsedMs`, every key always present, and no `text`. A non-202 response SHALL be a failed send, recorded on the adapter's status like any other.

#### Scenario: Frame shape

- **WHEN** the adapter relays a running state with two calls done and `grep` in flight after 72 seconds
- **THEN** the POSTed frame has `kind: "progress"`, the session and job ids, and `progress` equal to `{status: "running", toolsDone: 2, failed: 0, running: 1, currentTool: "grep", lastFailedTool: "", lastFailureReason: "", elapsedMs: 72000}`
