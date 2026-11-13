## Why
Long-running chats accumulate rendering work, while background tasks are difficult to discover and stop. Dedicated activity views make ongoing work visible without consuming chat width.

## What Changes
- Keep typing responsive as tool history grows.
- Add /tasks and /subagents activity views with details and cancellation.
- Show session activity counts above the editor with keyboard access.

## Capabilities
### New Capabilities
- `tui-activities`: Session-scoped task discovery, inspection and cancellation with responsive chat rendering.
### Modified Capabilities
None.

## Impact
TUI chat rendering, command registration and pages; existing task registry and persisted child sessions are reused. No background-task lifecycle changes.
