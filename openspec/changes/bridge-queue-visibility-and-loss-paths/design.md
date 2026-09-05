# Design: bridge-queue-visibility-and-loss-paths

## Context

See `proposal.md § Why` for motivation. The relevant existing architecture:

- `sessionDispatch.inbound` is a buffered channel (cap 16, `dispatch.go:30`). `pushInbound`
  (`dispatch.go:908-914`) blocks on send with no `default:` branch. The `run()` loop
  (`dispatch.go:124-138`) calls `handleInbound` which blocks for the entire `agent.Run`
  lifetime.
- On `ErrSessionBusy` today (`dispatch.go:218-223`): the message content is discarded; the
  user is told "please resend". `runFailureMessage` (`dispatch.go:258-269`) contains the
  lossy wording pinned by `TestRunFailureMessage_BusyDoesNotAdviseAbort`
  (`dispatch_test.go:60-74`).
- `runInboundLoop` (`service.go:426-438`) is a **single shared goroutine** for the whole
  service. It calls `s.dispatchInbound(ctx, in)` **synchronously** (line 435).
  `dispatchInbound` → `pushInbound` blocks when the target session's `d.inbound` is full.
  This means: if any one session's channel fills, `runInboundLoop` blocks, and **every
  other session on every adapter stops being dispatched**. This is the cross-session
  starvation topology.
- Per-adapter buffer: `adapterInbound := make(chan bridge.Inbound, 32)` (`adapters.go:96`).
  `sendInboundWithBackpressure` (`adapters.go:162-177`) blocks indefinitely (ctx only) on
  `s.inboundCh`.
- The session-run ledger is process-global (`session-run-exclusivity/spec.md`). Cross-actor
  holders (flow steps, cron sentinels, task auto-resume) produce `ErrSessionBusy` that the
  single-dispatcher invariant cannot prevent.
- All three adapters support in-place edit: Telegram (`bot.EditMessageText`,
  `adapter.go:523`), Slack (`api.UpdateMessageContext`), Mattermost (`client.UpdatePost`).
  No emoji-reaction primitive exists on any adapter. The per-session interactive buffer
  cap matches `dispatchInboundCap` (both 16, `question.go:43`).
- The TUI drain worker (`internal/app/queue.go:131-212`) treats `ErrSessionBusy` as a
  100 ms retryable backoff. **This precedent does NOT transfer to the bridge without
  modification** — see Decision 1 for the critical topology difference.
- `POST /router/inbound` (`http_inbound.go:50-58`) non-blocking select returns bare
  `429 "inbound dispatcher full; retry"`. No `Retry-After` header.
- `runNudger` is at `question.go:516-527`; the interactive buffer short-circuit at
  `inbound.go:132-138`.

## Goals / Non-Goals

**Goals:**
- Human message content is never discarded on `ErrSessionBusy` — TUI parity of intent.
- Cross-actor busy is observable to the user (visibility).
- A single stalled session cannot freeze all other sessions (non-starvation).
- Three silent loss paths become audible.
- Orchestrator-facing 429 is actionable.

**Non-goals (design level):**
- Message durability across restart — queued message bytes are in-memory; binding rows
  persist in `bridge_sessions`. Making shutdown loss log-audible is the requirement;
  durable delivery is deferred.
- Unifying the bridge queue with `internal/app/queue.go` — routing bridge inbound through
  the app queue would place two queues in series for the same session, making depth
  accounting ambiguous. Stated non-goal.
- Adding emoji-reaction primitives to any adapter.

## Decisions

### Decision 1: ErrSessionBusy retry — bounded, with non-blocking push to prevent starvation

**Decision: bounded per-attempt retry (≤ 5 min) + non-blocking push with per-session
overflow to decouple runInboundLoop from per-session channel depth.**

#### Why unbounded inner-loop retry is unsafe in the bridge topology

The TUI drain worker (`app/queue.go`) retries `ErrSessionBusy` unboundedly. At first glance
this seems like the right precedent. It is NOT directly applicable: the TUI gives each
session its own goroutine for the entire drain sequence. `runInboundLoop` is a **single
shared goroutine** for all sessions; it calls `dispatchInbound` synchronously; `dispatchInbound`
calls `pushInbound` which blocks when `d.inbound` is full.

