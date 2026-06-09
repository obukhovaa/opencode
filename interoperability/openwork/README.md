# OpenCode + OpenWork Integration

OpenWork is an open-source control surface for agentic workflows. OpenCode serves as the backend engine. This fork is fully compatible with the upstream `opencode-ai` version — any `opencode serve` instance works as a drop-in backend.

- OpenWork repo: https://github.com/different-ai/openwork.git

## What's new — in-process chat bridge

As of this fork, the **chat bridge runs inside `opencode serve`**. There is no separate Node `opencode-router` process to install or supervise; channels, identities, and tokens live under the `router` section of `.opencode.json`, and HTTP routes are mounted under `/router/*` on the existing opencode API mux. See **[DEPLOY.md](./DEPLOY.md)** for end-to-end setup and cutover instructions.

> **Status of the legacy TS package** (`apps/opencode-router` in the openwork repo): **deprecated**. It still works against any plain `opencode serve` that hasn't enabled the in-process bridge, but no new features land there. Scenarios 1 and 3 below have been rewritten around the in-process bridge; scenario 4 (the orchestrator) remains as-is since it has nothing to do with the router.

## Architecture (in-process bridge)

```
                          ┌──────────────────────────────────────────┐
                          │   opencode serve                          │
                          │                                            │
                          │   ┌──────────┐  ┌──────────┐  ┌─────────┐ │
                          │   │ telegram │  │  slack   │  │mattermost│ │
                          │   │ adapter  │  │ adapter  │  │ adapter │ │
                          │   └────┬─────┘  └────┬─────┘  └────┬────┘ │
                          │        │             │             │       │
                          │   ┌────┴─────────────┴─────────────┴────┐ │
                          │   │  bridge orchestrator                 │ │
                          │   │  per-session dispatch goroutine      │ │
                          │   └──────────┬───────────────────────────┘ │
                          │              │                              │
                          │   ┌──────────┴───────────────────────────┐ │
                          │   │  agent.Run / question / permission    │ │
                          │   └───────────────────────────────────────┘ │
                          │                                            │
                          │   HTTP /router/* /flow/* /session/* /event │
                          └──────────────────────────────────────────┘
                                            ▲
                                            │ same HTTP/SSE API
                                            │ (also serves openwork-server, web UI)
                                  ┌─────────┴─────────┐
                                  │  external clients │
                                  │  (UI, c2-agent,   │
                                  │   orchestrators)  │
                                  └───────────────────┘
```

**Components in the openwork repo** (now optional, downstream of the bridge):

- ~~**opencode-router**~~ — **deprecated**. The bidirectional Telegram/Slack/Mattermost bridge is now in-process inside `opencode serve`. See [DEPLOY.md](./DEPLOY.md).
- **openwork-server** — REST + SSE API layer with workspace management, approvals, file sync. Includes a built-in lightweight Toy UI at `/ui`. Unaffected by the bridge port — it talks to `opencode serve`'s HTTP API the same way.
- **openwork-orchestrator** — process supervisor that manages opencode + server as a unit. Not needed when you run OpenCode yourself.

## Prerequisites

All scenarios assume OpenCode is running:

```bash
opencode serve --hostname 127.0.0.1 --port 3456
```

The legacy `OPENCODE_ENABLE_QUESTION_TOOL=1` env var is no longer needed — the interactive question tool is gated by `router.questionMode = "interactive"` in `.opencode.json`.

---

## Scenario 1: In-process bridge (Telegram/Slack/Mattermost, no web UI)

The lightest setup. Chat with your agent through Telegram, Slack, or Mattermost; everything runs inside `opencode serve`. **Full deployment walkthrough lives in [DEPLOY.md](./DEPLOY.md)** — this section is a quick overview.

### 30-second example: Telegram

