## 1. Storage

- [x] 1.1 `AddSessionCost`, `RecordSessionUsage` and `RecordSessionCompaction` in `internal/db/sql/sessions.sql` (`:one`, `RETURNING *`) and `internal/db/sql/mysql/sessions.sql` (`:execrows`); remove `cost = ?` from `UpdateSession` in both. Regenerate sqlc (`sqlc.yaml`) for `internal/db` and `internal/db/mysql`.
- [x] 1.2 `Querier` gains the three methods. `MySQLQuerier` runs the exec, then `GetSessionByID` once; zero rows on a missing id returns `sql.ErrNoRows`; a read-back failure after a committed write returns an error wrapping `db.ErrWriteCommitted` (non-retryable). Drop `Cost` from the `UpdateSession` mapping.
- [x] 1.3 `session.Service.AddCost`, `RecordUsage`, `RecordCompaction`: one write statement each, only the write inside `db.WithTxRetry`, read-back never retried, `ErrCostCommitted` sentinel; publish `UpdatedEvent`. Negative delta is an error, zero delta writes nothing. `Save` stops mapping `Cost`; its doc says so.

## 2. Writers

- [x] 2.1 `TrackUsage`: one `RecordUsage` (last-call tokens, total increments, cost) replaces `Get` + `Save`; logging reads the returned row.
- [x] 2.2 Synchronous and background compaction in `agent.go`: one `RecordCompaction` with the summary message id, tokens and `inCost + outCost`.
- [x] 2.3 Hold the per-task slot through final accounting and use a fresh stored balance on resume. Release completed slots.
- [x] 2.4 Credit the positive run delta on a bounded context that survives cancellation.
- [x] 2.5 Apply the slot and delta accounting to synchronous success, error and cancellation.
- [x] 2.6 Finish asynchronous accounting and release its slot before delivering the completion. Release the slot on startup failures and panic.

## 3. Tests

- [x] 3.1 Agent: sync resume (0.32487257 + 0.09976659 adds 0.42463916), async resume, cancelled and errored runs still hand over the spend, nested subagent counted once. Real SQLite `session.Service`.
- [x] 3.2 Cover overlapping resumes, alternating workers and post-turn compaction.
- [x] 3.3 Exercise concurrent async completions alongside parent model usage and assert the combined cost.
- [x] 3.4 Session (SQLite): stale `Save` keeps `AddCost`/`RecordUsage` increments; 50 concurrent `AddCost` sum exactly; concurrent `RecordUsage` + `AddCost` sum exactly; `RecordCompaction` sets summary id and cost together; negative delta rejected.
- [x] 3.5 Session retry discipline with a fake querier: retryable write error then success is applied once; read-back failure executes the write once and returns `ErrCostCommitted`.
- [x] 3.6 MySQL (`internal/db/mysql_cost_integration_test.go`, tag `mysql_integration`): concurrent `AddSessionCost` sums exactly; `RecordSessionUsage` concurrent with `AddSessionCost` loses nothing; `UpdateSession` leaves cost; missing id returns `sql.ErrNoRows`.
- [x] 3.7 Add the three methods to every `session.Service` fake; tests that seeded `Cost` through `Save` seed through `AddCost`.

## 4. Docs

- [x] 4.1 `Cost` doc in `internal/api/handler_flow.go`, the `cost` bullets in `docs/flows.md` and a comment at the `metrics.cost` loop in `cmd/flow.go`: step cost includes subagent sessions; adding child rows counts them twice.
- [x] 4.2 Sync the delta specs into `openspec/specs/` at archive time (`subagent-cost-rollup` new, `task-async-mode` modified).

## 5. Verify

- [x] 5.1 `go test ./internal/db/... ./internal/session/... ./internal/llm/agent/... ./internal/api/... ./cmd/...`, then `make test`.
- [x] 5.2 MySQL: `make test-mysql` where Docker is available; otherwise the GitHub `test-mysql` workflow after the port. Say which in the MR.
