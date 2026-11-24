# Account for each task run once

## Why

Resuming a task reused its child session and credited the child's lifetime cost to the caller a second time. Parallel task completions could also overwrite each other's additions when each saved a stale copy of the parent session.

## What Changes

- Capture the child's stored cost at the start of each run. Credit the difference at completion, including usage recorded during post-turn compaction.
- Apply parent cost increments, model usage and compaction accounting atomically on SQLite and MySQL. Ordinary session saves preserve the stored cost.
- Document the existing inclusive meaning of flow step cost.

## Capabilities

### New Capabilities

- `subagent-cost-rollup` defines cost attribution and concurrent session accounting.

### Modified Capabilities

- `task-async-mode` applies the same accounting rules before delivering the completion.

## Impact

The change covers the task tool, the session storage service and flow documentation. Existing databases and flow result consumers use their current schemas. Previously stored costs keep their recorded values.
