## Why

Traces of a flow run can carry who the work is for, through a `requester` flow arg mapped by `telemetry.flowArgs`. Anything that is not a flow cannot: a long-running agent behind the chat bridge has no flow args, and `telemetry` offers no other way to set trace metadata. When several people share one bridged agent, a single static value would be wrong anyway: one thread can mix authors, and each turn works for whoever sent the message.

## What Changes

- Every trace gets a `requester` metadata field, chosen per trace, first match wins: a `requester` flow arg; the per-turn requester on the run context; the new static `telemetry.requester`.
- The chat bridge sets the per-turn requester from the inbound message's author. An adapter may implement a new optional `bridge.UserEmailResolver` to map the author id to an email; Slack does so via `users.info`. Results are cached for an hour; a lookup failure falls back to the raw author id and never blocks the turn.
- Scheduled jobs remember who created them: `cron_jobs` gains a `requester` column, filled from the creating turn's context, and every run of the job carries it.
- Detached async subagents inherit the parent turn's requester.
- New config field `telemetry.requester` (schema regenerated).

## Capabilities

### New Capabilities

- `telemetry-requester`: attribution of each trace to the person the run works for.

## Impact

- `internal/llm/tools` (context helpers), `internal/llm/agent` (trace metadata, async subagent ctx), `internal/bridge` (resolver interface, Slack adapter, per-turn ctx), `internal/cron` (stored requester, replay on fire), `internal/config` + schema.
- Migration `20261001120000_add_cron_jobs_requester` for SQLite and MySQL; additive, defaults to `''`, so existing jobs keep working and carry no requester.
- Slack: email resolution needs the `users:read` and `users:read.email` scopes on the app. Without them traces carry the Slack user id.
- No behaviour change for flows that already pass a `requester` flow arg.
