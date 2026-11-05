## Why

Free-form bridge sessions should be able to delegate work to a flow while retaining their conversation, questions and progress. The current controller can launch from a bridge agent's tool call, but its lifetime and routing remain TUI-oriented; it also cannot run an existing YAML definition through that tool.

## What Changes

- Extend the primary-agent `flow` tool with `plan` for a goal-driven generated workflow and `run` for a named, existing YAML definition with supplied arguments; retain compatible `create`/`update` definition submission.
- Support direct Slack/Telegram/Mattermost conversations and external mediated conversations with progress, final results, questions, steering and controls spanning the whole run.
- Execute a validated snapshot of an existing definition without changing its source or permitting tool-based amendments to that run. Dynamic definitions retain safe downstream amendments.
- Store run definitions and lifecycle in the configured session backend so MySQL workers can recover without the original machine's profile files; add fenced ownership and durable external delivery identity.
- Keep flow invocation top-level: no flow step or subagent may create or run another flow. Nested flows and subprocess-based nesting are explicitly discarded.
- Separate delivery into Wave A (direct bridge and named YAML execution) and Wave B (external-worker migration, ownership and lifecycle protocol). This proposal does not implement either wave.

## Capabilities

### New Capabilities

- `bridge-flow-sessions`: conversation ownership, questions, steering, progress and controls for bridge-initiated flows.
- `session-named-flows`: execute existing immutable YAML definitions from ordinary sessions through the same flow tool.
- `dynamic-flow-ownership`: portable run definitions, recovery, delivery receipts and ownership across workers.

### Modified Capabilities

None. These additions extend the existing `dynamic-session-flows` contract without changing standalone YAML semantics or existing TUI controls.

## Impact

Flow tool and agent/schema registration, app controller, session migrations/sqlc, flow runner, bridge dispatcher/question routing, external relay protocol, API/SSE events and pool lifecycle. Tests must cover SQLite and MySQL plus direct and mediated bridge adapters. External orchestrator coordination is a cross-repository integration dependency, not something this repository alone can claim to deliver.
