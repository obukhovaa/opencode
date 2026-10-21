## Why

The bridge runs every `/`-prefixed inbound as a chat command before it resolves the session, whatever the channel (CD-5331, from the CD-5245 audit). A peer on the `external` channel, which is one end user's conversation in a tenant app, can therefore send `/sessions` to list the newest sessions of the whole project (other users, other apps), `/session <prefix>` to re-bind its conversation to any of them and receive its replies, and `/agent` or `/model` to change process-wide state for every conversation the daemon serves. Artem reproduced the listing live on dev on 2026-10-17. C3 now refuses slash text on its own routes (CD-5330), but the engine must not rely on its consumer for this.

`router_send` already keeps external conversations apart (GENAI-464, PR #96 and b37382f): a turn from an external peer may reach only that peer, and no turn may reach an external conversation it does not belong to. The chat commands should hold to the same rule. The chat-side half exists as `router.isolateExternalSessions`, but it is opt-in and neither the c2-agent nor the scenario-builder daemon sets it, so on dev a Slack user can still list and enter external conversations.

## What Changes

- On the `external` channel, only an allowlist of chat commands runs. By default that is `/stop` and `/reset` (with their aliases `/abort`, `/cancel` and `/new`). A known command that is not on the list gets a refusal reply and runs nothing. Text that is not a known command still goes to the agent as before.
- New `router.channels.external.commands` (list of command names) replaces the default allowlist. Aliases are matched by the command they stand for. Unset or empty keeps the default.
- A caller on the `external` channel can address only its own session, whatever the allowlist says: `/sessions` lists only it, `/session <prefix>` matches only it (so nothing else is shown, named or bound), and `/crons` shows only its jobs. No store query is needed for this.
- **BREAKING** `router.isolateExternalSessions` now defaults to on: a chat peer can no longer list, switch into or see the cron jobs of an external conversation's run unless the setting is explicitly `false`. The field becomes a tri-state (unset means on).

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `chat-bridge`: new requirements for the `external` channel's command allowlist and for confining an external caller's session commands to its own session; the opt-in isolation of external sessions from chat session commands becomes on by default.

## Impact

- `internal/bridge/config.go` (new `ExternalChannelConfig.Commands`, `IsolateExternalSessions` becomes `*bool`), `internal/bridge/service/{inbound,commands,session_isolation}.go`, `cmd/schema/main.go` and `opencode-schema.json`, `docs/bridge.md`, tests.
- Deployments that relied on chat peers entering external conversations through `/session` must set `router.isolateExternalSessions: false`.
- External consumers that sent `/sessions`, `/session`, `/agent`, `/model`, `/help` or other commands now get a refusal reply. C3 already refuses slash text on its routes, so C3 users see no change.
- Chat-to-chat behaviour is unchanged: a Slack, Telegram or Mattermost peer still lists and switches between non-external sessions, as `router_send` still allows chat-to-chat sends.
