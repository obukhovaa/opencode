# Cross-Process Session Observation

**Status:** Draft  
**Created:** 2026-03-04

## Problem

When a running OpenCode process has an active agent session, a second process opening the same session sees a static snapshot from when it loaded the DB. There is no subscription to the first process's updates — the TUI never receives fresh messages. It would be valuable if another OpenCode process could connect and observe session progress in real-time, in **read-only mode**, since concurrent writes would break the single-producer principle.

## Current Architecture

### What exists

- **PubSub is entirely in-memory** — `map[chan Event[T]]struct{}` in `pubsub.Broker`. Zero cross-process capability.
- **DB is the source of truth** — every agent write goes to DB first, then pubsub fires.
- **SQLite WAL mode** — allows concurrent readers while the writer is active.
- **`messages.seq`** — monotonically increasing per session, suitable for incremental reads (`WHERE seq > last_seen_seq`).
- **`sessions.updated_at`** — trigger-updated on every mutation, cheap change detection.
- **`finished_at IS NULL`** — natural "still streaming" indicator on messages.
- **`activeRequests sync.Map`** — prevents concurrent agent runs within one process (needs a DB-level equivalent).

### What does NOT exist

- No dedicated lock/ownership table.
- No process identity stored in the DB.
- No cross-process notification mechanism (SQLite has no `LISTEN/NOTIFY`).
- No polling infrastructure — all reads are on-demand.
- No TTL/heartbeat mechanism for detecting stale ownership.

## Two Orthogonal Pieces Needed

1. **Session Lock** — prevent a second process from writing to an active session.
2. **Change Notification** — let the observer process know when to re-read.

## Implementation Options

### Option A: Poll DB (simplest, ~2-3 days)

**Session lock:** New `session_locks` table:

```sql
CREATE TABLE session_locks (
  session_id  TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
  owner_pid   INTEGER NOT NULL,
  owner_host  TEXT NOT NULL,
  heartbeat   INTEGER NOT NULL,  -- unix ms
  created_at  INTEGER NOT NULL
);
```

- Writer does `INSERT OR IGNORE` on agent `Run()`, deletes on completion.
- Heartbeat updated every ~5s via background goroutine.
- Observer detects stale locks via `heartbeat < now - 15s`.

**Change notification:** Poll loop in the observer process:

- Poll `GetMaxSeqBySession(sessionID)` every 200-500ms.
- When seq changes, re-fetch new messages with `WHERE seq > last_seen_seq` (needs one new query).
- Poll `sessions.updated_at` for token/cost counter updates.
- Feed fetched messages into the local pubsub broker as synthetic events — TUI renders normally.

**Pros:** Dead simple, no new IPC, works with MySQL too, robust to crashes (stale lock detection).

**Cons:** 200-500ms latency, unnecessary DB reads when nothing changes. But SQLite reads of a single integer are ~10μs, so the load is negligible.

### Option B: UDS Signal + DB Read (medium, ~4-5 days)

Writer process acquires session lock (same table as Option A), writes a UDS path into the lock row (e.g. `/tmp/opencode-{pid}-{sessionID}.sock`), starts a minimal UDS listener that sends a 1-byte signal whenever a message event fires (hooked into the existing `message.Broker`).

Observer reads the UDS path from the lock row, connects, and on each signal byte re-reads messages from DB.

**Wire protocol:** Writer sends `\x01` on each message create/update. Observer reads it and does a DB fetch. No serialization needed.

**Pros:** Near-instant notification (<1ms), no wasted polls, clean separation (UDS = "something changed", DB = source of truth).

**Cons:** UDS lifecycle management (cleanup on crash), platform-specific (Unix only, no Windows), slightly more moving parts.

### Option C: UDS Event Stream (~6-8 days)

Same as B but the UDS carries full serialized `pubsub.Event[message.Message]` as JSON. Observer deserializes and injects into its local broker — TUI renders without any DB reads for streaming content.

**Pros:** True real-time, zero DB polling, lowest latency.

**Cons:** Must serialize/deserialize the full `Message` type including all `ContentPart` variants. Fragile coupling to message types. Must handle reconnection, buffering, backpressure, and catch-up on reconnect.

## Recommendation

**Option A (poll DB) is the right starting point.**

1. **200ms poll latency is invisible** — LLM tokens arrive every 50-200ms anyway, and the TUI renders at ~30fps.
2. **Zero new IPC code** — no UDS lifecycle, no crash cleanup, no platform concerns, works identically with MySQL.
3. **Robust** — if the writer crashes, the observer just sees the heartbeat go stale and can release the lock. No orphaned sockets.
4. **Incremental path to Option B** — the lock table already has a column for a UDS address. Adding the signal layer later is additive, not a rewrite.
5. **The `seq` field makes incremental reads trivial** — `SELECT * FROM messages WHERE session_id = ? AND seq > ? ORDER BY seq ASC`.

## Implementation Sketch (Option A)

```
New migration: session_locks table
New queries: ListMessagesAfterSeq, AcquireSessionLock, ReleaseSessionLock, RefreshLockHeartbeat

Agent.Run():
  - acquire lock → defer release
  - start heartbeat goroutine

App startup:
  - detect locked sessions
  - show lock indicator in TUI

Session open (locked by another process):
  - enter read-only mode
  - disable editor input
  - start poll goroutine (200ms interval)
  - poll: check MAX(seq), fetch new msgs, inject into local broker
  - poll: check session.updated_at for cost/token updates
  - poll: check lock row — when lock disappears, switch to normal mode

TUI:
  - show "observing" badge
  - hide editor
  - show lock owner info
```

**Estimated effort:** ~3 days for a working MVP, another 1-2 days for polish (lock staleness detection, graceful mode transitions, edge cases like the writer finishing mid-observation).
