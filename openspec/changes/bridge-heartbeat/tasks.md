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

## 4. Docs

- [x] 4.1 Document the heartbeat in the bridge docs
