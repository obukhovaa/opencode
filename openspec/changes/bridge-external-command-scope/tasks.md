## 1. External callers address only their own session

- [x] 1.1 `chatSessionVisible` and `cronSessionVisible` take the caller's channel and, for `external`, return "only the current session" without a store or session read; `/sessions`, `/session` and `/crons` pass it, and `/crons` filters for external callers whatever `isolateExternalSessions` says. Verify with bridge tests: an external peer's `/sessions` lists only its session, `/session <prefix>` of another session (unique and ambiguous) neither binds nor names it, `/crons` shows only its jobs, with the setting on and off.
- [x] 1.2 `docs/bridge.md` chat-commands section states the external rule; verify by reading the rendered section.

## 2. External channel command allowlist

- [ ] 2.1 `ExternalChannelConfig.Commands` (`router.channels.external.commands`), a nil-safe accessor returning the canonical allowed set (default `abort`, `reset`; aliases folded; entries trimmed, lower-cased, leading `/` dropped); schema entry in `cmd/schema/main.go` and regenerated `opencode-schema.json`. Verify with `make schema-check`, `go test ./cmd/schema/...`, and a viper round-trip test under `internal/config/`.
- [ ] 2.2 `handleChatCommand` refuses a known command not on the external allowlist with a reply naming the command, runs allowed ones, and leaves unknown commands to fall through; `/help` on `external` lists only allowed commands. Verify with a bridge test driving `dispatchInbound`/`handleChatCommand`: `/sessions`, `/session`, `/agent`, `/model`, `/help` refused by default with no binding change and no agent/model change; `/stop`, `/cancel`, `/reset`, `/new` run; a configured list is honoured; Slack is unaffected; `/etc/hosts …` falls through.
- [ ] 2.3 `docs/bridge.md`: the new field in the router/external config table and the chat-commands section.

## 3. Chat-side isolation on by default

- [ ] 3.1 `IsolateExternalSessions` becomes `*bool`; `ExternalSessionsIsolated()` is true for a nil config, an unset field or `true`; schema default `true` and regenerated schema; viper round-trip covers unset/true/false. Verify with `go test ./internal/config/... ./internal/bridge/...` and `make schema-check`.
- [ ] 3.2 Update the isolation tests and `docs/bridge.md` for the new default; verify the "unset hides `E1`" and "`false` lists `E1`" cases pass.

## 4. Integration

- [ ] 4.1 `make test` passes (all tests and formatters).

## Workflow follow-up

- Sync the delta into `openspec/specs/chat-bridge/spec.md` and archive the change once the PR is approved.
