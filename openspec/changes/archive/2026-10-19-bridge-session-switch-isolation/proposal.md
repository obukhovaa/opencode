## Why

`router_send` keeps external conversations apart (GENAI-464), but the chat session commands do not. `/sessions` lists every session of the project, `/session <id-prefix>` shows or re-binds the caller into any of them, and `/crons` lists every session's jobs with their task and prompt. A Slack, Telegram or Mattermost user can therefore read the title of a session serving an `external` peer (often another tenant), switch into it, receive every later agent reply of that conversation, and post into it.

## What Changes

- New opt-in `router.isolateExternalSessions` (bool, default off). Unset, every command behaves exactly as before and the store is not queried.
- When on, a session of an external conversation's run — one holding at least one binding on the `external` channel, or sharing its root session with one (a flow run's unbound sibling steps) — is:
  - left out of the `/sessions` listing;
  - left out of `/session <id-prefix>` matching, so a prefix that matches only such a session gets the ordinary "No session matches" reply, and an ambiguous-prefix listing does not name it;
  - left out of `/crons`, with its jobs.
- The caller's own current session always stays visible.
- Roots come from the session rows the commands already list; `/crons` reads the session of a job that is not among them (a subagent's) once.
- The external-bound set comes from one store query per command (`Store.ListSessionIDsByChannel`, new sqlc query `ListBridgeSessionIDsByChannel` for SQLite and MySQL). A failed lookup fails closed: only the current session stays visible, with a WARN log.

## Capabilities

### Modified Capabilities

- `chat-bridge`: new requirement for opt-in isolation of external sessions from the chat session commands.

## Impact

- `internal/bridge/config.go` (field), `internal/bridge/service/{commands,session_isolation}.go`, `internal/bridge/store` (new method), `internal/db` (generated query), `cmd/schema/main.go` + `opencode-schema.json`, `docs/bridge.md`, tests.
- No behaviour change unless the setting is on. Like the other scalar `router` fields it is read per command from the live config and has no HTTP mutator.
