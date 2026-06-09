# Deploying OpenCode with Telegram, Slack, and Mattermost (in-process bridge)

Step-by-step guide for setting up secure remote agent control through Telegram, Slack, and Mattermost.

The chat bridge runs **in-process** inside `opencode serve` — there is no separate Node router to install or supervise. Channels, identities, and tokens live under the `router` section of your existing `.opencode.json`.

> **Migrating from the TypeScript `opencode-router`?** See [Cutover from the TS bridge](#cutover-from-the-ts-bridge) at the bottom. Stop the Node process, copy tokens into `.opencode.json`, restart `opencode serve`, and re-issue Telegram pairing codes. There is no data migration — bindings and allowlists start fresh.

## Prerequisites

- OpenCode installed and working (`opencode serve` starts successfully)
- A workspace directory for your agent to operate in
- No Node.js required — the bridge ships as Go inside the opencode binary

## 1. Open `.opencode.json` and add a `router` section

The bridge starts automatically when `opencode serve` boots if and only if `.opencode.json` contains a non-empty `router` section with at least one enabled channel.

Minimal stub (no channels yet):

```json
{
  "router": {
    "questionMode": "interactive",
    "permissionMode": "allow",
    "toolUpdatesEnabled": true,
    "channels": {}
  }
}
```

Top-level `router` fields:

| Field | Values | Description |
|---|---|---|
| `questionMode` | `"interactive"` \| `"disabled"` | When `interactive`, agent's `question` tool renders platform-native UI (Slack blocks, Telegram inline keyboards) with a numbered-text fallback. |
| `permissionMode` | `"allow"` \| `"deny"` | How the bridge resolves agent permission prompts surfaced over chat. |
| `toolUpdatesEnabled` | `bool` | Stream tool-execution updates to the chat. |
| `channels` | object | Per-platform identity arrays — see sections 2-4. |

The file is written back atomically (temp-file + fsync + rename + parent-dir fsync) by every HTTP CRUD endpoint (`POST /router/identities/...`, `POST /router/config/groups`); mode is forced to `0o600` once token-bearing fields are present.

## 2. Start OpenCode

```bash
opencode serve --hostname 127.0.0.1 --port 3456
```

That is everything — the bridge boots inside the same process. Keep it running in a dedicated terminal or as a background service.

> The legacy `OPENCODE_ENABLE_QUESTION_TOOL=1` env var is no longer required. The interactive question tool is gated on `router.questionMode` in `.opencode.json`.

Optional flags for autonomous flow runs (used by k8s Jobs and c2-agent):

| Flag | Description |
|---|---|
| `--flow <id>` | Start the named flow after the API is live. |
| `--flow-args <path>` | Path to a JSON file containing `{ "args": { ... } }` passed to the flow. |
| `--flow-exit` | Exit the process when the flow reaches a terminal state. |

## 3. Configure Telegram

### 3a. Create the bot

1. Message [@BotFather](https://t.me/BotFather) on Telegram.
2. Send `/newbot`, follow the prompts.
3. Copy the bot token (e.g. `123456:ABC-DEF...`).
4. Recommended: send `/setprivacy` → select your bot → choose **Enable**. The bot will only see direct messages and `@mentions` in groups.

### 3b. Register the bot

Two equivalent options:

**Option A — HTTP CRUD endpoint (preferred, persists atomically):**

```bash
curl -X POST http://127.0.0.1:3456/router/identities/telegram \
  -H 'Content-Type: application/json' \
  -d '{
    "id": "default",
    "token": "<BOT_TOKEN>",
    "enabled": true,
    "access": "private",
    "pairingCodeHash": "<SHA256_HEX>"
  }'
```

**Option B — edit `.opencode.json` directly, then restart `opencode serve`:**

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
            "pairingCodeHash": "<SHA256_HEX_FROM_BELOW>"
          }
        ]
      }
    }
  }
}
```

Generate the pairing hash:

```bash
echo -n "MY-SECRET-CODE" | tr '[:lower:]' '[:upper:]' | tr -cd 'A-Z0-9' | shasum -a 256 | cut -d' ' -f1
```

Key fields:
- `access: "private"` — rejects all messages until the user pairs.
- `pairingCodeHash` — SHA-256 hex of your secret code (uppercased, non-alphanumeric stripped). The user must send `/pair MY-SECRET-CODE` to authenticate.
- `groupsEnabled` (per-identity) — set `true` to accept group chat messages (requires bot @mention).

### 3c. Pair from Telegram

1. Open Telegram and message your bot.
2. The bot replies that pairing is required.
3. Send `/pair MY-SECRET-CODE` (the plaintext, not the hash).
4. The bot confirms pairing — the chat is added to `bridge_allowlist`, and all future messages from that chat are accepted.

Other users who don't know the code cannot interact with the bot.

## 4. Configure Slack

### 4a. Create the Slack app

1. Go to https://api.slack.com/apps and click **Create New App** > **From scratch**.
2. Name it (e.g. "OpenCode Agent") and pick your workspace.

### 4b. Enable Socket Mode

1. Go to **Settings > Socket Mode** in the app config.
2. Toggle **Enable Socket Mode** on.
3. Generate an app-level token with `connections:write` scope. Copy it (`xapp-...`).

### 4c. Configure bot permissions

Go to **OAuth & Permissions > Scopes > Bot Token Scopes** and add:
- `chat:write`
- `app_mentions:read`
- `im:history`
- `files:read`
- `files:write`

### 4d. Subscribe to events

Go to **Event Subscriptions > Subscribe to bot events** and add:
- `app_mention`
- `message.im`

### 4e. Install to workspace

Go to **Install App** and install. Copy the bot token (`xoxb-...`).

### 4f. Register in `.opencode.json`

```bash
curl -X POST http://127.0.0.1:3456/router/identities/slack \
  -H 'Content-Type: application/json' \
  -d '{
    "id": "default",
    "botToken": "xoxb-...",
    "appToken": "xapp-...",
    "enabled": true
  }'
