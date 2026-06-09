## ADDED Requirements

### Requirement: HTTP surface mounted on existing opencode mux under `/router/*`

The bridge SHALL mount all HTTP routes on opencode's existing API mux (`internal/api/server.go`). The bridge MUST NOT start a second HTTP server, and MUST reuse opencode's existing auth middleware and the localhost-only default network posture. All bridge routes MUST live under the `/router/*` namespace. The bridge MUST NOT expose bare `/send`, `/identities/*`, or `/config/groups` paths — the only published external consumer (the `openrouter-communication` skill) is updated in lockstep with this change.

#### Scenario: Routes available under /router/ on the existing port

- **WHEN** `opencode serve --port 3456` runs with the bridge enabled
- **THEN** `POST /router/send`, `/router/identities/*`, `/router/bind`, `/router/unbind`, `/router/config/groups` are reachable on port 3456

#### Scenario: Bare paths return 404

- **WHEN** a client calls `POST /send` (the TS-bridge-era bare path)
- **THEN** the response is 404 — no implicit alias is provided

#### Scenario: No second listener

- **WHEN** the bridge starts
- **THEN** no additional `net.Listen` call occurs in `internal/bridge/`; routes are registered on the existing mux

### Requirement: POST /router/send for proactive delivery

The bridge SHALL expose `POST /router/send` matching the functional contract of the TS bridge's `/send` endpoint at `health.ts`, with two intentional differences:

1. No `autoBind` field — explicit `/router/bind` is the only binding path.
2. Path is namespaced under `/router/`.

Request body specifies destination (channel, identity, peerId), message content, optional mention prefix, and optional attachments. Response includes per-recipient delivery status. Tests MUST cover the scenarios in `health-send.test.js` (446 LOC), adapted to the new path.

#### Scenario: Send text to a configured peer

- **WHEN** `POST /router/send` is called with `{channel:"slack", identity:"default", peerId:"D012345", text:"hi"}`
- **THEN** the request is dispatched to the Slack adapter and the response reports per-peer success/failure

#### Scenario: Send to a Slack user ID (auto-DM resolution)

- **WHEN** `POST /router/send` is called with `{channel:"slack", identity:"default", peerId:"U01ABC123", text:"hi"}`
- **THEN** the bridge calls `conversations.open` to obtain a DM channel and posts the message there; the response reports the resolved DM channel ID in `deliveredTo`

#### Scenario: Send with file attachments

- **WHEN** the request body includes a `files` array with local paths
- **THEN** the adapter uploads each file (subject to platform size limits per the adapter spec) and the response reports per-file delivery status

#### Scenario: Send with mention prefix

- **WHEN** the request body includes `{mention: "<@U01ABC>", text: "review please"}`
- **THEN** the outbound message text is `<@U01ABC> review please` (mention prepended)

#### Scenario: autoBind field rejected

- **WHEN** the request body includes `{autoBind: true}`
- **THEN** the request fails with 400 and a message pointing at `/router/bind` as the explicit binding endpoint

### Requirement: POST /router/bind and POST /router/unbind

The bridge SHALL expose `POST /router/bind` to associate one or more peers with an opencode session, and `POST /router/unbind` to dissociate. Both endpoints operate on the `bridge_sessions` table directly and start/stop the per-`sessionId` dispatch goroutine as appropriate.

`POST /router/bind` request body:

```json
{
  "sessionId": "sess-uuid",
  "peers": [
    {
      "channel":  "slack",
      "identity": "default",
      "peerId":   "D012345",
      "mention":  "<@U01ABC>"
    }
  ]
}
```

`POST /router/unbind` request body — either form is valid:

```json
{ "sessionId": "sess-uuid" }                    // unbind ALL peers
{ "sessionId": "sess-uuid", "peers": [...] }    // unbind specific peers
```

#### Scenario: Bind a session to a single Slack DM

- **WHEN** `POST /router/bind` is called with `{sessionId: "S", peers: [{channel: "slack", identity: "default", peerId: "D012345"}]}`
- **THEN** a `bridge_sessions` row exists with the specified values; the per-`S` dispatch goroutine starts; subsequent inbound from `D012345` resolves to session `S`

#### Scenario: Bind a session to multiple reviewers

- **WHEN** `POST /router/bind` is called with two peer entries across Slack and Telegram
- **THEN** two `bridge_sessions` rows are created, both with `session_id == "S"`; outbound for session `S` fans out to both

#### Scenario: Bind a Slack channel without thread (mutation on first outbound)

- **WHEN** `POST /router/bind` is called with `peers: [{channel: "slack", identity: "default", peerId: "C0DEF456"}]` AND the session subsequently emits an agent message
- **THEN** the bridge posts to channel `C0DEF456`, captures the returned `ts`, and mutates the `bridge_sessions.peer_id` to `C0DEF456|<ts>` so future outbound posts in-thread and future inbound from that thread resolves correctly

#### Scenario: Bind a Slack user ID (auto-DM resolution before persistence)

- **WHEN** `POST /router/bind` is called with `peers: [{channel: "slack", identity: "default", peerId: "U01ABC123"}]`
- **THEN** the bridge calls `conversations.open` to obtain a DM channel ID `D012345`, substitutes it, and persists the row with `peer_id = "D012345"`; the response reports the resolved value

#### Scenario: Unbind all peers for a session

- **WHEN** `POST /router/unbind` is called with `{sessionId: "S"}` only
- **THEN** all rows in `bridge_sessions` with `session_id = "S"` are deleted; the per-`S` dispatch goroutine exits

#### Scenario: Partial unbind