Starvation chain with unbounded in-handle retry:

1. Session A hits `ErrSessionBusy`. A's per-session `run()` goroutine stays inside
   `handleInbound` retrying `agent.Run` — for as long as the competing run (e.g. a flow
   step) holds the slot. That can be the entire flow-step duration: `flow-runtime-resume`
   spec explicitly says such holders "outlast any budget worth spending".
2. While `run()` is blocked, it does not read from `d.inbound`. New messages for A from
   reviewers accumulate in the 16-slot channel.
3. On message 17, `pushInbound` in `dispatchInbound` (called from `runInboundLoop`) blocks
   waiting for a slot.
4. `runInboundLoop` is now blocked. Every other session — on every adapter, every identity —
   stops being dispatched. One wedged session takes down the whole bridge.

The TUI's per-session goroutine is isolated; the bridge's shared loop is not. The precedent
does not transfer.

#### Root-cause fix: non-blocking push with per-session overflow (Option B)

The cross-session starvation root cause is that `runInboundLoop` can block in `pushInbound`
waiting for a slot on any session's channel. Fix: make `dispatchInbound`'s push to
`d.inbound` non-blocking. If the channel is full, append to a per-session in-memory
`overflow` slice on `sessionDispatch`. `run()` drains the overflow slice after each
`handleInbound` returns, before reading the next message from `d.inbound`, preserving FIFO
ordering. This ensures:

- `runInboundLoop` completes `dispatchInbound` in O(1) regardless of per-session depth.
- Content is never dropped (overflow is in-memory, unbounded in practice).
- Per-session FIFO is preserved: overflow items are served before new `d.inbound` reads.
- Back-pressure on adapters still propagates through `s.inboundCh` (cap 64) and
  `sendInboundWithBackpressure` — the adapter-level stall behavior is unchanged.
- The "NEVER drop (back-pressure adapter instead)" contract is preserved at the adapter
  tier; what changes is that the back-pressure no longer reaches the shared loop.

Considered alternative — goroutine-per-`dispatchInbound` call (Option C): rejected because
two concurrent goroutines pushing for the same session can arrive at `pushInbound` out of
spawn order, breaking per-session FIFO. The overflow slice approach keeps pushes sequential.

#### ErrSessionBusy retry: bounded at 5 minutes per attempt

With the non-blocking push fix in place, the per-session `run()` goroutine blocks in
`handleInbound` while retrying — this only stalls that session's own queue (which drains
via overflow), not the shared loop. Unbounded retry is now safe from a system perspective,
but still undesirable: an agent slot held for hours would park one session's queue
indefinitely, with the reviewer seeing increasingly stale "queued" acks.

Chosen shape: `handleInbound` retries `agent.Run` with 100 ms backoff for up to 5 minutes.
If the budget is exhausted without success, `handleInbound` does NOT discard the message;
instead it signals `run()` to re-queue the item (via a non-blocking push to `d.inbound`,
which goes to overflow if the channel is full). `run()` waits a longer backoff (e.g. 30 s)
before re-reading, giving other queued messages a chance to make progress. The retry clock
resets when the item re-enters the queue. Content is never discarded.

This asymmetry with the TUI is intentional and documented: the TUI worker can afford
unbounded blocking because it is isolated; the bridge `handleInbound` uses a budget to
allow other pending items for the same session to make progress between reattempts.

Considered: a shorter budget (1 min). Rejected — cross-actor runs (flow steps) can
legitimately hold a session for several minutes, and a 1-minute budget would re-queue
aggressively, creating unnecessary churn.

### Decision 2: Where to generate and update the queued-visibility ack

**Decision: generate in `handleInbound` before the busy-retry inner loop; update inside
the loop on each retry cycle; resolve on loop exit (success or non-busy error).**

Because `handleInbound` blocks for the run lifetime, it is the natural owner of the ack
lifecycle. The ack token (message ID / ts) returned by `Send` is stored in a local variable
and passed to `Edit` on each retry. Callers do not need a new field on `sessionDispatch` —
this is ephemeral per-inbound-call state.

The short-wait threshold check (see Decision 3) is also evaluated here: a 2-second timer
arms after the first `ErrSessionBusy`; if the retry loop succeeds before the timer fires,
the ack is never sent.

