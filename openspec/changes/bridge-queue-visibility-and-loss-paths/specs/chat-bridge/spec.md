## Purpose

Delta spec for the `chat-bridge` capability. Amends the per-session dispatcher requirement
to (a) remove the incorrect "MUST NEVER return `ErrSessionBusy`" assertion, (b) specify the
correct content-preserving retry contract, (c) add the cross-session non-starvation
requirement, and (d) add three missing requirements for previously silent loss paths:
interactive-flow buffer overflow, no-active-agent drop, and in-flight message loss on
process shutdown.

## MODIFIED Requirements

### Requirement: Inbound dispatch MUST NOT starve other sessions (cross-session non-starvation)

The bridge's shared `runInboundLoop` (`service.go:426-438`) processes all sessions through
a single goroutine, calling `dispatchInbound` synchronously for each message. To prevent
one stalled session from blocking dispatch to all others, `dispatchInbound`'s push to any
per-session `d.inbound` channel MUST be non-blocking from the shared loop's perspective.
When a session's `d.inbound` channel is full, the message SHALL be appended to a per-session
in-memory overflow slice on `sessionDispatch` rather than blocking `runInboundLoop`. The
per-session `run()` goroutine SHALL drain the overflow slice after each `handleInbound` call
completes, before reading the next message from `d.inbound`, preserving per-session FIFO.

This non-starvation requirement is independent of `ErrSessionBusy` handling — it applies to
the push path regardless of why a session's channel is full. "NEVER drop (back-pressure
adapter instead)" refers to back-pressuring the per-adapter pump goroutine at the
`s.inboundCh` (cap 64) level; it never meant the shared loop should block per-session.

#### Scenario: Session A full; session B dispatched without delay

- **GIVEN** session A's `d.inbound` channel is full (a long-running agent turn is in flight)
- **WHEN** messages arrive for both session A and session B via `runInboundLoop`
- **THEN** session B's message is dispatched immediately; session A's message is stored in
  A's overflow slice and delivered after A's current turn completes; neither message is
  dropped; `runInboundLoop` is NOT blocked

#### Scenario: Overflow slice drains in FIFO order

- **GIVEN** session A has 3 messages in its overflow slice (accumulated while channel was
  full) and A's current run just completed
- **WHEN** `run()` exits `handleInbound` and checks the overflow slice
- **THEN** the 3 overflow messages are transferred to `d.inbound` in arrival order;
  subsequent reads from `d.inbound` deliver them before any newly arriving messages

### Requirement: Per-`sessionId` dispatch goroutine with dual-channel select (amended)

For each actively-bound `sessionId` the bridge SHALL run exactly **one** dispatcher goroutine
that owns both inbound message dispatch and parts demultiplexing for that session. The
dispatcher MUST use a single `select{}` over two channels:

| Channel | Capacity | Drop policy | Source |
|---|---|---|---|
| inbound  | 16 | NEVER drop — overflow to per-session slice when full (non-starvation requirement); back-pressure propagates at `s.inboundCh` level | per-peer adapter goroutines via runInboundLoop |
| parts    | 64 | drop-oldest with rate-limited log | broker-receive goroutine non-blocking forward |

The dispatcher MUST call `agent.Run` serially — only one in-flight Run per session at a
time. It MUST consume the Run channel's terminal `AgentEvent` before processing the next
inbound message.

**`ErrSessionBusy` from cross-actor holders is a legitimate outcome and MUST be handled by
content-preserving retry, not by discarding.** The session-run ledger is process-global
(`session-run-exclusivity` spec): any holder — a flow step's own agent instance, a cron
sentinel lock, a task auto-resume — makes `agent.Run` return `ErrSessionBusy`. The
single-dispatcher serialization only prevents bridge-vs-bridge collisions; it cannot prevent
cross-actor collisions. When `agent.Run` returns `ErrSessionBusy`, the bridge MUST:

1. Retain the inbound message content — it MUST NOT be discarded.
2. Retry with a short backoff (≤ 200 ms per attempt) for a bounded per-attempt budget.
3. On budget exhaustion, re-queue the message for a later attempt rather than discarding.
   Content preservation is unconditional and budget-independent.
4. Inform the sender (via `bridge-queue-visibility` if enabled) that their message is
   waiting behind a run it does not own.

The per-attempt budget is intentionally distinct from the TUI drain worker's unbounded
retry: the TUI uses a per-session goroutine isolated from other sessions; the bridge
`handleInbound` uses a bounded budget so other items queued behind it can make progress
between reattempts, with the message re-entering the queue rather than being discarded.

The dispatcher's lifecycle is tied to the binding: created on first `Bind(sessionId, ...)`,
torn down on `Unbind(sessionId)` or when the bridge observes `session_id == NULL`.