- **WHEN** `POST /router/unbind` is called with `{sessionId: "S", peers: [{channel: "slack", identity: "default", peerId: "D1"}]}` while session `S` is bound to three peers
- **THEN** only the matching row is deleted; the other two bindings persist; the dispatch goroutine continues running

### Requirement: Identity CRUD endpoints under /router/identities/*

The bridge SHALL expose identity CRUD endpoints at the following paths:

- `GET|POST|DELETE /router/identities/telegram[/:id]`
- `GET|POST|DELETE /router/identities/slack[/:id]`
- `GET|POST|DELETE /router/identities/mattermost[/:id]`

Every `POST` and `DELETE` MUST persist the resulting `.opencode.json` via `config.UpdateCfgFile` AND mutate the in-memory `config.cfg.Router` so the running bridge sees the change immediately without process restart.

#### Scenario: Add a new Slack identity

- **WHEN** `POST /router/identities/slack` is called with valid bot and app tokens for id `secondary`
- **THEN** `.opencode.json` is updated atomically with the new identity, `config.cfg.Router.Channels.Slack.Apps` contains `secondary`, and the Slack adapter starts the new identity without a process restart

#### Scenario: Delete an identity

- **WHEN** `DELETE /router/identities/telegram/default` is called
- **THEN** `.opencode.json` is updated atomically removing the identity, `config.cfg.Router.Channels.Telegram.Bots` no longer contains `default`, the corresponding adapter is stopped, and any `bridge_sessions` rows referencing the removed identity are deleted (cascading cleanup of orphaned bindings)

### Requirement: Per-identity groupsEnabled toggle under /router/config/groups

The bridge SHALL expose `GET|POST /router/config/groups`. The request body of `POST` MUST specify the identity scope (`{channel, identityId, enabled}`); there is no global `groupsEnabled` setting in the Go bridge. The endpoint persists the change to `.opencode.json` and updates `cfg.Router` in memory.

#### Scenario: Enable groups for one Mattermost instance

- **WHEN** `POST /router/config/groups` is called with `{channel:"mattermost", identityId:"default", enabled:true}`
- **THEN** `cfg.Router.Channels.Mattermost.Instances["default"].GroupsEnabled` is set to `true`, persisted, and the running Mattermost adapter starts accepting group-DM messages for that identity

#### Scenario: Request without identity scope rejected

- **WHEN** `POST /router/config/groups` is called without an `identityId`
- **THEN** the request fails with 400 and a message indicating per-identity scope is required

### Requirement: Extended /health reports per-identity bridge status

The `GET /health` endpoint SHALL include a `bridge` object with overall status, per-identity adapter status, and per-identity binding count. The per-identity entry MUST include at minimum `status` (one of `running`, `degraded`, `disabled`, `error`), `lastError` (string or null), `lastInboundAt` (unix-millis timestamp or null), `lastFailureAt` (unix-millis timestamp or null — distinct from `lastInboundAt` so transient delivery failures surface independently of inbound flow), and `boundSessions` (count of active bindings for this identity).

#### Scenario: All adapters healthy

- **WHEN** `GET /health` is called and all enabled identities are running
- **THEN** the response includes `bridge.status == "ok"` and `bridge.adapters[<id>].status == "running"` for every enabled identity

#### Scenario: One adapter degraded

- **WHEN** the MySQL `GET_LOCK`-holding connection for Slack `default` has dropped and is being reacquired
- **THEN** `bridge.adapters["slack:default"].status == "degraded"` and `lastError` carries a clear message

#### Scenario: Per-peer outbound failure surfaces

- **WHEN** a fan-out delivery to one of three bound peers fails (DM closed)
- **THEN** the identity's `lastFailureAt` is updated, `lastError` carries a redacted description of which peer failed and why; `status` remains `running` (single-peer failures don't degrade the whole adapter)

#### Scenario: Bridge disabled

- **WHEN** `cfg.Router == nil` or no channel is enabled
- **THEN** `bridge.status == "disabled"` with no `adapters` map

### Requirement: All mutating endpoints persist via UpdateCfgFile

Every mutation endpoint that changes `.opencode.json` MUST persist via `config.UpdateCfgFile` (the exported, atomic temp-write + rename + parent-dir fsync version shipped under the `config-atomic-writeback` capability) AND mutate the in-memory `cfg` alongside per that capability's documented contract. The bridge MUST NOT write `.opencode.json` directly. `/router/bind` and `/router/unbind` do NOT modify `.opencode.json` — they only touch the `bridge_sessions` table — and are exempt from this requirement.

#### Scenario: Crash mid-CRUD leaves prior or new file, never truncated

- **WHEN** a `POST /router/identities/slack` request is interrupted by SIGKILL during the writeback
- **THEN** `.opencode.json` on disk is either the prior contents or the new contents — never a truncated/invalid JSON

#### Scenario: Bridge sees its own write without restart

- **WHEN** `POST /router/identities/telegram` returns 200 for a new identity
- **THEN** the Telegram adapter for that identity is observably running before the next process restart, because the handler mutated `cfg.Router` in memory alongside the file write

#### Scenario: Bind does not touch .opencode.json

- **WHEN** `POST /router/bind` is called
- **THEN** `.opencode.json` is not modified; only the `bridge_sessions` table is updated

### Requirement: Auth and network posture preserved

The bridge endpoints MUST use opencode's existing API middleware for authentication and MUST inherit the localhost-only default network posture of the existing API server. No new auth scheme is introduced.

#### Scenario: Unauthenticated request rejected

- **WHEN** an unauthenticated request reaches `POST /router/identities/slack`
- **THEN** opencode's existing API middleware rejects it with the same status code as other authenticated endpoints
