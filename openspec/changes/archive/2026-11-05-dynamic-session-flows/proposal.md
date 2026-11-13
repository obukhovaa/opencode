## Why

Users need durable workflows inside ordinary TUI sessions, with progress and intervention, without losing standalone flow compatibility.

## What Changes

- Add opt-in primary-agent dynamicFlow configuration and a flow tool.
- Generate validated, bounded flows using bundled flow-creator guidance and OpenSpec.
- Persist definitions and session association, reuse existing flow execution and recovery.
- Add TUI flow progress, boundary steering, cancellation and resume; hide internal step sessions.

## Capabilities

### New Capabilities
- `dynamic-session-flows`: Session-owned bounded workflows with durable definitions and TUI controls.

### Modified Capabilities

None. Existing standalone flow contracts remain unchanged.

## Impact

Agent configuration/schema, registry, tool registration, flow service, app coordinator, TUI and session lists. OpenSpec CLI is required for dynamic planning.
