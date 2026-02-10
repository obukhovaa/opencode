# Session Tree File Aggregation
**Date**: 2026-02-10
**Status**: Implemented
**Author**: AI-assisted

## Overview

The TUI sidebar currently shows only files modified in the root session. When subagents create or edit files through the Task tool, those changes remain invisible to the parent session. This spec introduces a `root_session_id` column to enable efficient tree-wide file aggregation without recursive queries.

## Motivation

### Current State

The sidebar loads files using single-session queries:

```go
// internal/tui/components/chat/sidebar.go
func (m *Model) loadModifiedFiles() {
    latestFiles, err := m.history.ListLatestSessionFiles(ctx, m.session.ID)
    allFiles, err := m.history.ListBySession(ctx, m.session.ID)
}
```

The event handler drops file events from child sessions:

```go
case pubsub.Event[history.File]:
    if msg.Payload.SessionID == m.session.ID {
        // only processes root session events
    }
```

The underlying SQL queries have no concept of session hierarchy:

```sql
-- internal/db/sql/files.sql
SELECT * FROM files WHERE session_id = ? ORDER BY created_at ASC;
```

When a subagent edits `main.go`, that change never surfaces in the parent session's sidebar. The `parent_session_id` column exists and is populated correctly, but nothing uses it for file aggregation.

### Desired State

The sidebar shows all files modified anywhere in the session tree. When a task spawns a subagent that creates `helper.go`, that file appears in the root session's modified files list. The implementation uses simple indexed JOINs instead of recursive CTEs or N+1 queries.

## Research Findings

### Approach Comparison

| Approach | Query Complexity | Performance | Migration Cost | Maintenance |
|----------|-----------------|-------------|----------------|-------------|
| Recursive CTEs | High (WITH RECURSIVE) | Degrades with depth | None | Every query repeats CTE |
| App-level iteration | Low (simple SELECT) | O(N) queries | None | Complex Go code |
| Closure table | Medium (JOIN ancestor table) | O(1) lookups | High (new table + triggers) | Trigger maintenance |
| **root_session_id denorm** | **Low (simple JOIN)** | **O(1) indexed JOIN** | **Low (one column)** | **Set once at creation** |

### Existing Infrastructure

The sessions table already tracks hierarchy:

```sql
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    parent_session_id TEXT,
    -- other fields...
);
```

Session creation already handles parent relationships:

```go
// internal/session/session.go
func (s *service) CreateTaskSession(toolCallID, parentSessionID, title string) (*Session, error) {
    // sets parent_session_id correctly
}
```

Cost aggregation already walks the tree in `agent-tool.go` (ad-hoc implementation).

### Known Bugs

The `ListLatestSessionFiles` query has a scoping bug:

```sql
-- Groups across ALL sessions globally, not scoped to target session
SELECT f.* FROM files f
INNER JOIN (
    SELECT path, MAX(created_at) as max_created_at
    FROM files GROUP BY path  -- BUG: no WHERE clause
) latest ON f.path = latest.path AND f.created_at = latest.max_created_at
WHERE f.session_id = ?
```

The `ListNewFiles` query references a non-existent `is_new` column and should be removed.

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Add `root_session_id` column | Enables O(1) tree queries without recursive CTEs |
| Denormalize instead of compute | Set once at creation, never changes, avoids repeated traversal |
| Keep existing single-session methods | Backward compatibility, gradual migration |
| Add tree-aware methods alongside | Callers choose single-session or tree-wide behavior |
| Index `root_session_id` | File queries JOIN on this column frequently |
| Set `root_session_id = own ID` for roots | Simplifies queries (no NULL checks), consistent JOIN logic |
| Propagate from parent for children | One lookup at creation time, then cached forever |
| Fallback to `session.ID` when NULL | Pre-migration sessions degrade gracefully |

## Architecture

### Session Tree Structure

```
Root Session (id=A, root_session_id=A)
├── Task Session (id=B, parent=A, root_session_id=A)
│   ├── File: src/main.go (session_id=B)
│   └── File: src/helper.go (session_id=B)
├── Task Session (id=C, parent=A, root_session_id=A)
│   └── Sub-task Session (id=D, parent=C, root_session_id=A)
│       └── File: test/unit.go (session_id=D)
└── Title Session (id=E, parent=A, root_session_id=A)
```

### Query Pattern

```
Query: ListLatestSessionTreeFiles(root_session_id=A)

Result:
- src/main.go (from session B)
- src/helper.go (from session B)
- test/unit.go (from session D)

SQL:
SELECT f.* FROM files f
INNER JOIN sessions s ON f.session_id = s.id
WHERE s.root_session_id = 'A'
```

### Data Flow

```
User creates root session
  → session.Create sets root_session_id = own ID
  → Session A (root_session_id=A)

Agent invokes Task tool
  → CreateTaskSession looks up parent's root_session_id
  → Session B (parent=A, root_session_id=A)

Subagent edits file
  → File record created with session_id=B
  → Pubsub broadcasts file event

TUI sidebar receives event
  → Checks if event.SessionID in tree (via root_session_id)
  → Reloads using ListLatestSessionTreeFiles(A)
  → File appears in sidebar
```

## Implementation Plan

- [x] **Phase 1: Database Schema**
  - [x] Create migration adding `root_session_id` column (SQLite)
  - [x] Create migration adding `root_session_id` column (MySQL)
  - [x] Add index on `root_session_id` in both migrations
  - [x] Update `CreateSession` query to accept `root_session_id` parameter
  - [x] Add `ListChildSessions` query (both dialects)
  - [x] Add `ListFilesBySessionTree` query (both dialects)
  - [x] Add `ListLatestSessionTreeFiles` query (both dialects)
  - [x] Fix `ListLatestSessionFiles` subquery scoping bug
  - [x] Remove broken `ListNewFiles` query
  - [x] Regenerate sqlc code (`make generate`)

