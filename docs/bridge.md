# Chat Bridge

The chat bridge connects `opencode serve` to external chat platforms — Telegram, Slack, and Mattermost — so an agent can be driven from any of them and replies fan back through the same channels. The bridge runs **in-process** inside `opencode serve` (no separate router process), and HTTP routes are mounted under `/router/*` on the existing API port.

The bridge replaces the legacy out-of-process `opencode-router` Node service. Migration guide: [interoperability/openwork/DEPLOY.md → Cutover from the TS bridge](../interoperability/openwork/DEPLOY.md#cutover-from-the-ts-bridge).

## Quick Start

Add a `router` section to `.opencode.json`:

```json
{
  "router": {
    "questionMode": "interactive",
    "permissionMode": "allow",
    "toolUpdatesEnabled": true,
    "channels": {
      "telegram": {
        "enabled": true,
        "bots": [
          {
            "id": "default",
            "token": "<BOT_TOKEN>",
            "enabled": true,
            "access": "private",
            "pairingCodeHash": "<SHA256_HEX>"
          }
        ]
      },
      "slack": {
        "enabled": true,
        "apps": [
          {
            "id": "default",
            "botToken": "xoxb-...",
            "appToken": "xapp-...",
            "enabled": true
          }
        ]
      },
      "mattermost": {
        "enabled": true,
        "instances": [
          {
            "id": "local",
            "serverUrl": "https://mm.example.com",
            "accessToken": "<TOKEN>",
            "enabled": true
          }
        ]
      }
    }
  }
}
```

Start the server. The bridge boots automatically when `router` is present and at least one channel has an enabled identity. The startup banner shows per-adapter status:

```
opencode serve --hostname 127.0.0.1 --port 3456

  ⌬ OpenCode HTTP Server
  ──────────────────────
  Listening:  http://127.0.0.1:3456
  Version:    v0.9.5
  Auth:       none
  CORS:       *
  Router:     ok
                mattermost:local         running    0 sessions
                slack:default            running    0 sessions
                telegram:default         running    1 session
```

Health snapshot: `curl http://127.0.0.1:3456/router/health` (per-adapter `status`/`lastError`/`lastInboundAt`/`lastFailureAt`/`boundSessions`).

## Top-level `router` fields

| Field | Values | Description |
|---|---|---|
| `questionMode` | `"interactive"` \| `"disabled"` | When `interactive`, the agent's `question` tool renders Slack actions blocks / Telegram inline keyboards with a numbered-text fallback. When unset or `"disabled"`, the question tool isn't initialized. |
| `permissionMode` | `"allow"` \| `"deny"` \| `"ask"` \| empty | How the bridge resolves agent permission requests on bridge-bound sessions. `allow`/`deny` auto-resolve; `ask`/empty defer to opencode's default UI (will hang headless). Unrecognised values fail-safe to deny with a one-shot WARN log. |
| `toolUpdatesEnabled` | `bool` | Stream tool-call lifecycle (`🔧 <tool> · <params>`, `✓ <tool> · <result>`, `✗ <tool> · <error>`) to chat. Error lines surface regardless of this flag. |
| `channels.{telegram,slack,mattermost}` | object | Per-platform configuration; see below. |

## Per-channel configuration

### Telegram

```json
"telegram": {
  "enabled": true,
  "bots": [
    {
      "id": "default",
      "token": "<BOT_TOKEN>",
      "enabled": true,
      "access": "private",
      "pairingCodeHash": "<SHA256_HEX>",
      "groupsEnabled": false
    }
  ]
}
```

- `access: "private"` requires a peer to `/pair <code>` before any inbound is accepted. `pairingCodeHash` is the SHA-256 of the secret code (uppercased, non-alphanumeric stripped). Generate with:
  ```bash
  echo -n "MY-SECRET" | tr '[:lower:]' '[:upper:]' | tr -cd 'A-Z0-9' | shasum -a 256 | cut -d' ' -f1
  ```
- `access: "public"` accepts any DM.
- `groupsEnabled: true` accepts messages in group chats (requires the bot to be @mentioned).
- Peer ID format: numeric `chat_id` (never `@username`).

### Slack

```json
"slack": {
  "enabled": true,
  "apps": [
    {
      "id": "default",
      "botToken": "xoxb-...",
      "appToken": "xapp-...",
      "enabled": true,
      "groupsEnabled": true
    }
  ]
}
```

- Uses Socket Mode (no public webhook URL needed).
- Required Slack app scopes: `chat:write`, `app_mentions:read`, `im:history`, `files:read`, `files:write`. Event subscriptions: `app_mention`, `message.im`.
- Peer ID formats: `D<id>` (DM), `C<id>` (channel — auto-mutates to `C<id>|<thread_ts>` after first outbound), `C<id>|<thread_ts>` (existing thread), `U<id>` (user — auto-resolved to DM via `conversations.open` before persistence).

### Mattermost

```json
"mattermost": {
  "enabled": true,
  "instances": [
    {
      "id": "local",
      "serverUrl": "https://mm.example.com",
      "accessToken": "<BOT_ACCESS_TOKEN>",
      "enabled": true,
      "groupsEnabled": true
    }
  ]
}
```

- WebSocket + REST hand-rolled (no third-party Mattermost SDK; avoids ~272 transitive deps).
- Peer ID formats: `<channelId>` (DM/channel — auto-mutates to `<channelId>|<rootPostId>` on first outbound), `<channelId>|<rootPostId>` (existing thread), 26-char user-id (auto-resolved to DM via `channels/direct`).
- Reconnect: 1s → 30s exponential backoff, 20 attempts max. After exhaustion the adapter is marked `error` in `/router/health`.
- Interactive question UI is **NOT** supported (would require a Mattermost-callable webhook URL the bridge doesn't host). Mattermost peers always use the numbered-text question fallback regardless of `questionMode`.

## HTTP API (`/router/*`)

All endpoints live on the existing opencode API port. Bare paths (`/send`, `/identities/*`, `/config/groups`) return 404 — everything is under `/router/*`.

### `POST /router/send`

Deliver a single message to a peer:

```bash
curl -X POST http://127.0.0.1:3456/router/send \
  -H 'Content-Type: application/json' \
  -d '{
    "channel": "slack",
    "identity": "default",
    "peerId": "D012345",
    "text": "hello",
    "files": []
  }'
```

`autoBind: true` is rejected with 400 + a pointer at `/router/bind` (router-initiated conversations must bind explicitly first). File paths must resolve under `<dataDir>/bridge/media/`.

### `POST /router/bind` + `POST /router/unbind`

Associate one or more peers with an opencode session. The same session can be bound to **multiple peers** across platforms (multi-reviewer fan-out):

```bash
curl -X POST http://127.0.0.1:3456/router/bind \
  -H 'Content-Type: application/json' \
  -d '{
    "sessionId": "<SESSION_ID>",
    "peers": [
      {"channel":"telegram","identity":"default","peerId":"<CHAT_ID>"},
      {"channel":"slack",   "identity":"default","peerId":"U123ABC","mention":"@alice"}
    ]
  }'
```

- Slack `U<id>` / Mattermost user-IDs are auto-resolved to DM channels (`conversations.open` / `channels/direct`) **before** the binding is persisted.
- Channel-only Slack peers (`C<id>`) get the binding **mutated** to channel+thread (`C<id>|<ts>`) after the first outbound creates a thread; subsequent agent output replies in-thread.
- Optional `mention` per peer — platform-native ping handle prepended to the first outbound only, then cleared.
- If the session doesn't exist, `Bind` auto-creates it (so router-initiated callers can bind a fresh sessionID without pre-creating).

`POST /router/unbind` with empty `peers` drops every binding for the session and tears down the dispatcher. With non-empty `peers`, removes only those rows — dispatcher stays alive if any binding remains.

### Identity CRUD — `/router/identities/{channel}[/{id}]`

```bash
# List (tokens redacted; only `hasToken: bool` is exposed)
curl http://127.0.0.1:3456/router/identities/slack

# Upsert — mutates .opencode.json atomically + launches the adapter
curl -X POST http://127.0.0.1:3456/router/identities/slack \
  -H 'Content-Type: application/json' \
  -d '{"id":"default","botToken":"xoxb-...","appToken":"xapp-...","enabled":true}'

# Delete — deregisters the adapter + cascades all bindings for this identity
curl -X DELETE http://127.0.0.1:3456/router/identities/slack/default
```

### Per-identity `groupsEnabled` — `GET|POST /router/config/groups`

```bash
curl -X POST http://127.0.0.1:3456/router/config/groups \
  -H 'Content-Type: application/json' \
  -d '{"channel":"mattermost","identityId":"local","enabled":true}'
```

Per-identity scope — there is no global `groupsEnabled`.

### `GET /router/health`

Returns `{status, adapters[<channel:identity>]}` with per-adapter `status`/`lastError`/`lastInboundAt`/`lastFailureAt`/`boundSessions`. Overall status is worst of (`error` > `degraded` > `disabled` > `running`). Also embedded as `bridge` in the global `/health` endpoint.

## Chat commands

Once a peer is bound (manually or via the first inbound), the following commands work in any of the chat surfaces:

| Command | Description |
|---|---|
| `/sessions` | List recent sessions (★ marks current binding); shows tokens + cost + relative age. |
| `/session` | Show current session details (title, tokens, cost, message count). |
| `/session <id-prefix>` | Switch this peer's binding to another session by ID prefix. Refuses while a run is in flight on either side; multi-reviewer rows for other peers are unaffected. |
| `/agent` | List primary agents (active marked). |
| `/agent <id-prefix>` | Switch active agent (affects every chat session on this process; prompt cache invalidates). |
| `/model` | List models grouped by provider (active marked). |
| `/model <id>` | Switch model on the active agent. |
| `/reset` | Forget this peer's binding — next message starts a fresh session. |
| `/abort` | Cancel an in-flight run on the current session (releases the busy lock — use when an MCP tool hangs). |
| `/pair <code>` | Pair with a private Telegram bot. |
| `/skip` | Dismiss a pending agent question. |
| `/help` | List commands. |
| `/dir` | Unsupported — one opencode process is pinned to one workspace (returns an explanatory message). |

Any non-command message is forwarded as a prompt.

## In-process agent tool: `router_send`

When at least one channel has an enabled identity AND the agent is in `mode: "agent"` (not `subagent`), the agent's tool list automatically includes `router_send` — a tool for sending messages to bound peers mid-turn.

```json
{
  "channel":  "slack",
  "identity": "default",
  "peerId":   "D012345",
  "text":     "Done — see attached.",
  "files":    ["./output/report.pdf"]
}
```

The tool's description is **built dynamically at registration time** from the live `cfg.Router` snapshot — the agent sees enumerated channels, peer-ID formats, identities, and currently-bound peers without needing external documentation. Implementation calls `bridge.Service.Send(...)` directly in-process (no HTTP loopback).

Response shape: `{"delivered": bool, "error"?: string, "resolvedPeerId"?: string}`. Multi-peer fan-out isn't exposed — the agent makes parallel `router_send` calls if it needs to reach multiple peers (allowed via `AllowParallelism: true`).

## Interactive question UI

When `questionMode: "interactive"` is set and the agent calls the `question` tool, the bridge renders choices using platform-native UI:

- **Slack** — `chat.postMessage` with an actions block (one button per option).
- **Telegram** — `sendMessage` with `reply_markup.inline_keyboard` (one row per option).
- **Mattermost** — numbered-text fallback (interactive attachments aren't supported, see the per-channel notes above).

Button click callbacks are normalized into the same `bridge.Inbound` shape as text replies, so the agent's question-reply parsing works identically across platforms. Fallback to numbered text is per-peer — if Slack's `chat.postMessage` errors, only that peer falls back; other peers in a multi-reviewer session still get buttons.

## Subagent visibility (the `task` tool)

When the agent calls the `task` tool to spawn a subagent, the subagent runs in its own session whose `root_session_id` points at the parent. The bridge's dispatcher forwards part events from **both** the parent session and its descendants, so reviewer chat shows tool activity inside the subagent in real time:

```
🔧 task#dkhDCd · Create Jira tickets for c2 unification subagent_type=piano-manager
🔧 atlassian_jira_search#qQjm1W · {"jql":"project = GENAI ..."}
🔧 atlassian_jira_get_all_projects#Sh34dJ · {}
✓ atlassian_jira_search#qQjm1W · (...result preview...)
✓ atlassian_jira_get_all_projects#Sh34dJ · (...result preview...)
✓ task#dkhDCd · (...subagent's final output...)
```

Without this, a long-running subagent would look like 15 minutes of silence — the user might think the bridge is broken when it's actually waiting on an MCP tool.

Permission requests from subagent sessions are also handled by the bridge: `PermissionRouter` matches sessions by either direct `bridge_sessions` row OR `root_session_id` matching a bound row. With `permissionMode: "allow"`, MCP calls inside subagents auto-grant without an interactive prompt (which would hang in headless serve mode).

Subagent **costs** are rolled into the parent via `agent-tool.go`'s `parent.Cost += subagent.Cost` once the task completes (including on cancel/error paths). `/sessions` and `/session` display the aggregated number.

## Multi-reviewer fan-out

A single opencode session can be bound to multiple chat peers across platforms. The bridge:

- **Fans agent output to every bound peer** — text + attachments delivered in parallel via a bounded worker pool (cap 4).
- **Attributes inbound messages** — when multiple peers are bound, each inbound is prepended with `[<peerId> via <channel>]: ` so the agent knows who spoke. The bridge automatically strips this envelope from echoed outbound to avoid feedback loops.
- **Isolates per-peer failures** — if Slack delivery fails for one peer, Mattermost / Telegram delivery still proceeds; failures are reported per-peer in `/router/health`.

## Flow API integration

For external orchestrators (k8s Jobs, automation systems), the bridge integrates with the [Flow API](flows.md):

- Flow steps marked `interactive: true` auto-bind via `bridge.Service.Bind` before `agent.Run` and auto-unbind after `struct_output`.
- `interaction.target` accepts a single PeerRef or array of PeerRefs (resolved from `${args.NAME}`).
- SSE events on `/event`: `flow.step.{started,completed,failed}`, `flow.waiting_for_input` (carries the resolved target peers), `flow.completed`, `flow.failed`.
- `opencode serve --flow <id> --flow-args /tmp/args.json --flow-exit` for the k8s Job entrypoint pattern.

See [docs/flows.md](flows.md) for the YAML schema and orchestrator integration details.

## Single-writer enforcement

Two opencode processes against the same database can't simultaneously own the same chat identity (otherwise both would consume the same inbound stream). The bridge enforces this:

- **SQLite local-dev** — file lock on `<dataDir>/bridge.lock` via `flock` (POSIX) / `LockFileEx` (Windows).
- **MySQL** — `GET_LOCK('opencode_bridge:' + SHA1(project_id + channel + identity_id))` on a dedicated `*sql.Conn` (never returned to the pool until release).

A second process attempting to start an already-locked identity sees:

```
WARN bridge: slack adapter launch failed identity=default err="bridge: lock slack:default: identity is locked by another opencode process"
```

Its other identities continue running normally — the lock is per-identity, not per-process.

## Storage

Two new tables on both providers (SQLite + MySQL), keyed by `(project_id, channel, identity_id, peer_id)`:

- `bridge_sessions` — many-to-one peer→session mapping with `session_id` FK to `sessions(id) ON DELETE SET NULL`, plus `mention_handle` (per-peer ping handle for first-message attribution) and `mention_consumed_at` (timestamp set after first delivery; reset on re-bind).
- `bridge_allowlist` — per-identity peer allowlist (Telegram private-mode pairing).

Migrations live in `internal/db/migrations/{sqlite,mysql}/20260609120000_add_bridge_tables.sql`. MySQL column widths are sized so the compound PK fits within InnoDB's 3072-byte key-length cap under utf8mb4.

## Audit logging

Every inbound message produces one structured log line at the orchestrator's funnel point, before any branching (slash-command interception, question-reply handling, agent.Run):

```
level=INFO msg="bridge: inbound" channel=telegram identity=default peerId=344281281 \
  authorId=344281281 command=pair attachments=0 truncated=false text="/pair MY-SECRET"
```

Grep for `bridge: inbound` to reconstruct who sent what when, across all platforms.

## Cutover from the legacy `opencode-router`

If you were running the legacy Node `opencode-router` process, the migration is mechanical: stop the Node process, copy tokens from `~/.openwork/opencode-router/opencode-router.json` into the `router.channels` section of `.opencode.json`, restart `opencode serve`, re-issue Telegram pairing codes (no allowlist migration ships). Full step-by-step: [interoperability/openwork/DEPLOY.md → Cutover from the TS bridge](../interoperability/openwork/DEPLOY.md#cutover-from-the-ts-bridge).

The legacy CLI subcommands (`opencode-router telegram add`, `slack add`, etc.) are not ported — mutations go through the `/router/identities/*` HTTP CRUD endpoints or direct edits of `.opencode.json`.

## Behavior changes vs. the TS router

| Was (TS router) | Is now (in-process bridge) |
|---|---|
| `opencode-router send …` CLI | `POST /router/send` |
| Bare `/send`, `/identities/*`, `/config/groups` HTTP paths | Under `/router/*`; bare paths return 404 |
| `autoBind: true` on `/send` | Rejected; explicit `POST /router/bind` required |
| `/dir` chat command | Unsupported — one process is pinned to one workspace |
| Per-peer `directory` field | Removed |
| `~/.openwork/opencode-router/opencode-router.db` | `bridge_sessions` / `bridge_allowlist` tables in opencode's existing DB |
| `~/.openwork/opencode-router/opencode-router.json` | `router` section of `.opencode.json` |
| `OPENCODE_ROUTER_HEALTH_PORT` | Bridge runs on the opencode API port |
| `OPENCODE_ENABLE_QUESTION_TOOL=1` env var | `router.questionMode = "interactive"` in `.opencode.json` |
