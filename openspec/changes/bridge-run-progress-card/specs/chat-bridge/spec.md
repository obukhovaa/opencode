# chat-bridge (delta)

Delta spec for the `bridge-run-progress-card` change. Restates only the requirements
that change; unchanged requirements are not repeated. For the full specification see
`openspec/specs/chat-bridge/spec.md`.

## MODIFIED Requirements

### Requirement: Per-session typing/reporting indicators

The bridge SHALL emit platform-appropriate typing indicators while a run is in flight for a session, and SHALL surface tool-call activity to the chat surface when `cfg.Router.ToolUpdatesEnabled` is true — as one progress card per run at `compact` verbosity, or as one card per tool call at `full`. Indicator emission MUST NOT block the inbound dispatch loop.

#### Scenario: Tool updates enabled at compact

- **WHEN** `cfg.Router.ToolUpdatesEnabled == true`, the live verbosity is `compact`, and a tool call completes
- **THEN** the run's progress card is updated in place to reflect the new completion count; no separate message is posted for the call

#### Scenario: Tool updates enabled at full

- **WHEN** `cfg.Router.ToolUpdatesEnabled == true`, the live verbosity is `full`, and a tool call completes
- **THEN** the bridge sends a per-call status card reflecting the transition, carrying the argument summary and a rune-capped result body

#### Scenario: Tool updates disabled

- **WHEN** `cfg.Router.ToolUpdatesEnabled == false`
- **THEN** the bridge posts no progress card and no per-call cards, but still emits typing indicators, the final agent reply, and a one-line reason for any failed tool call

### Requirement: Compact tool updates are one progress card per run, updated in place

A tool update is a PROGRESS INDICATOR, not a transcript. In the default `compact` verbosity, when `cfg.Router.ToolUpdatesEnabled` is true, the bridge SHALL post exactly one progress message per agent run and SHALL update that message in place as the run proceeds. It SHALL NOT post a message per tool call.

The card SHALL be posted when the run starts, reading `Thinking...`. Each subsequent update SHALL carry the real number of tool calls completed so far in the run (`5 tool calls done`, `8 tool calls done`, …), the elapsed time since the card was created, and, when a call is known to be in flight, the name of the most recently started unfinished tool. When one or more calls have failed the card SHALL also carry the failure count and the most recent failure's reason, flattened to one line and rune-capped. When the run ends the card SHALL be updated one final time to a terminal state that names the total tool-call count and elapsed time, distinguishing a run that ended normally from one whose agent errored.

In `compact` the bridge SHALL NOT send tool ARGUMENTS or successful result BODIES to the chat surface; both are durably recorded in the session store (`messages.parts`) and in the telemetry backend, which is where investigation belongs.

Card updates SHALL be serialised so that a later count never overwrites an earlier one out of order, and SHALL be paced: completions arriving within the pacing interval collapse into one update carrying the latest counts. In-place editing SHALL use the platform's native edit call (Slack `chat.update`, Telegram `editMessageText`, Mattermost post update) through the optional `MessageEditor` adapter capability. If an edit fails the bridge SHALL post the card fresh to that peer and continue editing the new message.

The card SHALL be created only if the live verbosity is `compact` when the run starts; once created it SHALL track every completion of that run regardless of later verbosity switches.

`cfg.Router.ToolUpdateVerbosity` selects the level: `compact` (default) or `full`, where `full` posts one card per tool call carrying the argument summary and a rune-capped result body. The values `verbose` and `debug` SHALL be accepted as aliases of `full`. An absent or unrecognised value SHALL resolve to `compact` — the quiet option is the fail-safe — and an unrecognised value SHALL be logged once at WARN.

#### Scenario: Run start posts the card

- **WHEN** a bound peer's message starts an agent run with `toolUpdatesEnabled: true` at `compact`
- **THEN** one message reading `⏳ Thinking...` is posted to every bound peer whose adapter implements `MessageEditor`, before any tool call completes

#### Scenario: Successful multi-call run updates one message