```

Or in the file:

```json
{
  "router": {
    "channels": {
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
      }
    }
  }
}
```

### 4g. Slack-specific security notes

- Slack apps are inherently scoped to your workspace — only members can interact.
- Socket Mode means no public webhook URL is exposed.
- The bot only sees channel messages where it's @mentioned (via the `app_mention` subscription), plus DMs.
- For DMs, only users in your Slack workspace can reach the bot.

## 5. Configure Mattermost

Mattermost uses a token + native WebSocket. No external dependencies. If you don't already have a Mattermost server, section 5a walks through running one locally; skip to 5b if you have one.

### 5a. (Optional) Run a local Mattermost server for testing

The official [`mattermost/docker`](https://github.com/mattermost/docker) repo runs Mattermost + PostgreSQL in containers. Minimal localhost setup (no nginx, no TLS):

```bash
git clone https://github.com/mattermost/docker.git mattermost-docker
cd mattermost-docker
cp env.example .env
```

Edit `.env`:

```bash
DOMAIN=localhost
MATTERMOST_IMAGE=mattermost-team-edition
MM_SERVICESETTINGS_SITEURL=http://${DOMAIN}:8065
```

Create the volume directories:

```bash
mkdir -p ./volumes/app/mattermost/{config,data,logs,plugins,client/plugins,bleve-indexes}
mkdir -p ./volumes/db/var/lib/postgresql/data
# On Linux you also need: sudo chown -R 2000:2000 ./volumes/app/mattermost
```

Start postgres + mattermost (no nginx):

```bash
docker compose -f docker-compose.yml -f docker-compose.without-nginx.yml up -d
```

> **Apple Silicon (M1/M2/M3/M4):** `mattermost-team-edition` only publishes `linux/amd64`. Add a per-service platform override so Postgres stays native arm64 but Mattermost runs under Rosetta:
>
> Create `docker-compose.platform.yml`:
> ```yaml
> services:
>   mattermost:
>     platform: linux/amd64
> ```
>
> Then start with all three compose files layered:
> ```bash
> docker compose \
>   -f docker-compose.yml \
>   -f docker-compose.without-nginx.yml \
>   -f docker-compose.platform.yml \
>   up -d
> ```

Wait for it to come up:

```bash
until curl -sf http://localhost:8065/api/v4/system/ping >/dev/null; do sleep 2; done && echo "ready"
```

Open `http://localhost:8065`, create the admin account, then create your first team.

