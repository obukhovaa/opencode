## Purpose

Provides visible acknowledgement to a chat-bridge user whose inbound message has been
accepted into the dispatcher queue but cannot start its agent run immediately because a
run is already in flight for that session. The acknowledgement tells the sender their
message is queued and will be delivered; it is updated in-place as the queue drains and
resolved the moment the message begins its run, so no stale "queued" note ever lingers.

## ADDED Requirements

### Requirement: Queued-acknowledgement on per-session inbound backlog

When an inbound message is enqueued on a `sessionDispatch.inbound` channel while a run is
already in flight for that session, the bridge SHALL send a queued-acknowledgement reply to
the message's sender if `cfg.Router.QueueAcknowledgementsEnabled` is true. The
acknowledgement SHALL identify that the message is queued and, when multiple messages are
queued, its ordinal position (e.g. "queued — 2 messages ahead").

This requirement covers only messages that reach the dispatcher's inbound channel while a
run is in flight. Messages that enter the channel while no run is active start immediately
and produce no acknowledgement.

#### Scenario: First message queues behind an in-flight run

- **GIVEN** a run is in flight for session S and `QueueAcknowledgementsEnabled` is true
- **WHEN** an inbound message M arrives from peer P and is pushed to the channel
- **THEN** the bridge sends an acknowledgement to peer P indicating M is queued (e.g.
  "⏳ Your message is queued — 1 ahead. I'll respond once the current run finishes.")

#### Scenario: Second queued message shows its position

- **GIVEN** a run is in flight for session S and one message is already queued
- **WHEN** a second inbound message arrives from peer P
- **THEN** the acknowledgement for the second message indicates 2 messages are ahead

#### Scenario: Acknowledgements disabled

- **GIVEN** `QueueAcknowledgementsEnabled` is false (or unset)
- **WHEN** an inbound message queues behind an in-flight run
- **THEN** no acknowledgement is sent; the message is silently queued as before

### Requirement: Acknowledgements are updated in-place as the queue drains

The bridge SHALL update each sender's queued-acknowledgement in-place (using the
platform's edit-message primitive) rather than posting a new message per state change, on
all platforms that support edit (Telegram, Slack, and Mattermost all do). The in-place
update SHALL reflect the current queue position as earlier messages begin their runs and
the sender's message moves up.

No emoji-reaction primitives are used. The acknowledgement is a text message that is
edited; no reaction-based design (👀 / ✅) is employed or required.

#### Scenario: Queue drains; position updates in-place

- **GIVEN** peer P's message is queued at position 3 and their acknowledgement was sent
- **WHEN** two earlier messages complete their runs (queue drains by 2)
- **THEN** the bridge edits the existing acknowledgement message for P to indicate position 1,
  rather than posting two new messages

#### Scenario: Edit fails gracefully

- **GIVEN** the adapter returns an error when editing the acknowledgement (e.g. message too
  old, scope missing)
- **THEN** the bridge logs the failure at info level and does not retry the edit; the
  original acknowledgement remains visible to the sender with stale position information;
  no new message is posted

### Requirement: Acknowledgement is resolved when the message begins its run

When the queued message is dequeued and its agent run begins, the bridge SHALL resolve the
acknowledgement by editing it to a "running" state (e.g. "▶ Processing your message now…")
or deleting it. The bridge MUST NOT leave a "queued" acknowledgement visible after the run
has started.

#### Scenario: Run starts; acknowledgement resolved

- **GIVEN** peer P's message was queued and the acknowledgement was sent
- **WHEN** the dispatcher dequeues P's message and `agent.Run` is called
- **THEN** before blocking on the run, the bridge edits the acknowledgement to indicate the
  run has started (or deletes it); the "queued" state is no longer visible to peer P

#### Scenario: Run fails to start (non-busy error); acknowledgement resolved

- **GIVEN** peer P's message was queued and the acknowledgement was sent
- **WHEN** `agent.Run` returns an error other than `ErrSessionBusy`
- **THEN** the bridge edits or deletes the queued acknowledgement and sends the normal
  run-failure reply; no stale "queued" notice lingers

### Requirement: Short-wait threshold before announcing

The bridge SHALL NOT send a queued-acknowledgement when the queue depth at the time of
enqueueing is zero (i.e. the message is the only one queued) AND the run that is blocking
it has been running for less than a configurable short-wait threshold (default 2 seconds).
This avoids a pointless "queued" flash for inbound messages that start within human
reaction time. If the run is still in flight after the threshold, the bridge sends the
acknowledgement.

The threshold is an internal implementation decision for v1 — it MAY be hardcoded at 2
seconds and need not be config-exposed.

#### Scenario: Message queues for a sub-threshold wait

- **GIVEN** a run has been in flight for 0.5 seconds and one inbound queues
- **WHEN** the run completes within the short-wait threshold
- **THEN** no acknowledgement was ever sent to the sender

#### Scenario: Message queues for a long wait (threshold exceeded)

- **GIVEN** a run has been in flight for 30 seconds when an inbound queues
- **THEN** the acknowledgement is sent immediately (threshold already exceeded)

### Requirement: Acknowledgement is sent to the sender only in group/multi-peer sessions

In sessions bound to multiple peers (group channels or multi-reviewer flows), the queued
acknowledgement SHALL be sent to the specific peer whose inbound was queued, not
broadcast to all bound peers. Other peers' conversations MUST NOT be cluttered with
acknowledgements for messages they did not send.

#### Scenario: Multi-peer session; ack goes to sender only

- **GIVEN** session S is bound to Alice (Slack) and Bob (Telegram) and a run is in flight
- **WHEN** Alice sends a message that queues
- **THEN** Alice receives the acknowledgement in her DM; Bob's conversation is unchanged

### Requirement: Queue-acknowledgement feature is config-gated

The queued-acknowledgement behavior SHALL be controlled by a boolean field
`QueueAcknowledgementsEnabled` on the bridge's `RouterConfig` struct (or equivalent name
consistent with the existing `ToolUpdatesEnabled` precedent). When false or absent, the
bridge behaves as before this change — no acknowledgements are sent, messages queue
silently.

Adding this field to `RouterConfig` triggers all four CLAUDE.md schema obligations:
(1) update `cmd/schema/main.go`, (2) regenerate `opencode-schema.json`, (3) update
`docs/bridge.md`, (4) add a Viper round-trip unit test in `internal/config/`.

#### Scenario: Field absent in .opencode.json

- **GIVEN** `.opencode.json` contains a `router` section without `queueAcknowledgementsEnabled`
- **THEN** the field defaults to false; no acknowledgements are sent; Viper's case-fold
  does not mangle the absent key