- **WHEN** the run makes eight tool calls that all succeed and then ends normally
- **THEN** the chat surface shows one progress message, updated in place through states such as `⏳ 5 tool calls done · 1m12s` and ending as `✓ Done · 8 tool calls · 3m40s`; no per-call message exists, and neither arguments nor result bodies appear in chat

#### Scenario: Failed call folds into the card

- **WHEN** the third call of the run fails with a multi-line error body
- **THEN** the card shows `1 failed` and a second line `✗ <tool>#<id> · <reason>` with the body flattened to one line and truncated to the failure-reason cap; no separate failure message is posted to peers whose adapter edits in place

#### Scenario: Burst of completions is coalesced

- **WHEN** ten tool calls complete within one pacing interval
- **THEN** the card receives fewer than ten edits and the last edit reads `10 tool calls done`

#### Scenario: Agent error ends the card as failed

- **WHEN** the agent run terminates with an error event after four tool calls
- **THEN** the card's final state reads `✗ Run failed · 4 tool calls · <elapsed>`

#### Scenario: Unset verbosity resolves to compact

- **WHEN** `.opencode.json` sets `router.toolUpdatesEnabled: true` and omits `router.toolUpdateVerbosity` (or sets it to an unknown value such as `"chatty"`)
- **THEN** the bridge renders the progress card; the unknown value additionally produces one WARN naming the configured value and the mode actually used

#### Scenario: Verbose and debug are aliases of full

- **WHEN** `router.toolUpdateVerbosity` is `"verbose"` or `"debug"`
- **THEN** the bridge renders per-call cards with arguments and result bodies, and reports the live level as `full`

### Requirement: Reviewers can switch tool-update verbosity at runtime

The bridge SHALL expose a `/verbosity` chat command that reports the live verbosity and switches it between `compact` and `full`, accepting `verbose` and `debug` as aliases of `full`. The switch SHALL take effect for every bound session without a restart, and SHALL NOT be written back to `.opencode.json` — a reviewer enabling detail to watch one run MUST NOT silently reconfigure the deployment. A restart therefore returns to the configured value. An unknown mode SHALL be rejected with a usage reply and leave the live value unchanged.

#### Scenario: Reviewer asks for detail mid-run

- **WHEN** a reviewer sends `/verbosity full` on a bound peer while a run is in flight
- **THEN** the bridge replies with the applied mode, and every subsequent tool call in that process — including on other bound sessions — renders a per-call card with argument and result detail until the mode is switched back or the process restarts; a progress card already open for the run keeps counting to its terminal state

#### Scenario: Reviewer lists the modes

- **WHEN** a reviewer sends `/verbosity` with no argument
- **THEN** the bridge lists `compact` and `full` with one-line descriptions and marks the live one active

#### Scenario: Unknown mode is rejected

- **WHEN** a reviewer sends `/verbosity chatty`
- **THEN** the bridge replies with the accepted values and the live verbosity is unchanged

## ADDED Requirements

### Requirement: Optional MessageEditor per-adapter capability

An adapter MAY implement `MessageEditor`: `SendEditable` posts a plain-text message and returns an opaque token; `EditMessage` replaces that message's text in place. The three production adapters (Slack, Telegram, Mattermost) SHALL implement it, and their queued-acknowledgement methods SHALL be built on it so each platform has one edit path. The `external` relay adapter does not implement it.

A peer whose adapter does not implement `MessageEditor` SHALL receive nothing for the run's progress card, except that a tool failure no earlier update has delivered SHALL reach it once as the failure's one-line reason in plain text, so a failed call surfaces on text-only peers exactly as before.

#### Scenario: Relay channel receives no progress frames

- **WHEN** a session is bound to a Slack peer and an `external` relay peer, and its run's progress card is updated
- **THEN** the Slack message is edited in place and the relay receives no frame for the update

#### Scenario: Relay channel still receives failures

- **WHEN** the same run's tool call fails
- **THEN** the relay receives one text frame carrying `✗ <tool>#<id> · <reason>`, and the Slack card is edited to show the failure

#### Scenario: Edit failure recovers with a fresh post

- **WHEN** the card's message was deleted and the next update's edit fails
- **THEN** the bridge posts the card as a new message to that peer and edits that message from then on