1. Create a bot via [@BotFather](https://t.me/BotFather), copy the token.

2. Register the bot via the bridge's CRUD endpoint:

```bash
curl -X POST http://127.0.0.1:3456/router/identities/telegram \
  -H 'Content-Type: application/json' \
  -d '{
    "id": "default",
    "token": "<BOT_TOKEN>",
    "enabled": true
  }'
```

3. Message the bot in Telegram — the bridge forwards to OpenCode and sends the reply back automatically.

For private bots (recommended), add `access: "private"` and a `pairingCodeHash`. See [DEPLOY.md §3](./DEPLOY.md#3-configure-telegram).

### 30-second example: Slack

1. Create a Slack app, enable Socket Mode, generate bot + app tokens, install to workspace.

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

DM the bot or `@mention` it in a channel — the bridge handles routing.

### 30-second example: Mattermost

1. Create a Bot Account in Mattermost, copy the access token.

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

DMs work automatically; channel `@mentions` require the per-identity `groupsEnabled` flag.

### Binding sessions to peers

The bridge supports **multi-reviewer fan-out**: one opencode session can be bound to multiple chat peers across platforms. Output goes to every peer; inbound from any peer is attributed back to the agent with `[<who> via <channel>]: `.

```bash
curl -X POST http://127.0.0.1:3456/router/bind \
  -H 'Content-Type: application/json' \
  -d '{
    "sessionId": "<SESSION_ID>",
    "peers": [
      {"channel":"telegram","identity":"default","peerId":"<CHAT_ID>"},
      {"channel":"slack",   "identity":"default","peerId":"U123ABC"}
    ]
  }'
```

Slack `U<id>` and Mattermost user-IDs auto-resolve to a DM channel before binding. Channel-only Slack peers (`C<id>`) get the binding mutated to channel+thread (`C<id>|<ts>`) after the first outbound. See [DEPLOY.md §6](./DEPLOY.md#6-bind-sessions-to-peers) for details.

### Router-initiated conversations

`POST /router/bind` for a peer who has never messaged the bot is the load-bearing primitive — the agent's first turn lands in that peer's inbox. This unlocks the c2-agent-style "agent reaches out first" pattern and powers `interactive: true` flow steps.

### File exchange

**Receiving files** — send a file alongside your message. The bridge downloads it to `<dataDir>/bridge/media/` and threads the path into the agent's prompt.

**Sending files back** — either have the agent emit a `FILE:<path>` line in its reply (the bridge parses, validates the path is under the media root, and attaches), or POST to `/router/send` with a `parts` array:

```bash
curl http://127.0.0.1:3456/router/send \
  -H 'Content-Type: application/json' \
  -d '{
    "channel": "telegram",
    "identity": "default",
    "peerId": "<CHAT_ID>",
    "text": "Updated document",
    "parts": [{"type":"file","filePath":"./output/report.docx"}]
  }'
```

### `router_send` agent tool

When the agent is in `mode: "agent"` and at least one channel has an enabled identity, the agent's tool list automatically includes `router_send` — an in-process tool for sending messages mid-turn. Description is built dynamically from `cfg.Router` so the agent sees available channels, identities, and currently-bound peers without external docs.

### How the chat loop works

```
User sends message in chat
  → adapter normalizes to bridge.Inbound, downloads attachments to <dataDir>/bridge/media/
  → orchestrator resolves peer → bound sessionId (or creates a fresh one)
  → per-session dispatch goroutine calls agent.Run(prompt)
  → agent emits parts via messages.SubscribeParts → typing/tool updates fan to chat
  → terminal event → text parsed for FILE: tokens → adapters.Send to every bound peer
```

The whole loop is in-process — no HTTP loopback for inbound, no SSE round-trip for parts. See [DEPLOY.md](./DEPLOY.md) for chat commands, health endpoints, security checklists, and the migration guide from the legacy TS router.

### Config and data paths

| | Old (TS router) | New (in-process bridge) |
|---|---|---|
| Tokens & channels | `~/.openwork/opencode-router/opencode-router.json` | `router` section of `.opencode.json` |
| Bindings DB | `~/.openwork/opencode-router/opencode-router.db` (separate SQLite) | `bridge_sessions` / `bridge_allowlist` tables in opencode's existing DB (SQLite or MySQL) |
| Downloaded media | `<workspace>/.opencode-router/media/` | `<dataDir>/bridge/media/` |
| HTTP port | `OPENCODE_ROUTER_HEALTH_PORT` (separate) | opencode API port (`--port`) |
| HTTP paths | `/send`, `/identities/*`, `/config/groups` | `/router/send`, `/router/identities/*`, `/router/config/groups` (bare paths return 404) |

---

## Scenario 2: Server only (web UI, no chat)

Run the OpenWork server for browser-based access. Unaffected by the bridge port — `openwork-server` talks to `opencode serve`'s HTTP API regardless of whether the in-process bridge is enabled.

### 2a. Toy UI (zero build, built into the server)

```bash
npm install -g openwork-server

openwork-server \
  --workspace /path/to/workspace \
  --opencode-base-url http://127.0.0.1:3456 \
  --host 0.0.0.0 \
  --port 8787 \
  --cors '*' \
  --approval auto
```

Open `http://your-server:8787/ui` in a browser. The Toy UI is lightweight and baked into the server binary — no frontend build required.

Or via environment variables:

```bash
OPENWORK_OPENCODE_BASE_URL=http://127.0.0.1:3456 \
OPENWORK_HOST=0.0.0.0 \
OPENWORK_PORT=8787 \
OPENWORK_CORS_ORIGINS='*' \
OPENWORK_APPROVAL_MODE=auto \
  openwork-server --workspace /path/to/workspace
```

Config file alternative at `~/.config/openwork/server.json`:

```json
{
  "host": "0.0.0.0",
  "port": 8787,
  "approval": { "mode": "auto" },
  "opencodeBaseUrl": "http://127.0.0.1:3456",
  "workspaces": [{ "path": "/path/to/workspace" }],
  "corsOrigins": ["*"]
}
```

If OpenCode uses authentication:

```bash
OPENWORK_OPENCODE_BASE_URL=http://127.0.0.1:3456 \
OPENWORK_OPENCODE_USERNAME=user \
OPENWORK_OPENCODE_PASSWORD=pass \
  openwork-server --workspace /path/to/workspace
```

### 2b. Full React UI (requires building from source)

The full UI (sessions, skills, permissions, execution plans, live streaming) must be built as static files and served separately.

```bash
git clone https://github.com/different-ai/openwork.git
cd openwork
pnpm install

# Build the React app — env vars are baked in at build time
VITE_OPENWORK_URL=http://your-server:8787 \
VITE_OPENWORK_TOKEN=your-token \
  pnpm build:ui
# Output: apps/app/dist/
```

Serve `apps/app/dist/` with nginx, caddy, or any static file server. Then run the OpenWork server as in 2a.

Example nginx config:

```nginx
server {
    listen 3000;
    root /path/to/openwork/apps/app/dist;
    location / {
        try_files $uri $uri/ /index.html;
    }
}
```

| Build-time variable | Description |
|---|---|
| `VITE_OPENWORK_URL` | OpenWork server URL the UI connects to |
| `VITE_OPENWORK_TOKEN` | Client bearer token (must match server's `--token`) |
| `VITE_OPENWORK_HOST_TOKEN` | Host token for approval actions (optional) |

For development (Vite dev server instead of static build):

```bash
cd openwork

OPENWORK_REMOTE_ACCESS=1 \
OPENWORK_OPENCODE_BASE_URL=http://127.0.0.1:3456 \
  pnpm dev:headless-web
```

This starts both the Vite dev server (full React UI) and the OpenWork server in one command.

### Server environment variables

| Variable | Default | Description |
|---|---|---|
| `OPENWORK_OPENCODE_BASE_URL` | — | OpenCode server URL |
| `OPENWORK_HOST` | `127.0.0.1` | Bind host (`0.0.0.0` for remote) |
| `OPENWORK_PORT` | `8787` | Server port |
| `OPENWORK_TOKEN` | auto | Client bearer token |
| `OPENWORK_HOST_TOKEN` | auto | Host/admin token |
| `OPENWORK_APPROVAL_MODE` | `manual` | `manual` or `auto` |
| `OPENWORK_CORS_ORIGINS` | — | Comma-separated origins or `*` |
| `OPENWORK_OPENCODE_USERNAME` | — | OpenCode basic auth username |
| `OPENWORK_OPENCODE_PASSWORD` | — | OpenCode basic auth password |
| `OPENWORK_TOY_UI` | `true` | Disable Toy UI with `false` |

---

## Scenario 3: In-process bridge + Server (chat and web UI)

Run both for maximum flexibility — chat via Telegram/Slack/Mattermost AND monitor via web browser. With the bridge in-process, this is two processes total (down from three).

```bash
# Terminal 1: OpenCode (with bridge configured in .opencode.json)
opencode serve --hostname 127.0.0.1 --port 3456

# Terminal 2: OpenWork server (web UI at /ui)
openwork-server \
  --workspace /path/to/workspace \
  --opencode-base-url http://127.0.0.1:3456 \
  --host 0.0.0.0 \
  --port 8787 \
  --cors '*' \
  --approval auto
```

For the full React UI instead of Toy UI, build it as described in Scenario 2b and serve the static files.

---

## Scenario 4: Orchestrator (all-in-one, manages its own OpenCode)

The orchestrator downloads and supervises opencode + server as a single process tree. **You typically don't need this** since you're running your own `opencode serve`, but it's useful if you want a single command that manages everything.

```bash
npm install -g openwork-orchestrator

# Manages its own OpenCode sidecar + server
openwork start \
  --workspace /path/to/workspace \
  --approval auto \
  --remote-access

# Or point at your existing OpenCode
OPENWORK_OPENCODE_BASE_URL=http://127.0.0.1:3456 \
  openwork start \
    --workspace /path/to/workspace \
    --approval auto
```

The orchestrator provides a TUI dashboard in the terminal and the Toy UI at `/ui`. Chat support comes from the in-process bridge inside the managed `opencode serve` — no separate router sidecar.

---

## Summary

| Scenario | What you run | Web UI | Chat | Best for |
|---|---|---|---|---|
| 1. In-process bridge | `opencode serve` (single process) | None | Telegram/Slack/Mattermost | Headless agent control, k8s Jobs |
| 2a. Server (Toy UI) | `opencode serve` + `openwork-server` | Toy UI at `/ui` | None | Quick browser access |
| 2b. Server (full UI) | `opencode serve` + `openwork-server` + static files | Full React UI | None | Production web UI |
| 3. Bridge + Server | `opencode serve` + `openwork-server` | Toy UI or full | Telegram/Slack/Mattermost | Full remote control |
| 4. Orchestrator | `openwork start` | Toy UI at `/ui` | Optional (in-process bridge if configured) | Single-command setup |

All scenarios work with this fork or dax's `opencode-ai`. The orchestrator (scenario 4) is the only one that can manage its own OpenCode subprocess — all others expect you to run `opencode serve` yourself.

> **Legacy scenarios**: Earlier revisions of this README documented a fifth process (`opencode-router`, the Node bridge) for chat. That package is now **deprecated** — every chat scenario above uses the in-process Go bridge baked into `opencode serve`. If you have an existing `opencode-router` deployment, see [DEPLOY.md — Cutover from the TS bridge](./DEPLOY.md#cutover-from-the-ts-bridge) for the migration steps.

## Desktop app (development)

For local Electron desktop development:

```bash
git clone https://github.com/different-ai/openwork.git
cd openwork && pnpm install

OPENWORK_OPENCODE_BASE_URL=http://127.0.0.1:3456 pnpm dev
```

To use a local OpenCode binary as the managed sidecar (instead of connecting to an already-running server), apply the [runtime patch](./opencode-fork-for-openwork.patch) and set `OPENWORK_OPENCODE_BIN`:

```bash
OPENWORK_OPENCODE_BIN=$(which opencode) pnpm dev
```

## Patches

- [`opencode-fork-for-openwork.patch`](./opencode-fork-for-openwork.patch) — makes the OpenWork desktop runtime respect `OPENWORK_OPENCODE_BIN` env var for custom binary paths (upstream ignores it in favor of the bundled sidecar).