### 5b. Create a Bot Account + access token (recommended)

1. Avatar → **System Console** → **Integrations** → **Bot Accounts** → set **Enable Bot Account Creation** to **true**.
2. Same area: **Integrations → Integration Management → Enable Personal Access Tokens** → true.
3. Back in the main UI: avatar → **Integrations** → **Bot Accounts** → **Add Bot Account**.
   - Username: e.g. `opencode-agent`
   - Display name: e.g. `OpenCode Agent`
4. Click **Create Bot Account** — Mattermost shows the access token **once**. Copy it now.

### 5c. Register in `.opencode.json`

```bash
curl -X POST http://127.0.0.1:3456/router/identities/mattermost \
  -H 'Content-Type: application/json' \
  -d '{
    "id": "default",
    "serverUrl": "https://mm.example.com",
    "accessToken": "<BOT_ACCESS_TOKEN>",
    "enabled": true
  }'
```

Or in the file:

```json
{
  "router": {
    "channels": {
      "mattermost": {
        "enabled": true,
        "instances": [
          {
            "id": "default",
            "serverUrl": "https://mm.example.com",
            "accessToken": "<BOT_ACCESS_TOKEN>",
            "enabled": true
          }
        ]
      }
    }
  }
}
```

For the local server in 5a, use `"serverUrl": "http://localhost:8065"`.

After restart, the server logs `mattermost adapter started` for your `identityId`.

### 5d. Mattermost-specific behavior

- **DMs and group DMs** (`D` / `G` channel types): the bot responds to all messages automatically.
- **Public/private channels** (`O` / `P` channel types): the bot only responds when the per-identity `groupsEnabled` is true AND the user @mentions the bot by username.
- The bot ignores its own messages and posts from webhooks/integrations (`props.from_webhook` / `props.from_bot`) to prevent feedback loops.
- WebSocket reconnects automatically with exponential backoff (1s → 30s cap, 20 max attempts). After exhaustion the adapter is marked `error` in `/router/health`.

### 5e. Mattermost-specific security notes

- Bot accounts (preferred) are separate identities — restrict them by channel membership rather than via the token itself.
- Personal access tokens bypass MFA by design in Mattermost; prefer bot tokens.
- The WebSocket connection is outbound-only (no inbound webhook URL needed).
- For self-hosted instances with self-signed TLS: valid certificates are required.
- The local-server setup in 5a uses plain HTTP — fine for local dev, **do not** expose port 8065 publicly without nginx + TLS in front.

## 6. Bind sessions to peers

Bindings map a specific opencode session to one or more chat peers. The same session can be bound to multiple reviewers across platforms (multi-reviewer fan-out): every output goes to every bound peer, every inbound is attributed back to the agent with `[<who> via <channel>]: `.

There is no longer a per-peer `directory` field — one `opencode serve` process is pinned to one workspace (`config.WorkingDirectory()`). Use multiple processes for multiple workspaces.

### 6a. Bind via HTTP

```bash
curl -X POST http://127.0.0.1:3456/router/bind \
  -H 'Content-Type: application/json' \
  -d '{
    "sessionId": "<SESSION_ID>",
    "peers": [
      { "channel": "telegram", "identity": "default", "peerId": "<CHAT_ID>" },
      { "channel": "slack",    "identity": "default", "peerId": "U123ABC" },
      { "channel": "mattermost","identity": "default","peerId": "<CHANNEL_ID>" }
    ]
  }'
```

