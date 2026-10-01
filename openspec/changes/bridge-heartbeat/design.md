# Design: bridge-heartbeat

## Context

The bridge already owns everything a heartbeat needs: the binding that ties a
chat peer to a session, the per-session dispatcher that serialises agent runs
and relays their final reply, the chat-command surface, and the adapter that can
post to the peer. A heartbeat is therefore modelled as a scheduled, synthetic
inbound on a binding, not as a new kind of agent run.

## Decisions

### D1. A beat is a synthetic inbound through the session dispatcher

The scheduler builds a `bridge.Inbound` for the binding's peer with
`Heartbeat: true` and the heartbeat prompt as its text, and pushes it onto the
session's dispatcher. That buys, for free:

- **Delivery.** `handleInbound` already relays the run's terminal message to
  every bound peer. Self-started turns have no other relay path.
- **Exclusivity.** One run per session at a time; a beat can never overlap a
  human turn or another beat.
- **Context.** The beat runs in the bound session, so a human reply to the
  beat's report lands in the same conversation that wrote it.

`Inbound.Heartbeat` is `json:"-"`: it is set only in-process by the scheduler and
can never arrive over `/router/inbound`.

**Alternative rejected:** a separate session per beat. It keeps the main context
smaller, but the report then has no conversation behind it when the human
answers, and every beat would rebuild a fresh session's tool set and system
prompt.

### D2. The binding is the unit, the adapter lock is the leader

Settings are keyed by the binding key `(project_id, channel, identity_id,
peer_id)`. The scheduler only considers bindings whose identity has an adapter
registered in this process. The identity lock that already guarantees one
adapter per identity across processes therefore also guarantees one scheduler
per heartbeat, with no second leader election.

### D3. State lives in the database; `/heartbeat` is the only writer

A `bridge_heartbeats` row holds the state (`unset` | `on` | `off`), the tuning
(`every_seconds`, active window as minutes after UTC midnight, `weekdays_only`,
`model`, `agenda_file`) and the bookkeeping (`next_beat_at`, `last_beat_at`,
`last_status`, `last_error`, `reminded_at`). Defaults (hourly, all hours, every
day, the agent's own model, `HEARTBEAT.md`) are code constants, applied when a
column is NULL. There is no `.opencode.json` block: the human configures the
heartbeat from the chat, and the setting survives redeploys.

### D4. Scheduling

- The scheduler ticks every 30 seconds.
- A beat is due when `state = on` and `next_beat_at <= now`.
- **Busy defers.** If the session is running (`IsSessionBusy`) or a beat for this
  dispatcher is already queued or running, the due beat is left for a later
  tick, without advancing `next_beat_at`. A human message therefore always runs
  first, and a beat never waits in the queue behind one.
- **Coalescing.** When a beat fires, `next_beat_at` is computed from *now*, not
  from the missed due time, so any number of missed beats becomes exactly one.
- **Active window.** `next_beat_at` is the first slot on the interval grid at
  or after now that falls inside the window. The grid is anchored at 00:00 UTC
  (so `every 1h` beats on the hour, `every 30m` on :00 and :30). Hours are
  `[start, end)` in UTC and may wrap midnight (`hours 22-06`). `days weekdays`
  excludes Saturday and Sunday (UTC).
- **On `on`** the first beat is the next slot. `/heartbeat now` fires one beat
  immediately (still deferring when busy) without changing the schedule.

### D5. The heartbeat turn

The prompt is a fixed preamble naming the UTC time and the agenda file, telling
the agent this is a scheduled turn, not a human message, and that it should
reply exactly `HEARTBEAT_OK` when nothing needs the human's attention.

- **Empty agenda skips the beat.** If the agenda file is missing or contains only
  whitespace, markdown headings and list markers, the beat is skipped without a
  model call (`last_status = skipped`).
- **The file is read, not inlined.** The agent opens the file itself. This keeps
  the prompt small and lets the agent update the file during the beat.
- **Model override.** When `model` is set, the beat runs on an agent instance
  built by the agent factory with a `ModelOverride` for the primary agent,
  cached per model ID. The `/heartbeat model` reply warns that a different model
  cannot reuse the session's prompt cache.

### D6. Quiet rendering

While a heartbeat turn runs, its dispatcher is in quiet mode:

- no queued-ack, no progress card and no tool-call cards;
- the final reply is checked: `HEARTBEAT_OK` alone (or as the first or last line
  of a reply of at most 300 characters) is dropped (`last_status = silent`);
- anything else is posted with a `💓 Heartbeat HH:MMZ` header line
  (`last_status = ok`);
- a terminal error posts one line, `💓 Heartbeat HH:MMZ failed: <reason>`
  (`last_status = error`, `last_error` set). Unlike cron, a failed beat is not
  silent.

### D7. The setup reminder

On the first scheduler tick after an identity's adapter is registered, every
binding of that identity whose heartbeat state is `unset` (including "no row")
and whose `reminded_at` is NULL or older than seven days gets one message
explaining `/heartbeat on`, a tuned example, and `/heartbeat off`. `reminded_at`
is written *before* the post, so a crash or a failed post cannot cause a burst
of repeats. Placeholder bindings with an empty session (bound but never talked
to) are skipped. `on` and `off` both end the reminders for good; tuning settings
alone does not.

### D8. Daemon mode only

`Dependencies.Heartbeat` is set by `serve` only when it runs neither `--flow`
nor `--pool-mode`. With it unset there is no scheduler, no reminder, and
`/heartbeat` answers that heartbeats are not available in this mode.

## Risks

- **Context growth.** Every non-skipped beat adds a turn to the main session.
  The empty-agenda skip, the silent ack and the active window are the cost
  controls. Compaction stays the long-run backstop.
- **Clock skew across processes.** Irrelevant here: one process per identity
  (D2).
