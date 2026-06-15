## ADDED Requirements

### Requirement: Bridge suppresses tool-update indicators for synthetic messages
The bridge's per-session tool-update indicator emission path (the 🔧 tool icons that render in Slack/Telegram/Mattermost as tool calls happen) SHALL skip any Assistant message whose `synthetic` flag is `true`. Synthetic Assistant messages produced by `task.EnqueueTaskCompletion` (background bash completions, async task completions, monitor events, cron-fired completions) MUST NOT trigger any outbound chat indicator activity.

This requirement covers ALL synthetic message sources uniformly — the bridge filter is keyed off the `synthetic` flag on the message, not off the originating tool name (`bash`/`task`/`monitor`).

#### Scenario: Cron-fired completion does not emit a tool indicator
- **WHEN** a cron job fires and `EnqueueTaskCompletion` writes a synthetic Assistant(ToolCall name=task) + Tool(ToolResult) pair
- **THEN** the bridge's parts demux observes the Assistant message, sees `synthetic = true`, and does NOT emit a 🔧 task indicator to the bound chat platform; the next REAL assistant message (the agent's reply to the synthetic ToolResult) DOES fan out to chat as a normal text reply

#### Scenario: Background bash completion does not emit a tool indicator
- **WHEN** a background bash subprocess exits and `EnqueueTaskCompletion` writes a synthetic pair
- **THEN** the bridge does NOT emit a 🔧 bash indicator; the agent's human-readable reaction to the completion DOES flow to chat

#### Scenario: Monitor events do not emit per-event indicators
- **WHEN** a monitor task fires a `monitor-event` notification via `EnqueueTaskCompletion`
- **THEN** the bridge does NOT emit a 🔧 monitor indicator for the synthetic pair; the agent's reaction (if any) flows to chat normally

#### Scenario: Real (non-synthetic) tool calls still emit indicators
- **WHEN** the agent invokes a real tool call (e.g., a synchronous bash, a read, a grep) — Assistant message has `synthetic = false`
- **THEN** the bridge emits the appropriate 🔧 indicator as it does today; no behavior change for non-synthetic messages

### Requirement: Bridge filter reads `synthetic` from the persisted message
The bridge's filter check SHALL read the `synthetic` flag from the same `message.Message` struct that the bridge already receives in its parts subscription. The bridge MUST NOT make any additional DB read solely to determine the flag; the `synthetic` column MUST be hydrated into the struct by the existing read path (sqlc-generated bindings updated by the migration).

#### Scenario: Single-read filter path
- **WHEN** a synthetic message arrives in the bridge's parts demux
- **THEN** the filter check is a struct-field read (e.g., `if msg.Synthetic { continue }`) — no extra query, no async lookup

### Requirement: Synthetic suppression does not affect transcript visibility
Synthetic Assistant messages SHALL remain visible in the session's message log and SHALL be returned by all existing `/session/<id>/messages` read endpoints. Only the bridge's OUTBOUND fan-out to chat platforms is filtered.

#### Scenario: HTTP message read returns synthetic
- **WHEN** an HTTP client GETs `/session/<id>/messages` for a session that has had background task completions
- **THEN** the synthetic Assistant and Tool messages appear in the response with `synthetic: true` exposed; HTTP consumers can choose to filter or include them
