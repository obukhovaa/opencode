## Context

See proposal.md for the production trigger. Both SQLite and MySQL must preserve project scoping, external connector isolation and coordinated tree deletion. The existing TUI uses value receivers, so background work must capture stable application references rather than a model pointer.

## Goals / Non-Goals

Goals: bound interactive memory and response size, keep terminal input responsive, preserve privacy before truncation, and allow complete traversal when explicitly needed. This change does not change retention configuration or automatically prune production data.

## Decisions

- Use creation-time/ID keyset cursors and a composite index instead of OFFSET. Deleting the cursor row or tied timestamps cannot repeat the same page. Scope hashes bind cursors to filters/project; authorization is always recomputed by the query.
- Apply connector visibility in SQL before LIMIT instead of filtering a returned page. External callers are restricted to the current binding; failed visibility reads cannot widen that boundary.
- Read 50 TUI roots asynchronously and 10 bridge rows, with a 200-row service cap and five-second read deadlines. Pages replace rows. Control generations, query revisions and an in-flight navigation guard prevent stale actions; database replies have separate monotonic request IDs.
- Read flow labels from stored metadata without restoring/importing snapshots. Tests serialize the actual persisted flow structs and exercise the pinned current root.
- Keep HTTP array and status-map shapes; expose continuation through headers and use existing ACP cursor fields. Busy status enumerates process-active slots rather than history.
- Count cleanup trees in SQL and walk bounded candidate batches through the existing deletion coordinator. Resolve the active conversation and exclude all its run trees.

## Risks / Trade-offs

- Substring search and cleanup aggregation can still scan matching project rows. Bounded results, indexes and cancellation limit interactive impact; this is not a full-text search system.
- Cursor pages are a traversal, not a database snapshot. New sessions appear when the caller starts again at the first page.
- Clients that previously relied on an unbounded response must follow continuation; the array contract stays intact.

## Migration Plan

Apply normal startup migrations for each storage engine, then update the OpenCode source pin in c2-agent. Both indexes can be removed through their down migration. Roll back the OpenCode image for behavioral rollback; no data rewrite is required.
