## Why

A named workflow started through a chat agent can fail at its first interactive step because its `${args.reviewer}` argument is normally supplied by the orchestrator. The runtime already knows and persists the authorized originating conversation, so chat execution should not depend on orchestrator argument enrichment.

## What Changes

- Supply absent interaction target arguments from the authenticated originating conversation for named flow-tool invocations.
- Save effective arguments and use the recorded origin when recovering older runs that lack those arguments.
- Preserve explicit target validation, thread isolation, immutable source definitions, and standalone behavior.
- Verify the interactive question/answer round trip and recovery without real vendor messages or external model dependencies.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `bridge-flow-sessions`: Named interactive flows inherit missing routing context from their conversation.

## Impact

OpenCode conversation-flow admission and execution, regression tests and bridge documentation. No new configuration, database migration, tool schema or orchestrator service is needed.
