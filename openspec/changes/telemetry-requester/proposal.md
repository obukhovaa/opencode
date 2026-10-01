## Why

Traces of a flow run can carry who the work is for, through a `requester` flow arg mapped by `telemetry.flowArgs`. Anything that is not a flow cannot: a long-running agent behind the chat bridge has no flow args, and `telemetry` offers no other way to set trace metadata. When several people share one bridged agent, a single static value would be wrong anyway: one thread can mix authors, and each turn works for whoever sent the message.

## What Changes

- Every trace gets a `requester` metadata field, chosen per trace, first match wins: a non-blank `requester` flow arg; the per-turn requester on the run context; the new static `telemetry.requester`.
- The chat bridge sets the per-turn requester from the inbound message's author (`Inbound.AuthorID`, which the Slack, Telegram and Mattermost adapters all set). An adapter may implement a new optional `bridge.UserEmailResolver` to map the author id to an email; Slack does so via `users.info`, for workspace (`U…`) and Enterprise Grid (`W…`) ids. Results are cached for an hour; a lookup is bounded by a 3 s timeout, and a failure falls back to the raw author id and is cached for 5 minutes so a permanent error (missing scope, unknown user) does not cost a platform call and a warning on every message.
- Scheduled jobs remember who created them: `cron_jobs` gains a `requester` column, filled like the creating turn's trace (a `requester` flow arg when created inside a flow step, else the creating turn's context) and clamped to the column width, and every run of the job carries it.
- Work detached from the turn keeps its requester: async subagents, the first turn's session-title generation, and the turn a background task's completion auto-resumes (the task records its spawning turn's requester).
- New config field `telemetry.requester` (schema regenerated).

## Capabilities

### New Capabilities

- `telemetry-requester`: attribution of each trace to the person the run works for.

## Impact

- `internal/llm/tools` (context helpers, background bash / monitor task requester), `internal/llm/agent` (trace metadata, async subagent ctx and task requester, title ctx), `internal/task` (`Task.Requester`; `Deps.ResumeSession` takes the requester), `internal/app` (auto-resume ctx), `internal/bridge` (resolver interface, Slack adapter, per-turn ctx), `internal/cron` (stored requester, replay on fire), `internal/config` + schema.
- Migration `20261001120000_add_cron_jobs_requester` for SQLite and MySQL; additive, defaults to `''`, so existing jobs keep working and carry no requester. The MySQL column is `VARCHAR(320)`; `Create` clamps longer values on both backends.
- Slack: email resolution needs the `users:read` and `users:read.email` scopes on the app. Without them traces carry the Slack user id. Telegram and Mattermost traces carry the platform user id.
- No behaviour change for flows that already pass a `requester` flow arg.
