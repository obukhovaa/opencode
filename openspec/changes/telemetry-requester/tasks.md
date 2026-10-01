## 1. Trace metadata

- [x] 1.1 Context helpers `tools.WithRequester` / `tools.RequesterFromContext`
- [x] 1.2 `telemetry.requester` config field, schema and docs
- [x] 1.3 `stampRequester` in `createLangfuseTrace` with flow arg > ctx > config precedence
- [x] 1.4 Async subagents copy the requester onto their detached run ctx
- [x] 1.5 A blank `requester` flow arg counts as absent
- [x] 1.6 Title generation runs with the turn's requester
- [x] 1.7 Shared `tools.TurnRequester` (non-blank `requester` flow arg, else ctx); title generation and cron `Create` resolve through it

## 2. Chat bridge

- [x] 2.1 Optional `bridge.UserEmailResolver`; Slack implementation via `users.info` (U and Enterprise Grid W ids)
- [x] 2.2 Per-author cache with TTL; error path falls back to the author id, negative-cached for 5 minutes
- [x] 2.3 `handleInbound` runs the turn with the resolved requester
- [x] 2.4 Slack scope docs list `users:read` / `users:read.email`
- [x] 2.5 `/compact` runs the summarizer with the command author's requester

## 3. Scheduled jobs

- [x] 3.1 Migration adding `cron_jobs.requester` (SQLite, MySQL) and regenerated sqlc code
- [x] 3.2 `Create` stores the requester from params, a flow arg, or ctx, clamped to 320 bytes
- [x] 3.3 `fireJob` replays it onto the task ctx

## 4. Background-task auto-resume

- [x] 4.1 `task.Task.Requester` recorded at spawn (async task, background bash, monitor)
- [x] 4.2 `Deps.ResumeSession` takes the requester; the app's resume runs with it

## 5. Verification

- [x] 5.1 Unit tests for precedence, resolver cache, per-turn ctx, cron store and replay
- [x] 5.2 Tests driving `runAsync`, `createLangfuseTrace` (namespaced key), auto-resume, title generation (flow-arg case included), `/compact`, failure caching, W ids, cron clamp and flow-arg fallback