Peer-ID format per platform:

| Platform | Peer-ID format |
|---|---|
| Telegram | Numeric `chat_id` (e.g. `344281281`), never `@username` |
| Slack | `D<id>` (DM) / `C<id>` (channel) / `C<id>\|<thread_ts>` (thread) / `U<id>` (user, auto-resolved to DM via `conversations.open` on bind) |
| Mattermost | `<channelId>` (DM/channel) / `<channelId>\|<rootPostId>` (thread) / 26-char user-id (auto-resolved to DM on bind) |

To find your Telegram `chat_id`: message the bot, then:

```bash
curl -s "https://api.telegram.org/bot<BOT_TOKEN>/getUpdates" | jq '.result[-1].message.chat.id'
```

### 6b. Channel→thread auto-mutation

When you bind to a channel-only peer (Slack `C<id>` without thread, Mattermost `<channelId>` without root post), the bridge **mutates the binding** after the first outbound creates a thread. Subsequent agent output replies in-thread, and reviewer replies in that thread route to the correct session.

### 6c. Router-initiated conversations

`POST /router/bind` for a peer who has never messaged the bot is the **load-bearing primitive** for opening conversations: the orchestrator binds, the agent runs, the first outbound lands in the peer's inbox. Use this for c2-agent's interactive flow steps and for any other "the agent reaches out first" workflow.

For Slack user-IDs (`U<id>`) and Mattermost user-IDs, the adapter resolves to a DM channel via `conversations.open` / `channels/direct` **before** the binding is persisted. Callers don't pre-resolve.

### 6d. Unbind

```bash
# Drop a single peer (dispatcher kept if other peers remain)
curl -X POST http://127.0.0.1:3456/router/unbind \
  -H 'Content-Type: application/json' \
  -d '{"sessionId":"<SESSION_ID>","peers":[{"channel":"slack","identity":"default","peerId":"D123"}]}'

# Drop every peer for the session and tear down the dispatcher
curl -X POST http://127.0.0.1:3456/router/unbind \
  -H 'Content-Type: application/json' \
  -d '{"sessionId":"<SESSION_ID>"}'
```

In-flight `agent.Run` calls complete cleanly — unbind closes the inbound channel but lets the dispatcher's run loop continue until the agent's terminal event arrives.

## 7. Send messages and files programmatically

Outside of the agent's automatic replies, two surfaces emit outbound messages:

### 7a. From outside the process — `POST /router/send`

```bash
curl http://127.0.0.1:3456/router/send \
  -H 'Content-Type: application/json' \
  -d '{
    "channel": "telegram",
    "identity": "default",
    "peerId": "<CHAT_ID>",
    "text": "Updated document",
    "parts": [{"type": "file", "filePath": "./output/report.docx"}]
  }'
```

`autoBind: true` from the legacy router is **not supported**. Bind explicitly via `POST /router/bind` first. The `400 Bad Request` response includes a pointer to `/router/bind` if you forget.

File paths must resolve under the bridge media root (`<dataDir>/bridge/media/`) — paths that escape the root are rejected.

### 7b. From inside an agent turn — `router_send` tool

If the agent is in `mode: "agent"` (not subagent) and at least one router channel has an enabled identity, the agent's tool list automatically includes `router_send`. Its description is built dynamically from your `cfg.Router` snapshot — the agent sees enumerated channels, identities, and currently-bound peers without any external documentation.

The tool calls `bridge.Service.Send(...)` directly in-process — there is no HTTP loopback.

## 8. Chat commands

Once paired (Telegram) or installed (Slack/Mattermost), these commands are available in chat:

| Command | Description |
|---|---|
| `/reset` | Clear session, start fresh |
| `/sessions` | List recent OpenCode sessions in the current workspace (current marked) |
| `/session <id-prefix>` | Switch to an existing OpenCode session by ID prefix |
| `/agent` | List primary OpenCode agents (current marked) |
| `/agent <id-prefix>` | Switch active agent. Affects every chat on this OpenCode process; prompt cache is invalidated |
| `/model` | List models grouped by connected provider (current marked) |
| `/model <model-id>` | Switch model on the active agent. Qualify as `provider/model-id` if ambiguous |
| `/pair <code>` | Pair with a private Telegram bot |
| `/skip` | Dismiss a pending question from the agent |
| `/help` | List commands |
| `/dir` | **Unsupported** — one opencode process is pinned to one workspace. Run multiple processes for multiple workspaces. |

Any non-command message is forwarded to OpenCode as a prompt.

## 9. Health & observability

```bash
curl http://127.0.0.1:3456/router/health
```

Response shape:

```json
{
  "status": "running",
  "adapters": {
    "telegram:default":  {"status": "running",  "lastInboundAt": 1734567890123, "lastFailureAt": 0, "boundSessions": 2},
    "slack:default":     {"status": "running",  "lastInboundAt": 1734567880000, "lastFailureAt": 0, "boundSessions": 1},
    "mattermost:default":{"status": "degraded", "lastError": "websocket reconnect 3/20", "boundSessions": 0}
  }
}
```

Overall `status` is the worst per-adapter status (`error` > `degraded` > `disabled` > `running`). Returns `disabled` when no channel is enabled.

Per-identity fields:
- `status`: `running` / `degraded` / `disabled` / `error`
- `lastError`: most recent operator-visible error (tokens redacted)
- `lastInboundAt`: unix-millis of the last received inbound, `0` if none yet
- `lastFailureAt`: unix-millis of the last outbound failure, `0` if none
- `boundSessions`: count of active `bridge_sessions` rows for this identity

The bridge state is also embedded in `/global/health` for callers that want a single endpoint.

## 10. Flow API (for orchestrators)

The narrow-scope Flow API lets external orchestrators (c2-agent, k8s Jobs) start a configured flow, watch incremental progress over SSE, and react to interactive steps.

```bash
# List flows
curl http://127.0.0.1:3456/flow

# Start a flow
curl -X POST http://127.0.0.1:3456/flow \
  -H 'Content-Type: application/json' \
  -d '{"flowID":"review","args":{"reviewer":{"channel":"slack","identity":"default","peerId":"U123"}}}'

# Snapshot
curl http://127.0.0.1:3456/flow/status

# Abort
curl -X DELETE http://127.0.0.1:3456/flow
```

SSE events emitted on the existing `/event` stream:
- `flow.step.started` / `flow.step.completed` / `flow.step.failed`
- `flow.waiting_for_input` (carries `sessionId`, `stepId`, resolved `interaction.target`)
- `flow.completed` / `flow.failed`

For interactive steps (`interactive: true` with an `interaction.target` block), the flow engine **auto-binds** the resolved peers via `bridge.Service.Bind` before `agent.Run` and **auto-unbinds** after. The orchestrator's job at pod startup is to populate `--flow-args` with the reviewer's `PeerRef`; the flow engine handles the rest.

## Security checklist

### Telegram
- [ ] Bot set to `access: "private"` with a strong `pairingCodeHash`
- [ ] `groupsEnabled` left default-false unless group chats are required
- [ ] Bot privacy enabled via BotFather (`/setprivacy` > Enable)