- [x] **Phase 2: Session Service**
  - [x] Add `RootSessionID` field to `session.Session` struct
  - [x] Update `Create` to set `root_session_id = own ID`
  - [x] Update `CreateTaskSession` to propagate parent's `root_session_id`
  - [x] Update `CreateTitleSession` to propagate parent's `root_session_id`
  - [x] Add `ListChildren(rootSessionID)` method to service interface
  - [x] Implement `ListChildren` using new query

- [x] **Phase 3: History Service**
  - [x] Add `ListBySessionTree(rootSessionID)` to service interface
  - [x] Add `ListLatestSessionTreeFiles(rootSessionID)` to service interface
  - [x] Implement both methods using new queries

- [x] **Phase 4: TUI Integration**
  - [x] Update sidebar `loadModifiedFiles` to use tree-aware methods
  - [x] Add fallback logic for NULL `root_session_id` (pre-migration sessions)
  - [x] Update event handler to accept file events from child sessions
  - [x] Subscribe to session creation events to track child session IDs
  - [x] Test with nested subagent scenarios

- [x] **Phase 5: Testing & Cleanup**
  - [x] Add tests for tree-aware queries
  - [x] Test backward compatibility with pre-migration sessions
  - [x] Test deeply nested session trees
  - [x] Test concurrent subagents editing same file
  - [x] Run `make test` to verify all checks pass

## Edge Cases

**Pre-migration sessions**: Sessions created before the migration have `root_session_id = NULL`. Tree-aware queries return empty results. The TUI should detect NULL and fall back to single-session queries using `session.ID`.

**Deeply nested subagents**: A task spawns a sub-task which spawns another. All descendants share the same `root_session_id`, so aggregation works at any depth without additional logic.

**Same file edited by parent and child**: Both sessions create file records for `src/main.go`. The `ListLatestSessionTreeFiles` query groups by path and takes `MAX(created_at)`, so the most recent edit wins regardless of which session made it.

**Resumable task sessions**: When the Task tool provides a `task_id` to resume an existing session, no new session is created. The existing session already has `root_session_id` set, so no special handling needed.

**Title sessions**: These are child sessions that generate titles but don't create files. They should still get `root_session_id` for consistency, even though they won't affect file queries.

**Concurrent subagents**: Multiple subagents running in parallel, all with the same `root_session_id`. File aggregation handles this naturally since it groups by path and takes the latest version by timestamp.

**Session deletion**: The `ON DELETE CASCADE` foreign key on `files.session_id` already handles cleanup. When a session is deleted, its files are removed. The `root_session_id` column doesn't change this behavior.

**Migration rollback**: If the migration needs to be rolled back, the column can be dropped. Existing queries don't reference it, so they continue working. Tree-aware queries would fail, but the TUI fallback logic handles this.

## Open Questions

**Should we backfill `root_session_id` for existing sessions?**

Recommendation: No. The migration sets the column to NULL for existing rows. The TUI fallback logic handles this gracefully. Backfilling requires recursive traversal of potentially large session trees and risks data corruption if the tree is malformed. Let new sessions populate the column naturally.

**Should we add a background job to aggregate costs using `root_session_id`?**

Recommendation: Not in this spec. Cost aggregation currently happens ad-hoc in `agent-tool.go`. A future spec can replace that with a proper aggregation query using `root_session_id`, but it's orthogonal to file aggregation.

**Should the sidebar show which session each file came from?**

Recommendation: Not initially. The sidebar shows file paths and modification status. Adding session attribution would clutter the UI. Consider this for a future enhancement if users request it.

**Should we validate that `root_session_id` points to a session with `parent_session_id = NULL`?**

Recommendation: No. This would require a CHECK constraint with a subquery, which SQLite doesn't support. The application logic ensures correctness by setting `root_session_id` at creation time. If corruption occurs, queries still work (they just return unexpected results).

## Success Criteria

- Modified files from subagent sessions appear in the parent session's TUI sidebar
- Existing single-session file queries continue to work unchanged
- Pre-migration sessions degrade gracefully (no crashes, single-session behavior)
- Both SQLite and MySQL migrations apply successfully
- New queries use indexed JOINs (verify with EXPLAIN)
- `make test` passes all checks
- Deeply nested session trees (3+ levels) aggregate files correctly
- Concurrent subagents editing the same file show the latest version

## References

**Database Layer**
- `internal/db/migrations/sqlite/` — SQLite migration files
- `internal/db/migrations/mysql/` — MySQL migration files
- `internal/db/sql/files.sql` — SQLite file queries
- `internal/db/sql/mysql/files.sql` — MySQL file queries
- `internal/db/sql/sessions.sql` — SQLite session queries
- `internal/db/sql/mysql/sessions.sql` — MySQL session queries
- `internal/db/querier.go` — Generated querier interface
- `internal/db/mysql/querier.go` — Generated MySQL querier
- `internal/db/models.go` — Generated DB models
- `internal/db/schema/mysql.sql` — MySQL schema for sqlc

**Service Layer**
- `internal/history/file.go` — File history service
- `internal/session/session.go` — Session service

**TUI Layer**
- `internal/tui/components/chat/sidebar.go` — Sidebar component

**Agent Layer**
- `internal/llm/agent/agent-tool.go` — Task tool (creates child sessions)

**Infrastructure**
- `internal/pubsub/events.go` — Event types
- `sqlc.yaml` — sqlc configuration
