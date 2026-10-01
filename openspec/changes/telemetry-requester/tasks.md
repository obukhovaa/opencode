## 1. Trace metadata

- [x] 1.1 Context helpers `tools.WithRequester` / `tools.RequesterFromContext`
- [x] 1.2 `telemetry.requester` config field, schema and docs
- [x] 1.3 `stampRequester` in `createLangfuseTrace` with flow arg > ctx > config precedence
- [x] 1.4 Async subagents copy the requester onto their detached run ctx

## 2. Chat bridge

- [x] 2.1 Optional `bridge.UserEmailResolver`; Slack implementation via `users.info`
- [x] 2.2 Per-author cache with TTL; error path falls back to the author id uncached
- [x] 2.3 `handleInbound` runs the turn with the resolved requester

## 3. Scheduled jobs

- [x] 3.1 Migration adding `cron_jobs.requester` (SQLite, MySQL) and regenerated sqlc code
- [x] 3.2 `Create` stores the requester from params or ctx
- [x] 3.3 `fireJob` replays it onto the task ctx

## 4. Verification

- [x] 4.1 Unit tests for precedence, resolver cache, per-turn ctx, cron store and replay