### Slack
- [ ] Socket Mode enabled (no public webhook URL)
- [ ] Minimal bot scopes (only what's listed in 4c)
- [ ] App installed only to your workspace
- [ ] Bot added only to intended channels

### Mattermost
- [ ] Dedicated bot account (not a personal access token on an admin user)
- [ ] Valid TLS certificate on the Mattermost server (for non-local deployments)
- [ ] `groupsEnabled` left default-false unless channel @mention responses are desired
- [ ] Local Mattermost (5a): bound to `127.0.0.1`, never exposed publicly without nginx + TLS

### OpenCode
- [ ] `opencode serve` bound to `127.0.0.1` unless remote access is intentional
- [ ] `.opencode.json` mode is `0o600` (the bridge enforces this on every writeback)
- [ ] Basic auth enabled if exposed beyond localhost
- [ ] `router.permissionMode = "deny"` if you want chat-relayed permission requests to auto-reject

## Combined `.opencode.json` example (all three channels)

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
            "token": "<TELEGRAM_BOT_TOKEN>",
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
            "id": "default",
            "serverUrl": "https://mm.example.com",
            "accessToken": "<BOT_ACCESS_TOKEN>",
            "enabled": true
          }
        ]
      }
    }
  }
}
```

## File exchange

**Receiving files:** send a file in Telegram/Slack/Mattermost with a message. The bridge downloads it to `<dataDir>/bridge/media/` and threads the path into the agent's prompt. The agent can read it via the standard file tools.

**Sending files back:** the agent's text replies are automatic. For explicit file delivery, either:
- Have the agent emit a `FILE:<path>` line — the dispatcher parses it, validates the path is under the bridge media root, and attaches it to the outbound message.
- Or POST to `/router/send` with a `parts` array (see section 7a).

## Network requirements

The bridge uses **outbound-only connections** for all three channels. No public ingress or webhook URLs are needed.

- **Telegram** — long polling via `go-telegram/bot` library. Outbound HTTPS to `api.telegram.org`.
- **Slack** — Socket Mode via `slack-go/slack`. Outbound WebSocket to Slack's servers.
- **Mattermost** — outbound WebSocket (`wss://`) to the Mattermost server for events; REST API for sending.

The only listening port is the existing opencode API server (whatever you pass to `--port`). The bridge mounts its routes under `/router/*` and `/flow/*` on that same mux.

### Required outbound access

| Destination | Protocol | Purpose |
|---|---|---|
| `api.telegram.org` | HTTPS (443) | Telegram Bot API (polling + sending) |
| `wss-primary.slack.com` | WSS (443) | Slack Socket Mode |
| `slack.com` | HTTPS (443) | Slack web API (file uploads, conversations.open) |
| Your Mattermost server | HTTPS/WSS (443) | Mattermost REST + WebSocket |

### No inbound access required

The bridge does not need to be reachable from the internet. It can run behind NAT, in a private network, or inside a Kubernetes pod with no public ingress.

## Kubernetes single-container deployment

Because the bridge is in-process, the pod spec collapses to a **single container** — no router sidecar.

### Pod architecture

```
┌─── Pod (no public ingress) ──────────────────────────┐
│                                                       │
│  ┌──────────────────────────────────────────────────┐ │
│  │   opencode serve --flow review                    │ │
│  │     ├─ HTTP /router/* /flow/* /session/* …       │ │
│  │     ├─ telegram adapter (outbound long-poll)     │ │
│  │     ├─ slack adapter (outbound Socket Mode)      │ │
│  │     └─ mattermost adapter (outbound WebSocket)   │ │
│  └──────────────────────────────────────────────────┘ │
│                                                       │
│  /workspace volume                                     │
│  /etc/opencode/.opencode.json (mounted secret)         │
└───────────────────────────────────────────────────────┘
```

### Example pod spec

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: opencode-agent
spec:
  containers:
    - name: opencode
      image: your-registry/opencode:latest
      command:
        - opencode
        - serve
        - --hostname=127.0.0.1
        - --port=3456
        - --flow=review
        - --flow-args=/etc/opencode/flow-args.json
        - --flow-exit
      volumeMounts:
        - name: workspace
          mountPath: /workspace
        - name: opencode-config
          mountPath: /etc/opencode
          readOnly: true
      readinessProbe:
        httpGet:
          path: /router/health
          port: 3456
        initialDelaySeconds: 5
        periodSeconds: 10
  volumes:
    - name: workspace
      emptyDir: {}
    - name: opencode-config
      secret:
        secretName: opencode-config
        defaultMode: 0o600
