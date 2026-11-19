## 1. Storage and consumers

- [x] 1.1 Add project-scoped SQLite/MySQL page queries, indexes, cursors and bounded walkers.
- [x] 1.2 Move TUI, bridge, HTTP, ACP, status and auto-approve consumers to bounded reads.
- [x] 1.3 Batch cleanup through coordinated deletion and protect active conversations/runs.
- [x] 1.4 Document connector continuation and the API/storage contracts.

## 2. Review and verification

- [x] 2.1 Review in a separate subagent and fix JSON casing, pinned metadata and queued-control ordering.
- [x] 2.2 Add real serialized-flow and deterministic TUI regressions alongside provider/connector tests.
- [x] 2.3 Run final make test, MySQL integration, schema/Unicode checks and connector E2E checks.
- [x] 2.4 Verify the main spec matches the delta and prepare the completed archive record.
