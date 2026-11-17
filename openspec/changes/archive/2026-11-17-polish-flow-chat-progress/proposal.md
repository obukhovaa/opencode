## Why

Conversation flows currently send a plain message for every step transition and expose raw JSON in chat. Long runs bury the conversation and ignore its existing verbosity setting.

## What Changes

- Maintain one editable, recoverable progress message per flow and recipient for native Slack, Mattermost and Telegram bridges.
- Show ordered step status, active work, elapsed duration and clear terminal outcomes.
- Use Slack plan/task cards, Mattermost collapsible blocks with readable fallback text, and Telegram expandable quotations for bounded details.
- Format structured output as readable JSON; compact mode omits routine output and full mode folds details. Honor disabled progress while keeping failures and completion visible.
- Preserve external relay events, delivery fencing, retries, question routing and standalone flow behavior.

## Capabilities

### New Capabilities
- `bridge-flow-progress`: Durable, platform-aware flow progress presentation with verbosity controls.

### Modified Capabilities
None.

## Impact

Bridge service, native adapters, flow-event presentation metadata and SQLite/MySQL delivery state. No new configuration, tool schemas or LLM prompt changes. Patch release; external relay protocol remains compatible.
