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
registered in this process that is inbound-active (it owns the bot's own
connection), and never the `external` relay. The identity lock that already
guarantees one inbound-active adapter per identity across processes therefore
also guarantees one scheduler per heartbeat, with no second leader election.

A mediated adapter (`inbound: "disabled"`, the orchestrator owns the connection)
takes no identity lock, so several processes can register it at once; their
bindings are not scheduled. A daemon whose bots are all mediated, as c2-agent's
default Slack and Mattermost apps are, therefore gets no scheduled beats.
`/heartbeat on` on such a chat says so; `/heartbeat now` still runs a beat,
because the human asked this process for it.

### D3. State lives in the database; the bridge is the only writer

A `bridge_heartbeats` row holds the state (`unset` | `on` | `off`), the tuning
(`every_seconds`, active window as minutes after UTC midnight, `weekdays_only`,
`model`, `agenda_file`) and the bookkeeping (`next_beat_at`, `last_beat_at`,
`last_status`, `last_error`, `reminded_at`). Defaults (hourly, all hours, every
day, the agent's own model, `HEARTBEAT.md`) are code constants, applied when a
column is NULL. There is no `.opencode.json` block for the heartbeat itself: the
human configures it from the chat, and the setting survives redeploys. The one
operator setting is `router.heartbeatReminder` (default on), which turns the
setup reminder off for a process (D7). Both the exact
`/heartbeat` form and the agent's `heartbeat` tool go through the same
`applyHeartbeat`, so validation lives in one place (`heartbeat.ParseCommand`).

The scheduler and the dispatcher work from a row they read earlier. To never
undo a concurrent `/heartbeat` change, their writes re-read the row first and
change only the fields their path owns (`next_beat_at`, the last-beat fields);
they write nothing when the row's state or settings differ from what they read.
A scheduled beat that loses its session puts `next_beat_at` back only while it
still holds the value the scheduler stored when it queued the beat. This keeps
the whole-row upsert; no conditional SQL is needed.

### D4. Scheduling

- The scheduler ticks every 30 seconds.
- A beat is due when `state = on` and `next_beat_at <= now`.
- **Busy defers.** If the session is running (`IsSessionBusy`), a message is
  queued for it, an interactive flow step owns it, or a beat for this dispatcher
  is already queued or running, the due beat is left for a later tick, without
  advancing `next_beat_at`. A human message therefore always runs first, and a
  scheduled beat never waits in the queue behind one. The dispatcher's one-beat
  claim is taken with a compare-and-swap before any check, so a tick and a
  `/heartbeat now` cannot both queue a beat.
- **Late beats respect the window.** A due beat fires only while now is inside
  the active hours of an active day (`heartbeat.InWindow`, days counted as in
  `NextBeat`). A catch-up after an overnight redeploy, or a beat a busy session
  held past the window's end, moves to the next allowed slot instead.
- **One beat per session.** A session bound to several chats has one row per
  binding. Once one row of a session fires (or is skipped) in a tick, the
  session's other due rows move to their next slot without firing. The report
  goes through the normal terminal path, which already reaches every bound
  peer.
- **Coalescing.** When a beat fires, `next_beat_at` is computed from *now*, not
  from the missed due time, so any number of missed beats becomes exactly one.
- **Active window.** `next_beat_at` is the first slot at or after now. Each
  day's slots start at the window start (00:00 UTC without a window) and repeat
  every interval while inside it, so `every 24h hours 07-08` is a daily 07:00
  beat and `every 2h hours 07:30-21` beats at 07:30, 09:30, …. Hours are
  `[start, end)` in UTC and may wrap midnight (`hours 22-06`). `days weekdays`
  excludes windows that start on Saturday or Sunday (UTC).
- **On `on`** the first beat is the next slot. `/heartbeat now` fires one beat
  without changing the schedule. It skips the busy gate: sent mid-turn, or from
  the `heartbeat` tool (whose call is always inside the session's own turn, so
  the session always reads as busy), the beat is queued behind the current turn
  and the reply says so. If the beat then finds another actor holding the
  session, it waits in the dispatcher's busy-retry loop like a message (without
  a queued-ack) instead of going back to the scheduler, which would drop it when
  the heartbeat is not on; when the retry budget runs out it posts one failure
  line.

### D5. The heartbeat turn

The prompt is a fixed preamble naming the UTC time and the agenda file, telling
the agent this is a scheduled turn, not a human message, and that it should
reply exactly `HEARTBEAT_OK` when nothing needs the human's attention.

- **Empty agenda skips the beat.** If the agenda file is missing or contains only
  whitespace, markdown headings and list markers, the beat is skipped without a
  model call (`last_status = skipped`). When a scheduled beat starts being
  skipped for a reason (the last outcome was not a skip with the same reason),
  the binding gets one notice: on a daemon whose working directory is wiped at
  redeploy, the agenda disappears and the heartbeat would otherwise stay on and
  silent forever.
- **The file is read, not inlined.** The agent opens the file itself. This keeps
  the prompt small and lets the agent update the file during the beat.
- **Model override.** When `model` is set, the beat runs on an agent instance
  built by the agent factory with a `ModelOverride` for the primary agent,
  cached per model ID. `/heartbeat model` accepts a supported model whose
  provider is configured and enabled (what the factory needs), and its reply
  warns that a different model cannot reuse the session's prompt cache.

### D6. Quiet rendering

