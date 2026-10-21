## Context

`Service.dispatchInbound` (`internal/bridge/service/inbound.go`) splits any `/`-prefixed text into a command and calls `handleChatCommand` before it resolves the binding. The only channel filter is `commandAvailableForChannel`, which hides `/pair` outside Telegram. The session commands then decide visibility with `chatSessionVisible` / `cronSessionVisible` (`session_isolation.go`), which only act when `router.isolateExternalSessions` is on, and only protect external sessions from chat peers: an external caller sees every chat session and, with the setting off, every other external conversation.

`router_send` (GENAI-464) already states the rule this change applies to commands: an external turn reaches only its own peer, and no turn reaches an external conversation it does not belong to; chat-to-chat is unchanged.

## Goals / Non-Goals

**Goals:**

- An external peer can run only `/stop` and `/reset` unless an operator widens the list.
- An external peer's session commands can never show, name or bind a session other than its own, even if an operator allows them.
- A chat peer cannot reach an external conversation through the session commands unless an operator opts out.

**Non-Goals:**

- Scoping chat peers to "their own" chat sessions. The bridge keeps no history of which sessions a chat peer was bound to, and `/sessions` + `/session` exist to switch between them. `router_send` leaves chat-to-chat alone for the same reason.
- An allowlist for chat channels. Only `external` needs one now; the field sits on the external channel's config and can be repeated on the others later.
- The HTTP `/router/*` API, which is authenticated separately.

## Decisions

**Refuse with a reply, not fall through.** A known command that is not allowed replies "`/<cmd>` is not available on this channel" and stops. Falling through (what `/pair` does off Telegram) would hand `/sessions` to the agent as a prompt, spend a model turn and leave the consumer guessing. Text whose first word is not a known command still falls through, so `/etc/hosts …` keeps working.

**Aliases are matched by their canonical command.** `stop` and `cancel` map to `abort`, `new` to `reset`, both for the inbound command and for each allowlist entry. An operator who writes `["stop", "reset"]` then also gets `/cancel` and `/new`, which run the same handlers; listing aliases one by one would be a trap. Entries are trimmed, lower-cased and may carry a leading `/`. Unknown entries match nothing.

**Unset or empty list means the default.** Viper does not reliably tell `[]` from an absent key, so "empty disables all commands" would behave differently between JSON and the loader. Nobody has asked for that, so empty keeps the default.

**External callers see only their own session, with no store query.** A peer has exactly one binding row, so "the sessions bound to this peer" is the current session. The visibility predicates take the caller's channel; for `external` they return `id == currentID` before reading anything, so neither the setting nor a store failure can widen it. This is checked in the predicates, not in the allowlist, so it holds even when an operator allows `/sessions` on `external`.

**`isolateExternalSessions` becomes `*bool`, unset meaning on.** The same shape as `HeartbeatReminder`. `ExternalSessionsIsolated()` returns true for a nil config or a nil field, so code paths keep calling one method. The schema default becomes `true`. This is a separate commit so a reviewer can drop it and keep the external-side fix.

## Risks / Trade-offs

- [A chat user who used `/session` to step into an external conversation for debugging loses that by default] → set `router.isolateExternalSessions: false`; documented in `docs/bridge.md`.
- [An external consumer that relied on `/help` or `/sessions`] → none known: C3 refuses slash text since CD-5330, and the c2-agent orchestrator code sends no slash commands of its own. An operator can add them back with `router.channels.external.commands`.
- [With the default flipped, every chat `/sessions` and `/session` does one more store query] → one `SELECT DISTINCT session_id` filtered by project and channel per command, as already happens on deployments that set the flag.

## Migration Plan

Ship in the fork and roll the dev daemon onto the new build. No data migration. Rollback is the previous image; to keep the new build but restore the old chat behaviour, set `router.isolateExternalSessions: false`.