#### Scenario: Cross-actor `ErrSessionBusy` is retried, not discarded

- **GIVEN** a flow step's agent instance holds the session slot for session S
- **WHEN** an inbound message M from peer P reaches the dispatcher and `agent.Run` returns
  `ErrSessionBusy`
- **THEN** M is retained and retried with backoff; M is NOT discarded and the user is NOT
  told to resend; if the per-attempt budget expires, M is re-queued for a subsequent attempt
  via the overflow mechanism, never dropped

#### Scenario: Budget expires; message re-queued, not discarded

- **GIVEN** a flow step holds session S's slot for longer than the per-attempt retry budget
- **WHEN** the budget expires without the slot freeing
- **THEN** M's content is preserved and re-queued (via the overflow slice or equivalent);
  the session's dispatcher may process other pending messages before retrying M;
  M is eventually delivered when the slot frees; the sender's queued-ack is updated

#### Scenario: Retry succeeds when the competing run finishes

- **GIVEN** M was re-queued after an `ErrSessionBusy` from a flow-step holder
- **WHEN** the flow step's run completes and releases the session slot
- **THEN** the dispatcher's next attempt of `agent.Run` succeeds; M's full content is
  delivered to the agent as if it had arrived after the competing run

#### Scenario: Bridge-originated `ErrSessionBusy` cannot occur

- **WHEN** Alice and Bob both send messages to session S within milliseconds of each other
- **THEN** their messages land on the per-session inbound channel in arrival order; the
  dispatcher processes Alice's full agent turn before pulling Bob's message; no
  `ErrSessionBusy` ever surfaces from bridge-internal serialization

#### Scenario: Parts overflow drops oldest, logs once per session per minute

- **WHEN** a session emits more than 64 part events while its sender is blocked on
  outbound IO
- **THEN** the oldest part is dropped, the newest appended, and a warn-level overflow log
  is emitted (rate-limited to once per session per minute)

### Requirement: No-active-agent drop is audible

When `handleInbound` is reached but `ActiveAgent()` returns nil — meaning no agent is
registered in the process — the bridge MUST NOT silently discard the inbound. The bridge
SHALL reply to the sender's peer with a brief error explaining that no agent is available,
so the sender knows their message was not processed and can retry or escalate.

#### Scenario: Inbound arrives when no agent is configured

- **GIVEN** `app.ActiveAgent()` is nil (e.g. the agent service has not initialized or
  has been torn down)
- **WHEN** an inbound message arrives from peer P for session S
- **THEN** the bridge sends a reply to P explaining the message could not be processed
  due to no active agent; the message is not delivered silently to /dev/null

### Requirement: Interactive-flow buffer overflow is audible

When `QuestionRouter.BufferInbound` evicts the oldest buffered message because the
per-session interactive buffer is full (`interactiveInboundBufferCap`, currently 16), the
bridge MUST reply to the peer whose message was dropped informing them that their input was
not retained and they should resend it after the current interactive step concludes. A
warn-level log entry remains required; the user-visible reply is additive.

#### Scenario: Interactive-buffer overflow evicts message 17

- **GIVEN** session S is in an interactive flow step with 16 messages already buffered and
  no question pending
- **WHEN** a 17th inbound message arrives from peer P
- **THEN** the oldest buffered message (message 1) is evicted; the bridge sends a reply to
  that message's peer notifying them that their input was dropped and should be resent; the
  warn-level log is also emitted

### Requirement: Queued messages logged at WARN on shutdown; in-chat notification not required

When `Service.Stop` tears down a `sessionDispatch` that has messages queued in its inbound
channel or per-session overflow slice, the bridge SHALL drain those messages BEFORE closing
the channel and SHALL emit one WARN log entry per dropped message (session ID, peer ID) plus
a per-session summary count.

In-chat notification to senders at shutdown is NOT required and MUST NOT be specced as even
advisory. `Service.Stop()` cancels the service context before `tearDownDispatchers()` runs;
any adapter API call using that cancelled context returns immediately without reaching the
platform. Speccing unreliable behavior produces tests that pass in timing-lucky runs and
fail silently in production.

Durability across process restarts is explicitly out of scope for this change.

#### Scenario: Shutdown with queued messages

- **GIVEN** session S's dispatcher has 3 messages in its inbound channel when
  `Service.Stop` is called
- **THEN** before closing `d.inbound`, the bridge drains the channel; for each item it logs
  at WARN: `"bridge: shutdown lost queued inbound session=<S> peer=<P>"`; a final WARN
  carries the total count; no in-chat reply is sent or attempted

#### Scenario: Clean shutdown with empty queues

- **GIVEN** all session dispatchers have empty inbound channels and overflow slices at
  shutdown time
- **THEN** no shutdown-loss warn is emitted; shutdown is silent as before
