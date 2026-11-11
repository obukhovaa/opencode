# Proposal

## Why

Identical tool arguments can represent useful progress: a question may be answered with new messages, or a read may return changed content. Counting arguments alone interrupts legitimate interactive conversations.

## What Changes

- Count repetitions of completed calls only when both arguments and results match.
- Clear repetition history after inbox input is successfully stored for the next model turn.
- Preserve stalled-call protection, forced terminal tools, permission-denial limits and turn budgets.

## Capabilities

### New Capabilities
- `tool-loop-detection`: Detect repeated tool calls without mistaking changed results or delivered user input for a stalled loop.

### Modified Capabilities

None.

## Impact

Agent call tracking and execution, inbox delivery boundaries, regression tests, and configuration documentation. No configuration or schema changes.
