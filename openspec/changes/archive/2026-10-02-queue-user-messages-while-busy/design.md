# Design: queue-user-messages-while-busy

## Context

See `proposal.md — Why` for motivation. The relevant current state is:

- `editorCmp.send()` (`internal/tui/components/chat/editor.go:140`) returns a warning
  toast and does nothing else when `IsSessionBusy` is true. The textarea text survives
  but the submission is discarded.
- The process-global session-run ledger (`internal/llm/agent/session_locks.go`) enforces
  one `RunWith` goroutine per session; any second attempt returns `ErrSessionBusy` with
  no side effects. This invariant is never relaxed.
- The agent loop's `processGeneration` re-reads the DB message log on compaction
  (`agent.go:945`) and in the non-interactive outer cycle (`agent.go:1244`). A message
  persisted to the DB before delivery would appear in those reloads non-deterministically.
- The bridge dispatcher (`internal/bridge/service/dispatch.go`) already solves a
  structurally identical problem: `sessionDispatch.run()` processes one `handleInbound`
  at a time, blocking for the full `agent.Run` lifetime; concurrent inbound messages
  queue on a channel rather than being rejected.
- Consecutive same-role user turns are combined by the direct Anthropic API, historically
  rejected by Bedrock, and unverified on VertexAI. Injecting a queued message mid-run
  (between tool calls) would require merging it into the tool-result user message — each
  provider presents a distinct correctness risk, and the blast radius on the hottest code
  path is high.

## Goals / Non-Goals

**Goals:**

- Zero lost submissions: every Enter keypress while busy enqueues the text.
- FIFO delivery as separate `agent.Run` calls after the current run completes.
- No goroutine leaks; no blocking of the Bubble Tea render loop.
- A clear, discard-capable TUI affordance.
- Full compliance with the `session-run-exclusivity` invariant.

**Non-Goals:**

- Mid-run injection (between tool calls). Deferred follow-up; the Bedrock alternation
  constraint makes this non-trivial and the blast radius is high. Documented here so the
  follow-up starts informed.
- Queuing Ctrl+E (open external `$EDITOR`) or slash commands. These mutate session state
  in ways that do not compose safely with a queued text turn.
- Persistence of queued messages. The in-memory approach is intentional.
- Bounding the queue. Unbounded slice; a future change may impose a cap if real-world
  memory pressure appears, and MUST specify overflow back-pressure at that point.

## Decisions

### Decision 1 — Option A (post-run delivery) over B (mid-run injection) and C (shared dispatcher)

**Option A** keeps all queued messages in memory and delivers each one as a separate
`agent.Run` after the slot frees. No agent-loop change; no provider risk; fully
compliant with `session-run-exclusivity`. This is what Claude Code does: queued input
lands after the current response completes, never between tool calls.

**Option B** (mid-run turn-boundary injection, delivering mid-turn before the next
`streamAndHandleEvents` call) avoids the extra round-trip but has two hard problems:
(1) Bedrock rejects consecutive user turns, so the queued text would need to be merged
into the same user message as the pending tool results — non-trivial merge logic in the
hottest code path; (2) ordering relative to tool results is ambiguous and changes model
behavior in ways that are hard to test. Scoped explicitly as a follow-up.

**Option C** (extract the bridge `sessionDispatch` into `internal/dispatch/` and use it
from both bridge and TUI) would eliminate duplication. The bridge path is proven and
load-bearing, so touching it carries risk. The queue-and-serialize core is ~40 lines;
duplicating it in `internal/app/` is simpler and safer for this change. A future
refactor can unify them once both sides are stable.

**Chosen: Option A.** Smallest blast radius, correct semantics, reviewable on its own.

### Decision 2 — In-memory queue, not persisted-pending-message

Adding a `Pending bool` or `Status` column to `message.Message` would require a schema
migration, a struct change, and updates to all consumers of the message list. More
critically, anything written to the DB is immediately visible to the compaction reload
and the non-interactive outer loop in `processGeneration`, making delivery
non-deterministic. An in-memory queue avoids both the schema churn and the hazard.
Consequence: undelivered messages are silently dropped on app exit — acceptable because
the session was never committed.

### Decision 3 — Unbounded slice, not a capped channel

The bridge uses `chan bridge.Inbound` (cap 16) with blocking back-pressure. A channel
with blocking push would stall the Bubble Tea `Update` goroutine, which processes every
key event; a TUI event loop MUST NOT block on an application concern. An unbounded slice
with non-blocking append is safe. A real-world user typing faster than the agent can drain
is unlikely to exhaust memory, and an explicit overflow requirement is deferred.

### Decision 4 — Cancel preserves the queue; discard is explicit