```

### Kubernetes secret

Store `.opencode.json` (with tokens) and the flow-args file in one secret:

```bash
kubectl create secret generic opencode-config \
  --from-file=.opencode.json=./.opencode.json \
  --from-file=flow-args.json=./flow-args.json
```

### NetworkPolicy (optional, recommended)

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: opencode-agent-egress
spec:
  podSelector:
    matchLabels:
      app: opencode-agent
  policyTypes:
    - Egress
    - Ingress
  ingress: []  # no inbound traffic
  egress:
    - to:
        - ipBlock:
            cidr: 0.0.0.0/0
      ports:
        - port: 443
          protocol: TCP   # Telegram API + Slack WSS/HTTPS + Mattermost
    - to:
        - ipBlock:
            cidr: 10.0.0.0/8   # adjust for your cluster DNS
      ports:
        - port: 53
          protocol: UDP
        - port: 53
          protocol: TCP
```

## Minimal startup script (bare metal / VM)

```bash
#!/usr/bin/env bash
set -euo pipefail

WORKSPACE="/path/to/workspace"
OPENCODE_PORT=3456

# Make sure .opencode.json with router section exists at $WORKSPACE/.opencode.json
opencode serve --hostname 127.0.0.1 --port "$OPENCODE_PORT" &
OPENCODE_PID=$!

# Wait for the bridge to come up
until curl -sf "http://127.0.0.1:${OPENCODE_PORT}/router/health" > /dev/null 2>&1; do
  sleep 1
done

trap "kill $OPENCODE_PID 2>/dev/null" EXIT
wait
```

## Cutover from the TS bridge

If you were running the legacy `opencode-router` Node process, the migration is mechanical:

1. **Stop the Node process.** `kill` the `opencode-router start` process; remove any systemd unit or supervisor entry. Do **not** uninstall the npm package yet — keep it as a rollback option for the first day.
2. **Copy tokens into `.opencode.json`.** Open `~/.openwork/opencode-router/opencode-router.json` and merge its `channels` block into the `router.channels` section of `.opencode.json`. The schemas match field-for-field for `telegram.bots`, `slack.apps`, and `mattermost.instances`. Drop fields that are no longer supported:
   - Per-bot `directory` — bridge is pinned to one workspace.
   - Top-level `opencodeUrl`, `opencodeDirectory` — bridge runs in-process.
3. **Restart `opencode serve`.** The bridge boots automatically and connects to all three platforms.
4. **Re-issue Telegram pairing codes.** There is no allowlist migration — every paired Telegram chat must `/pair <CODE>` again on the new bridge. The `pairingCodeHash` stays the same, so you can keep the same secret code.
5. **Re-bind active sessions.** `bridge_sessions` is also empty on first run. For any active conversations, call `POST /router/bind` to recreate the binding. New conversations bind automatically on first inbound.
6. **Delete the legacy config** (`~/.openwork/opencode-router/opencode-router.json`) once you've confirmed everything works. Optionally `npm uninstall -g opencode-router` to remove the Node package.

### Behavior changes you'll notice

| Was (TS router) | Is now (in-process bridge) |
|---|---|
| `opencode-router send …` CLI | HTTP `POST /router/send` |
| Bare `/send`, `/identities/*`, `/config/groups` HTTP paths | Under `/router/*` namespace; bare paths return 404 |
| `autoBind: true` on `/send` | Removed — explicit `POST /router/bind` required |
| `/dir` chat command | Removed — one opencode process = one workspace |
| Per-peer `directory` field | Removed |
| `~/.openwork/opencode-router/opencode-router.db` (separate SQLite) | `bridge_sessions` / `bridge_allowlist` tables in opencode's existing DB |
| `~/.openwork/opencode-router/opencode-router.json` | `router` section of `.opencode.json` |
| `OPENCODE_ROUTER_HEALTH_PORT` | Bridge runs on the opencode API port |
| `OPENCODE_ENABLE_QUESTION_TOOL=1` env var | `router.questionMode = "interactive"` in `.opencode.json` |
