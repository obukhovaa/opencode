# Tasks: bridge-heartbeat

## 1. Domain

- [x] 1.1 `internal/heartbeat`: settings with defaults, `ParseCommand`, `NextBeat`
  (UTC grid, active window, weekdays), `IsSilentAck`, `AgendaIsEmpty`, `Prompt`,
  status rendering; table-driven tests

## 2. Storage

- [x] 2.1 SQLite and MySQL migrations for `bridge_heartbeats`; MySQL schema file
- [x] 2.2 sqlc queries (get, upsert, list by project) and regenerated code
- [x] 2.3 `store.Heartbeat` with `GetHeartbeat` / `PutHeartbeat` /
  `ListHeartbeats` on both stores; store tests

## 3. Bridge

- [x] 3.1 `bridge.Inbound.Heartbeat` (`json:"-"`)
- [x] 3.2 Scheduler loop: due beats, busy deferral, coalescing, empty-agenda skip
- [x] 3.3 Dispatcher quiet mode for heartbeat turns; silent-ack, header, failure
  line; outcome recorded on the row
- [x] 3.4 Model override through the agent factory, cached per model
- [x] 3.5 `/heartbeat` command and `/help` entry
- [x] 3.6 Weekly setup reminder
- [x] 3.7 `Dependencies.Heartbeat`, set by `serve` in daemon mode only

## 4. Natural language

- [x] 4.1 `heartbeat` tool (`internal/llm/tools`), opt-in manager tool with a
  late-bound configurer; registration test
- [x] 4.2 Bridge configurer (`HeartbeatStatus` / `ApplyHeartbeat` per session)
  sharing `applyHeartbeat` with the command
- [x] 4.3 `/heartbeat` falls through to the agent when the exact form does not
  parse; refused with the grammar when the agent lacks the tool
- [x] 4.4 Slots start at the window start (daily beats at a fixed time)

## 5. Docs

- [x] 5.1 Document the heartbeat in the bridge docs

## 6. Review fixes

- [x] 6.1 Reminder only to top-level DMs of inbound-active adapters
  (`bridge.DirectPeerChecker` for Slack, Mattermost, Telegram); never external,
  threads, flow/subagent or interactive sessions
- [x] 6.2 `router.heartbeatReminder` switch (default on): `bridge.Config`,
  `cmd/schema`, `opencode-schema.json`, viper round-trip test, docs
- [x] 6.3 Scheduler only for inbound-active adapters; `/heartbeat on` notes
  when scheduled beats will not run
- [x] 6.4 `/heartbeat now` and the tool's `now` queue behind the current turn;
  a manual beat waits out another actor instead of being dropped
- [x] 6.5 Late (catch-up, held-back) beats fire only inside the active window
- [x] 6.6 A human message preempts a running beat; cancellations post nothing
- [x] 6.7 Quietness per run (text guard) only; no dispatcher-wide flag
- [x] 6.8 One notice when scheduled beats start being skipped for an agenda
- [x] 6.9 Compare-and-swap claim of the dispatcher's beat
- [x] 6.10 Scheduler writes re-read the row and never undo a `/heartbeat` change
- [x] 6.11 No beat on a session an interactive flow step owns
- [x] 6.12 One beat per session per slot
- [x] 6.13 `/heartbeat model` requires a configured, enabled provider
- [x] 6.14 `chat-bridge` delta: heartbeat exceptions to tool-transition status
  and the intermediate text relay; cost and DST notes in the docs