Esc/Ctrl+C fires the run's cancel func and does NOT touch the queue. Rationale: the most
common interruption pattern is "I noticed a mistake; let me redirect" — the queued
follow-up message IS the redirection. The counter-argument (a user hitting Esc to "stop
everything" is surprised when a new run starts) is addressed by making the discard
affordance prominent and by displaying the queue count while the session is busy, so the
user knows messages are pending before pressing Esc.

### Decision 5 — Drain worker per session, on first enqueue

Starting the worker lazily (on first enqueue) avoids creating goroutines for sessions
that never queue anything. The worker is cancelled via a `context.WithCancel` tied to
the session's lifetime in `app.App`. When the queue empties and no new enqueue arrives,
the worker exits; a subsequent enqueue starts a new worker. The goroutine map is guarded
by a mutex on `App`.

### Decision 6 — ErrSessionBusy is retried with back-off, not surfaced

The drain worker will lose an `acquireSessionSlot` race against cron commits. Surfacing
`ErrSessionBusy` as an error toast would alarm the user for a condition that is
transparently retried. The worker re-queues the failed message at the head and sleeps a
short back-off (e.g. 100 ms) before retrying. The retry count is not bounded — the
surrounding context cancellation is the sole deadline.

`IsSessionBusy` MAY be used as a non-authoritative poll to reduce pointless `Run` calls
(avoid attempting when the session is observably busy), but MUST NOT be treated as the
correctness gate for exclusivity — a race can make it return false while the slot is
still held by the deferred cleanup of a run goroutine. The authoritative mechanism is the
atomic `LoadOrStore` in `acquireSessionSlot` and the `ErrSessionBusy` it returns to any
loser. The implementation MUST NOT introduce a check-then-act pattern.

### Decision 8 — Enqueue whenever the queue is non-empty, even when the slot is idle

The `send()` routing condition is: **enqueue if `QueueLen(sessionID) > 0` OR
`IsSessionBusy(sessionID)`; dispatch directly only when both are false** (queue empty AND
slot observably idle). Without this, a message submitted in the window between two drain
deliveries (queue non-empty, slot temporarily free) bypasses the head of the queue and
causes a FIFO inversion — the new message starts a Run before the already-queued
message. The queue-non-empty branch is checked first, before the busy check, so the
ordering is stable even if the slot races.

Consequence: the direct idle path is preserved exactly for the common case (queue always
empty on an idle session). Queue involvement adds no latency when the queue is empty.

### Decision 7 — No shared primitive extracted from the bridge

The bridge's `sessionDispatch` carries bridge-only concerns: inbound channel, parts
fan-out to chat peers, tool-update streaming to Telegram/Slack/Mattermost. Extracting the
~40-line queue-and-serialize core requires splitting the type, adjusting import paths, and
retesting the bridge path. Given the risk/reward ratio, this change duplicates the
pattern in `internal/app/` and documents the follow-up extraction as a known tech-debt
item.

### Decision 9 — Halt the drain on non-ErrSessionBusy error

Two options:

- **Continue**: surface the error for M1, keep draining M2, M3, … Each failing message
  produces its own error toast. Advantage: one bad message (e.g. context-length rejection
  unique to M1) does not block well-formed messages behind it.
- **Halt**: surface the error for M1, stop the drain worker, preserve M2 … MN in the
  queue. One toast; remaining queue visible and discardable.

**Chosen: Halt.** A systemic failure (auth error, model endpoint down, API quota hit)
would fire N identical error toasts in rapid succession with no way to stop the cascade —
far more alarming and confusing than one attributed toast followed by a visible "N still
queued" indicator. The cost is that a single malformed message (e.g. one that exceeds the
context window) blocks the messages behind it until the user discards it. This is
acceptable because: (a) such messages are rare in practice; (b) the discard affordance
is always visible so the user is never stuck; (c) the drain restarts naturally on the
next enqueue, so a transient error (network blip) recovers without user action beyond
retrying.

## Risks / Trade-offs

**[Risk] Undelivered messages dropped on quit** → Acceptable: sessions are best-effort;
no user-visible data loss claim is made. The TUI affordance ("N queued") warns the user
before they quit.

**[Risk] Drain worker continues after session switch** → Intentional by Decision 5.
The worker is bound to the session, not the TUI's active session. This is correct
behavior; a background session's drain should not stop because the user opened a different
tab.

**[Risk] Drain worker retries ErrSessionBusy indefinitely** → Bounded by the context
lifetime. If the app is shutting down, the context is cancelled and the worker exits.
Long-lived retries are not expected in practice (cron locks are short-lived).

**[Risk] Bedrock alternation if follow-up mid-run injection is attempted** → Documented
explicitly here so the follow-up implementation is aware. Any mid-run injection MUST merge
the queued text into the tool-result user message block, NOT append a new user message.

**[Trade-off] Queue is unbounded** → Simplicity and render-loop safety over memory
efficiency. A 10,000-message queue at ~1 KB/message is ~10 MB — unlikely in practice.

## Open Questions

None that would change the spec, approach, or task breakdown. The mid-run injection
design is a deliberate follow-up, not an open question for this change.
