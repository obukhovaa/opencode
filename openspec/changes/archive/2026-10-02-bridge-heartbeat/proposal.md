# Proposal: bridge-heartbeat

## Why

A daemon agent bound to a chat is only awake when its human writes to it. Agents
that are meant to watch things on their human's behalf (tickets, merge requests,
CI, a team channel) work around this by building their own heartbeat out of the
tools they have: a `monitor` running `while true; do sleep 3600; echo HEARTBEAT;
done`, plus cron jobs that check a liveness file to see whether the loop is
still alive. In practice that hand-built heartbeat fails in five ways:

1. **It dies on every restart.** `monitor` processes live in the opencode
   process's in-memory task registry. A redeploy, a pod restart or `/reset`
   kills the loop, and nothing re-arms it until the human happens to send a
   message.
2. **It duplicates.** Re-arming is a prompt instruction, so a second loop gets
   started beside the first, and agents end up writing their own lock scripts.
3. **Its reports are lost.** A turn the agent starts itself (a monitor event
   resuming the session) has no path to the chat surface; only turns that answer
   an inbound message are relayed. Agents fall back to posting through a
   separate chat-write tool.
4. **Watchdogs need watching.** The liveness checks are themselves cron jobs the
   agent has to recreate after a reset.
5. **The schedule is prompt text.** Interval, quiet hours and what each beat does
   are spread across the agent's instructions and its private notes.

Other agent harnesses ship the heartbeat as a runtime feature (a stored schedule,
a checklist file, a silent "nothing to report" reply, active hours, and missed
beats coalesced into one). This change does the same for opencode's daemon mode.

## What Changes

- **A heartbeat per chat binding, owned by the bridge.** When enabled, the bridge
  wakes the binding's session on a schedule (default: every hour) by queueing a
  heartbeat turn through the same per-session dispatcher that handles inbound
  messages. The turn runs in the bound (main) session, so the agent sees the
  conversation and the human can reply to a beat's report directly.
- **Off until the human turns it on.** Nothing beats until someone in the chat
  runs `/heartbeat on`. The schedule is stored in a new `bridge_heartbeats`
  table, so it survives restarts, redeploys and `/reset`.
- **`/heartbeat` in natural language.** `/heartbeat every half hour on weekdays,
  7 to 23 Oslo time, and watch my merge requests` goes to the agent, which applies
  it with a new opt-in `heartbeat` tool (typed settings, UTC) and writes what to
  check into the agenda file.
- **An exact form, without a model call:** `status` (default), `on`, `off`, `now`,
  and tuning: `every <duration>`, `hours <HH-HH>|all` (UTC), `days weekdays|all`,
  `model <id>|default`, `file <path>|default`. Several settings can be given in one
  command. Anything that does not parse as the exact form is natural language.
- **A checklist file.** Each beat's prompt tells the agent to read its heartbeat
  file (default `HEARTBEAT.md` in the working directory). A missing or empty
  file skips the beat without a model call.
- **Silent beats stay silent.** A beat whose reply is `HEARTBEAT_OK` posts
  nothing. Heartbeat turns post no queued-ack, no progress card, no tool-call
  cards and no intermediate text; only the final reply (with a short
  `💓 Heartbeat HH:MMZ` header) or a one-line failure reaches the chat. A beat
  that starts being skipped for a missing agenda says so once.
- **Messages come first.** A human message that arrives while a beat runs
  cancels the beat; the cancelled beat posts nothing.
- **Busy sessions defer, missed beats coalesce.** A beat that comes due while the
  session is running waits until it is idle. Beats missed while the process was
  down become a single catch-up beat, which fires only inside the active hours.
  A session bound to several chats gets one beat per slot. `/heartbeat now`
  during a turn runs when the turn ends.
- **A once-a-week setup reminder.** When a daemon starts and a binding has never
  had its heartbeat turned on or off, the bridge posts a short message explaining
  `/heartbeat on` and `/heartbeat off`. It is repeated at most once every seven
  days, and never again once the human has chosen either. It reaches only the
  daemon's own top-level direct messages, never channels, threads, flow sessions,
  the external relay or a mediated bot's chats, and `router.heartbeatReminder:
  false` turns it off.
- **Daemon mode only, and only for bots the process owns.** Flow runners and pool
  pods, which also run the bridge, get no heartbeat and no reminder. A daemon
  schedules beats only for an adapter that is inbound-active in its process; a
  daemon whose bots are mediated by the orchestrator gets no scheduled beats.

## Capabilities

### New Capabilities

- `bridge-heartbeat`: scheduled heartbeat turns for chat-bound daemon sessions,
  the `/heartbeat` command, the setup reminder and their storage.

## Non-goals

- Running beats in a separate session. A beat runs in the bound session by
  design: the human replies to its report there, and the agent needs the
  conversation to judge what is new.
- Time-zone settings. Active hours are UTC.
- Event-driven wakes (a webhook that triggers an immediate beat).

## Impact

- New package `internal/heartbeat` (settings, schedule, command parsing, prompt).
- `internal/bridge/service`: heartbeat scheduler, `/heartbeat` command, reminder,
  quiet rendering for heartbeat turns, the configurer behind the tool.
- `internal/llm/tools` and `internal/llm/agent`: the `heartbeat` tool, opt-in like
  the cron tools, with a late-bound bridge handle.
- `internal/bridge/store` and `internal/db`: `bridge_heartbeats` table (SQLite and
  MySQL migrations, sqlc queries).
- `cmd/serve.go`: enables the heartbeat only for daemon mode.
- `internal/bridge`: optional `DirectPeerChecker` adapter contract (Slack,
  Mattermost, Telegram implement it); `router.heartbeatReminder` in
  `bridge.Config`, `cmd/schema` and `opencode-schema.json`.
- `chat-bridge` spec: heartbeat exceptions to tool-transition status and the
  intermediate text relay.