A heartbeat turn renders quietly. Quietness is a property of the run, carried
by its text guard with every part event it forwards, not a dispatcher-wide
flag: the parts goroutine can lag, so a late event of the human run before a
beat must still render, and a late event of the beat must stay quiet after it.

- no queued-ack, no progress card, no tool-call cards and no intermediate text;
- the final reply is checked: `HEARTBEAT_OK` alone (or as the first or last line
  of a reply of at most 300 characters) is dropped (`last_status = silent`);
- anything else is posted with a `💓 Heartbeat HH:MMZ` header line
  (`last_status = ok`);
- a terminal error posts one line, `💓 Heartbeat HH:MMZ failed: <reason>`
  (`last_status = error`, `last_error` set). Unlike cron, a failed beat is not
  silent. A cancellation is not a failure: it posts nothing and is recorded as
  `skipped`. The agent ends a run cancelled while the model streams with an
  error, but one cancelled while a tool runs with a response: the text the
  model wrote before the tool call and a `canceled` finish. That response is a
  cancellation too, and its text is not posted.

### D6a. A message preempts a beat

A beat is quiet, so a human message queued behind a running beat would wait with
no sign of life until the beat ends. When `dispatchInbound` queues a human
message on a dispatcher whose current turn is a beat, it cancels the beat's run
on the agent instance the beat runs on (a model-override beat runs on its own
instance). The dispatcher records that instance only while the beat's `Run` is
in flight, so the cancel can never stop another actor's run on the session. A
beat whose run has not started yet sees the preempted flag and does not start,
or cancels itself right after `Run` returns. The beat posts nothing, is
recorded as `skipped` (`preempted by a message`), and keeps its `next_beat_at`:
the slot is spent, not handed back. A beat that completed before the cancel
could stop it is reported as usual.

### D7. The setup reminder

On the first scheduler tick after an identity's adapter is registered, every
binding of that identity whose heartbeat state is `unset` (including "no row")
and whose `reminded_at` is NULL or older than seven days gets one message
explaining `/heartbeat on`, a tuned example, and `/heartbeat off`. `reminded_at`
is written *before* the post, so a crash or a failed post cannot cause a burst
of repeats. Placeholder bindings with an empty session (bound but never talked
to) are skipped. `on` and `off` both end the reminders for good; tuning settings
alone does not.

The bridge's database project can be shared: in c2-agent, daemon, flow and pool
pods all use one MySQL project, so an identity's bindings include channel and
@-mention threads, other pods' flow-review threads, external (c3) bindings and
chats of mediated bots. The reminder is therefore scoped to conversations this
daemon serves:

- the adapter is inbound-active in this process and is not the `external` relay;
- the peer is not a thread (`|` in the peer ID) and the adapter reports it as a
  direct message through the optional `bridge.DirectPeerChecker` (Slack: a `D…`
  channel without a thread; Mattermost: a channel the server reports as type
  `D`; Telegram: a positive, private chat ID). An adapter without it gets no
  reminder. Mattermost DM conversations that start from an inbound post are
  thread peers (`channel|root`), so on Mattermost only bare DM bindings are
  reminded;
- the session's root is itself (not a flow step's or subagent's session) and no
  interactive flow step owns it.

`router.heartbeatReminder: false` turns the reminder off for the process; unset
means on.

### D8. Natural language through the agent

People describe schedules ("every half hour on weekdays, 7 to 23 Oslo time")
and checks ("keep an eye on my merge requests") in their own words, so a rigid
grammar fails them. `/heartbeat <args>` is therefore handled in two tiers:

- **Exact form.** If `heartbeat.ParseCommand` accepts the arguments, the bridge
  applies them itself. A bare `/heartbeat`, `on` and `off` stay instant and cost
  no model call, which matters on a large session.
- **Natural language.** Otherwise the command handler returns nothing and the
  inbound is rewritten to a prompt (`heartbeat.AgentRequest`) carrying the
  request and the current status. It runs as an ordinary human turn. The agent
  maps it to the `heartbeat` tool's typed settings, converting times to UTC
  (and asking when the zone is unclear), and writes any checks into the agenda
  file with its file tools.

The `heartbeat` tool takes typed, optional fields (`state`, `every`, `hours`,
`days`, `model`, `file`, `now`) and renders them into the exact grammar, so the
tool and the command share one parser. It acts on every chat binding of the
calling session. It is a manager tool and default-deny like the cron tools:
an agent opts in with `"heartbeat": true`. The bridge handle is resolved at
call time, because `serve` installs it after the primary agents' tool sets are
built. If the active agent does not have the tool, a natural-language request
is refused with the exact grammar instead of being sent to an agent that
cannot carry it out.

### D9. Daemon mode only

`Dependencies.Heartbeat` is set by `serve` only when it runs neither `--flow`
nor `--pool-mode`. With it unset there is no scheduler, no reminder, and
`/heartbeat` answers that heartbeats are not available in this mode.

## Risks

- **Context growth and cost.** Every non-skipped beat re-reads the session
  (from the prompt cache only on the session's own model and while the cache is
  warm) and adds a turn to it. The empty-agenda skip, the silent ack and the
  active window are the cost controls. Auto-compaction applies to beats as to
  any turn and stays the long-run backstop.
- **Daylight saving time.** Windows are UTC and do not move with local DST, so
  local beat times shift by an hour twice a year. The agent can re-apply the
  window when asked.
- **Clock skew across processes.** Irrelevant here: one process per identity
  (D2).
