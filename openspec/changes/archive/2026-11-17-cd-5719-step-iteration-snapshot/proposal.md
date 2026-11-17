# Proposal

## Why

CD-5719 needs to distinguish a new Preview step execution from reload/reconnect of the same execution. Stream-only iteration disappears when the orchestrator reconstructs a view, while timestamps and session IDs are insufficient substitutes.

## What Changes

- Expose the persisted step iteration in current and completed /flow/status records.
- Preserve unknown identity as zero for older producers; never invent an iteration from a timestamp.
- Add regression tests and document the additive contract.
- Publish the paired contract changes for review, authorized by Arne via CoS mailbox 20261116T090430Z on 2026-11-16. Deployment remains a separate gate.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `flow-api`: Carry the existing persisted iteration across status recovery.

## Impact

Expose the persisted step iteration in current and completed /flow/status records. No execution lifecycle, prompt, tool grant, schema configuration, database or approval behavior changes. The paired producer/consumer drafts must be reviewed together. This exposes a prerequisite; it does not implement the Preview-ready receipt or invitation.