### Decision 3: Short-wait threshold for the queued-ack

**Decision: hardcode 2 seconds for v1; not config-exposed.**

An ack sent within the same second as the inbound creates a worse UX than no ack: the user
sees "queued" then "processing" nearly simultaneously. The threshold is a UX tuning knob,
not a behavioral invariant, so it need not be in `.opencode.json`. 2 seconds avoids the
flash while remaining short enough that a user waiting 10 seconds definitely sees the ack.

### Decision 4: In-place edit vs new-message per position update

**Decision: in-place edit on all three platforms; no new messages posted per position
change.**

All three adapters already expose edit primitives (Telegram `bot.EditMessageText`
`adapter.go:523`, Slack `api.UpdateMessageContext`, Mattermost `client.UpdatePost`). The
existing `updateAnsweredWidget` in `slack/adapter.go:565` is prior art for in-place update.
Posting a new message per drain step would flood the channel — a session with 5 queued
messages would produce 5 "position updated" posts. Edit failures are logged at info level;
the stale ack is left rather than posting a new one.

### Decision 5: Shutdown loss — log-only (in-chat not feasible)

**Decision: mandatory WARN log per dropped message; no in-chat notification.**

`Service.Stop()` calls `cancel()` first, which cancels the service context. By the time
`tearDownDispatchers()` runs, the service context is done. Any `replyToPeer` call after
that point uses a cancelled context — adapter API calls return immediately on `ctx.Done()`
rather than reaching the platform. Even if adapter goroutines are still alive briefly (the
race window between `cancel()` and goroutine exit), the cancelled context makes any API
call unreliable.

There is no feasible teardown window in which the adapter is definitely alive and the
context is definitely valid. Attempting advisory in-chat notification would produce
misleading test assertions (passes in timing-lucky runs, silently fails in production).

The WARN log (with session ID and message count) is the hard requirement. It is testable
against the log sink and meaningful to operators reading server logs. Durable delivery
across restarts remains a named non-goal.

### Decision 6: Per-adapter ack interface

**Decision: new optional `QueuedAcknowledger` interface asserted with `ok` pattern; all
three production adapters implement it.**

Rather than adding methods to the existing `Adapter` interface, `bridge.QueuedAcknowledger`
is asserted per-call — callers check `adapter, ok := a.(bridge.QueuedAcknowledger)` and
skip the ack if the adapter doesn't satisfy it. Test doubles that don't satisfy it behave
as if acks are disabled.

```go
type QueueAckToken = string  // platform-native message ID / ts

type QueuedAcknowledger interface {
    SendQueuedAck(ctx context.Context, peer PeerRef, position int) (QueueAckToken, error)
    UpdateQueuedAck(ctx context.Context, peer PeerRef, token QueueAckToken, position int) error
}
```

`position` is 1-based (1 = "your message is next").

## Risks / Trade-offs

**Non-blocking push + overflow adds a new in-memory structure per session.** The overflow
slice is unbounded in principle (a very chatty reviewer during a long flow step can
accumulate many entries). In practice the limit is the adapter's own stall behavior:
`sendInboundWithBackpressure` stalls the adapter pump when `s.inboundCh` (cap 64) is full,
so at most 64 unprocessed items can accumulate system-wide before adapters back-pressure.
Per-session overflow depth is bounded by that shared cap. Risk is low.

**Re-queue on `ErrSessionBusy` makes `TestRunFailureMessage_BusyDoesNotAdviseAbort` wrong.**
The test pins lossy wording that will no longer be reachable. It must be deliberately
updated; the tasks call this out explicitly.

**5-minute per-attempt budget means a persistently busy session re-queues every 5 min.**
If the competing run is a 2-hour flow step, the message re-queues ~24 times, each time
resetting the 2-second ack threshold timer. The user sees the ack update on each cycle.
This is acceptable behavior: the reviewer knows their message is alive and being retried.

**Teardown WARN log requires draining `d.inbound` before `close()`.** The current
`tearDownDispatchers` closes without draining (`dispatch.go:897-902`). The implementation
must drain first (collect to a slice, log, then close). This is a behavior change to
`sessionDispatch.close()` that must not introduce a race with the `run()` goroutine.
