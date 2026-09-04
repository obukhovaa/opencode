## Purpose

Holds user chat messages submitted while the agent is running and delivers them in order
once the current run completes, giving the same queued-input experience as Claude Code
without violating the one-Run-per-session exclusivity invariant.

## ADDED Requirements

### Requirement: Submitted messages are enqueued rather than rejected while the session is busy

When the user submits a message (Enter or Ctrl+S) while the session slot is held, the
system SHALL enqueue the message and attachments in an in-memory, per-session FIFO queue
and reset the textarea. The system MUST NOT show a warning toast for the discarded
submission and MUST NOT persist the queued message to the database. The textarea reset is
a behavior change from today: previously, nothing happened and the text remained in the
textarea; after this change the text moves into the queue and the input field is cleared.

#### Scenario: Second message submitted while agent is running
- **GIVEN** the agent is running on session S
- **WHEN** the user types a message and presses Enter
- **THEN** the message is appended to S's in-memory queue, the textarea is reset, and no
  toast is shown

#### Scenario: Queue preserves FIFO order
- **GIVEN** messages M1 and M2 are enqueued on session S in that order
- **WHEN** the drain worker delivers them
- **THEN** M1 is delivered to `agent.Run` before M2

#### Scenario: Queued messages are not visible in the chat message list
- **GIVEN** message M is in the queue for session S
- **WHEN** the chat message list renders
- **THEN** M does not appear as a chat message (it is shown only via the queue affordance)

### Requirement: The queue MUST NOT persist messages before delivery

Queued messages SHALL remain in memory only until dequeued for delivery. The system MUST
NOT call `message.Create` or any DB-write path for a message while it is in the queue.
This prevents agent-loop compaction and the non-interactive reload in `processGeneration`
from sweeping a queued message into the in-flight run's history, which would make
delivery non-deterministic.

#### Scenario: Compaction during a run with a queued message
- **GIVEN** session S has one queued message M and one in-flight run
- **WHEN** the in-flight run triggers compaction (re-listing messages from the DB)
- **THEN** M does not appear in the compacted message history; it is only delivered on
  the next drain cycle after the slot frees

#### Scenario: Queued message persisted on delivery
- **GIVEN** message M is at the head of S's queue
- **WHEN** the drain worker dequeues M and calls `agent.Run`
- **THEN** M is persisted by the normal `Run` path (as it would be for any user turn)

### Requirement: A drain worker delivers queued messages after the slot frees

The system SHALL maintain a per-session drain worker goroutine that calls `agent.Run` for
each queued message in order. The worker MUST treat `ErrSessionBusy` as a retryable
signal and MUST NOT surface it as a user-visible error. Each queued message MUST be
delivered as a separate `agent.Run` call (no coalescing), preserving the user's intent as
N sequential conversational turns.

The sole correctness mechanism preventing concurrent runs is the atomic slot acquisition
inside `RunWith` (`acquireSessionSlot` `LoadOrStore`) and the `ErrSessionBusy` it returns
to any loser. `IsSessionBusy` MAY be used as a non-authoritative poll to avoid a
pointless `Run` call when the session is observably busy, but MUST NOT be relied on for
exclusivity — a race can make it return false while the slot is still held. The design
MUST NOT introduce a check-then-act pattern around slot acquisition.

#### Scenario: Drain after a run completes
- **GIVEN** session S has queued messages [M1, M2] and the in-flight run just released
  the slot
- **WHEN** the drain worker observes the slot is free
- **THEN** it calls `agent.Run(M1)`, waits for that run to complete, then calls
  `agent.Run(M2)`, with each run acquiring the slot through the normal exclusivity path

#### Scenario: ErrSessionBusy on drain is retried
- **GIVEN** the drain worker dequeues M1 and calls `agent.Run`
- **WHEN** `agent.Run` returns `ErrSessionBusy` (e.g. a cron lock beats the worker)
- **THEN** the worker re-enqueues M1 at the head of the queue and retries after a short
  back-off; no error is shown to the user

#### Scenario: Slot exclusivity is enforced by atomic acquisition, not a pre-check
- **GIVEN** session S's slot is held by an in-flight run
- **WHEN** the drain worker calls `agent.Run` (with or without a prior `IsSessionBusy`
  observation, regardless of any race between the observation and the call)
- **THEN** `RunWith` returns `ErrSessionBusy` via its atomic `LoadOrStore` acquire; the
  drain worker re-queues the message at the head and retries; at no point does a second
  `RunWith` goroutine run on the same session concurrently

### Requirement: Non-ErrSessionBusy errors from the drain path are surfaced and halt the drain

When `agent.Run` returns any error other than `ErrSessionBusy`, the drain worker MUST
surface the error through the normal TUI error-reporting path. The error notification
MUST carry attribution indicating the failure originated from a queued message, so the
user understands this was not an interactive submission they just made. The drain worker
MUST halt after surfacing the first such error — it MUST NOT attempt to deliver the
remaining queued messages. The remaining queue MUST be preserved and visible in the queue
affordance, so the user can see how many messages were not delivered and choose to discard
them or allow a subsequent drain (triggered by enqueuing a new message) to retry.

#### Scenario: Provider error on a drained message
- **GIVEN** the drain worker dequeues M1 and calls `agent.Run`
- **WHEN** `agent.Run` returns a non-`ErrSessionBusy` error (e.g. a provider API or
  context-length error)
- **THEN** an attributed error notification is surfaced to the user (e.g. "Queued message
  could not be delivered: <reason>"); the drain worker halts; messages M2 … MN remain in
  the queue and are shown in the queue affordance

#### Scenario: Remaining queue visible after drain halt
- **GIVEN** the drain worker halted after an error and N messages remain in the queue
- **WHEN** the chat view renders
- **THEN** the queue affordance shows N messages; the user may discard them or trigger a
  fresh drain by enqueuing a new message

### Requirement: The drain worker lifecycle is bounded and does not leak

The system SHALL start the drain worker at most once per session on first enqueue and
MUST ensure the worker terminates when the queue is empty and no further enqueue can
arrive for that session (e.g. session switch to a new session, app shutdown). The worker
MUST NOT keep a goroutine alive after the app exits.

#### Scenario: Worker terminates after queue is drained
- **GIVEN** session S's queue becomes empty and no new messages are enqueued
- **WHEN** the drain worker finishes delivering the last message
- **THEN** the worker goroutine exits with no leak detectable under `go test -race`

#### Scenario: Worker is shut down on app exit
- **GIVEN** session S has a non-empty queue when the user quits
- **WHEN** the app shutdown path is reached
- **THEN** the worker goroutine is cancelled via its context; undelivered queued messages
  are dropped silently (no persistence, no data-loss claim — the session was never
  committed)

### Requirement: The queue has a defined overflow policy

The drain worker goroutine MUST NOT block the Bubble Tea render loop. The queue MUST use
an unbounded in-memory slice so a fast typist is never rejected after the first enqueue.
If memory growth is a concern in a future change, that bound and its back-pressure policy
MUST be specified explicitly in a follow-up requirement; this change intentionally leaves
the queue unbounded to avoid blocking the TUI event loop.

#### Scenario: Many messages enqueued while agent runs
- **GIVEN** the user submits ten messages while the session is busy
- **WHEN** each Enter keypress is processed
- **THEN** all ten are enqueued with no rejection, no blocking, and no toast

### Requirement: An explicit affordance shows queued messages and allows discard

The TUI SHALL display a visible indicator of the form "N message(s) queued" (where N ≥ 1)
while the queue for the active session is non-empty. The indicator MUST be rendered
distinctly from chat messages (not as a persisted chat bubble). The system SHALL expose a
key binding that discards all queued messages for the active session in a single
interaction. The discard key binding MUST be shown in the status or help bar while the
queue is non-empty.

#### Scenario: Queue indicator appears when session is busy and a message is queued
- **GIVEN** session S is busy and has one queued message
- **WHEN** the chat view renders
- **THEN** an indicator showing "1 message queued" (or equivalent) is visible and no
  queued message appears as a chat bubble

#### Scenario: Queue indicator disappears after drain
- **GIVEN** session S's queue is emptied by the drain worker
- **WHEN** the chat view re-renders
- **THEN** the queue indicator is no longer shown

#### Scenario: Discard key clears the queue
- **GIVEN** session S has N queued messages
- **WHEN** the user presses the discard key binding
- **THEN** all N messages are removed from the queue, the indicator disappears, and no
  messages are delivered to the agent

### Requirement: Cancel (Esc / Ctrl+C) targets the in-flight run; queued messages survive

Pressing Esc or Ctrl+C SHALL cancel the currently in-flight run for the active session
(per the existing cancel path) and SHALL leave queued messages intact. The drain worker
SHALL begin draining after the in-flight run's slot is released, delivering the surviving
queued messages in order. This is the default behavior because users commonly interrupt a
run precisely to let a freshly queued instruction take over.

#### Scenario: Esc cancels in-flight run; queue survives
- **GIVEN** session S is busy and has one queued message M
- **WHEN** the user presses Esc
- **THEN** the in-flight run is cancelled, the slot is released by the run goroutine's
  deferred cleanup, and M is delivered on the next drain cycle

#### Scenario: Discard key clears the queue after cancel
- **GIVEN** the user pressed Esc and the queue still has messages
- **WHEN** the user presses the discard key
- **THEN** the remaining queued messages are dropped and no new run starts for them

### Requirement: Flow-owned sessions and non-interactive runs are excluded

The system MUST NOT enqueue messages against sessions owned by the flow engine
(`NonInteractive = true`). The queue affordance, the enqueue path, and the drain worker
SHALL only activate for interactive (TUI-driven) sessions. This boundary is the existing
`IsInteractiveSession` gate used by the bridge dispatcher.

#### Scenario: Flow-session submission attempt is not queued
- **GIVEN** a session S is owned by the flow engine
- **WHEN** a user-initiated submit path fires for S (if reachable)
- **THEN** no message is enqueued in S's queue and the existing behavior is unchanged

### Requirement: External-editor and slash-command submits remain gated

The Ctrl+E (open external `$EDITOR`) path and `dialog.CommandRunCustomMsg` (slash-command
execution) SHALL retain their existing busy-reject guard and MUST NOT enqueue. These
paths mutate session state in ways that do not compose safely with a queued message
sequence; deferring them is a separate, explicit future decision.

#### Scenario: Ctrl+E while busy
- **GIVEN** session S is busy
- **WHEN** the user presses Ctrl+E
- **THEN** the existing warning toast is shown and no editor is opened; no enqueue occurs

#### Scenario: Slash command while busy
- **GIVEN** session S is busy
- **WHEN** a `CommandRunCustomMsg` is dispatched
- **THEN** the existing warning toast is shown and no command runs; no enqueue occurs

### Requirement: A new submission routes to the queue whenever the queue is non-empty

When a session's queue is non-empty, a new message submission MUST be appended to the
queue regardless of whether the session slot is currently held. Direct dispatch via
`agent.Run` is permitted only when the queue is empty AND the session slot is free. This
preserves FIFO ordering across the boundary between drain deliveries: a submission
arriving while the queue has entries but the slot is momentarily idle (e.g. between two
drain calls) MUST NOT bypass the already-queued messages.

#### Scenario: Submit while queue non-empty and session momentarily idle
- **GIVEN** session S's queue contains message M1 and the slot is momentarily free
  (e.g. between two drain deliveries)
- **WHEN** the user submits a new message M2
- **THEN** M2 is appended to the queue behind M1; the drain worker delivers M1 first,
  then M2; no FIFO inversion occurs

#### Scenario: Submit while queue empty and session idle
- **GIVEN** session S's queue is empty and the session slot is free
- **WHEN** the user submits a message M
- **THEN** M is dispatched directly via `agent.Run` (today's behavior, unchanged); no
  queue involvement, no added latency

### Requirement: The idle-path submit behavior is unchanged when the queue is empty

When the session's queue is empty and the session slot is free, pressing Enter MUST
dispatch the message via the existing path — persisting and calling `agent.Run` — with no
queue involvement and no added latency. Any error returned by `agent.Run` on this direct
idle path MUST be surfaced to the user as it is today. Only `ErrSessionBusy` returned
from the drain worker's retry loop is swallowed as a retryable signal; real errors on the
direct idle path MUST NOT be silently discarded.

#### Scenario: Idle-path submit reaches the agent with no queue involvement
- **GIVEN** session S's queue is empty and the slot is free
- **WHEN** the user submits a message M
- **THEN** `agent.Run` is called directly; no `EnqueueMessage` call is made; the message
  reaches the agent with the same latency as before this change

#### Scenario: Real error on idle path surfaces to the user
- **GIVEN** session S's queue is empty and the slot is free
- **WHEN** the user submits a message and `agent.Run` returns a non-`ErrSessionBusy`
  error
- **THEN** the error is surfaced via `util.ReportError` as it is today; it is NOT silently
  swallowed

### Requirement: Queue is per-session and does not follow session switches

Each session MUST have its own independent queue. Switching to a different session MUST
NOT carry the original session's queue to the new session. If the original session's
drain worker is still running, it MUST continue draining against the original session
regardless of which session is currently active in the TUI.

#### Scenario: Switch sessions while queue is non-empty
- **GIVEN** session S1 has two queued messages and the user switches to session S2
- **WHEN** the TUI renders S2
- **THEN** S2's queue is empty; S1's drain worker continues draining S1 in the background
