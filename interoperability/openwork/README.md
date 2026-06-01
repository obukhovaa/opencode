# OpenCode + OpenWork Integration

OpenWork is an open-source control surface for agentic workflows. OpenCode serves as the backend engine. This fork is fully compatible with the upstream `opencode-ai` version — any `opencode serve` instance works as a drop-in backend.

- OpenWork repo: https://github.com/different-ai/openwork.git

## Architecture

```
                          ┌─────────────────────┐
                          │   opencode serve    │  ← your OpenCode instance
                          └──────────┬──────────┘
                                     │ HTTP/SSE API
                    ┌────────────────┼────────────────┐
                    │                │                │
             ┌──────┴──────┐   ┌─────┴──────┐  ┌──────┴───────┐
             │   router    │   │   server   │  │ orchestrator │
             │  (chat)     │   │  (web API) │  │ (supervisor) │
             └──────┬──────┘   └─────┬──────┘  └──────────────┘
                    │                │
          ┌────────┼────────┐       │
          │        │        │       │
      Telegram   Slack  Mattermost Web UI
```

**Components** (all from the OpenWork repo, all optional):

- **opencode-router** — bidirectional Telegram/Slack/Mattermost bridge. Receives messages, forwards to OpenCode, sends replies back. Supports file exchange.
- **openwork-server** — REST + SSE API layer with workspace management, approvals, file sync. Includes a built-in lightweight Toy UI at `/ui`.
- **openwork-orchestrator** — process supervisor that manages opencode + server + router as a unit. Not needed when you run OpenCode yourself.

Since we run our own `opencode serve`, the orchestrator is redundant. The scenarios below use the router and server directly.

## Prerequisites

All scenarios assume OpenCode is running:

```bash
OPENCODE_ENABLE_QUESTION_TOOL=1 opencode serve --hostname 127.0.0.1 --port 3456 --auto-approve

```

The `OPENCODE_ENABLE_QUESTION_TOOL=1` env var enables the interactive question tool — the agent can ask users questions with selectable options through chat channels. Omit it if you don't need this feature.

---

## Scenario 1: Router only (Telegram/Slack/Mattermost, no web UI)

The lightest setup. Chat with your agent entirely through Telegram, Slack, or Mattermost. No web interface.

### Telegram setup

1. Create a bot via [@BotFather](https://t.me/BotFather), copy the bot token.

2. Install and configure:

```bash
npm install -g opencode-router

# Register the bot
opencode-router telegram add <BOT_TOKEN> --id default

# Bind a Telegram chat to a workspace (use numeric chat_id, not @username)
opencode-router bindings set \
  --channel telegram \
  --identity default \
  --peer <CHAT_ID> \
  --dir /path/to/workspace
```

To find your `chat_id`: message the bot, then check `https://api.telegram.org/bot<BOT_TOKEN>/getUpdates`.

3. Start the router:

```bash
OPENCODE_URL=http://127.0.0.1:3456 \
OPENCODE_DIRECTORY=/path/to/workspace \
  opencode-router start
```

Message the bot in Telegram — it forwards to OpenCode and sends the reply back automatically.

### Slack setup

1. Create a Slack app at https://api.slack.com/apps.
2. Enable **Socket Mode**, generate an app-level token (`xapp-...`).
3. Add bot token scopes: `chat:write`, `app_mentions:read`, `im:history`, `files:read`, `files:write`.
4. Subscribe to bot events: `app_mention`, `message.im`.
5. Install the app to your workspace, copy the bot token (`xoxb-...`).

```bash
# Register the Slack app
opencode-router slack add <XOXB_TOKEN> <XAPP_TOKEN> --id default

# Bind a Slack DM or channel to a workspace
opencode-router bindings set \
  --channel slack \
  --identity default \
  --peer <CHANNEL_OR_DM_ID> \
  --dir /path/to/workspace
```

Start:

```bash
OPENCODE_URL=http://127.0.0.1:3456 \
OPENCODE_DIRECTORY=/path/to/workspace \
  opencode-router start
```

### Mattermost setup

Mattermost support uses a personal access token + native WebSocket — no external npm dependencies.

1. In your Mattermost server, go to **Account Settings > Security > Personal Access Tokens** and create a token. (Requires the system admin to enable personal access tokens in **System Console > Integrations > Integration Management**.)

2. Register the identity:

```bash
# Register the Mattermost instance
opencode-router mattermost add https://mm.example.com <ACCESS_TOKEN> --id default

# Optionally bind a channel to a workspace
opencode-router bindings set \
  --channel mattermost \
  --identity default \
  --peer <CHANNEL_ID> \
  --dir /path/to/workspace
```

3. Start:

```bash
OPENCODE_URL=http://127.0.0.1:3456 \
OPENCODE_DIRECTORY=/path/to/workspace \
  opencode-router start
```

The bot responds to DMs and group DMs automatically. For public/private channel messages, set `GROUPS_ENABLED=true` and @mention the bot by username.

Environment variable shorthand (single instance):

```bash
MATTERMOST_SERVER_URL=https://mm.example.com \
MATTERMOST_ACCESS_TOKEN=<token> \
MATTERMOST_ENABLED=true \
  opencode-router start
```

### File exchange

**Receiving files** — send a file in Telegram/Slack along with your message. The router downloads it to `<workspace>/.opencode-router/media/` and includes the local path in the prompt to OpenCode. The agent can read and process the file.

**Sending files back** — the agent's text replies are sent automatically. To send files (documents, images, audio), use the router's HTTP API or CLI:

```bash
# CLI
opencode-router send \
  --channel telegram --identity default --to <CHAT_ID> \
  --file ./output/report.docx

# HTTP (useful from OpenCode tools/hooks)
curl http://127.0.0.1:${OPENCODE_ROUTER_HEALTH_PORT:-3005}/send \
  -H 'Content-Type: application/json' \
  -d '{
    "channel": "telegram",
    "directory": "/path/to/workspace",
    "text": "Updated document attached",
    "parts": [
      {"type": "file", "filePath": "./output/report.docx"},
      {"type": "image", "filePath": "./charts/summary.png", "caption": "Summary chart"}
    ]
  }'
```

Supported part types: `file` (any document), `image`, `audio`.

### How the chat loop works

```
User sends message in Telegram
  → router downloads any attachments to .opencode-router/media/
  → router calls session.prompt() on the OpenCode SDK
  → OpenCode processes the request (reads files, runs tools, etc.)
  → router extracts text reply from the response
  → router sends reply back to Telegram
```

The round-trip is fully automatic. No UI or manual intervention needed for the text conversation. File sending back requires the agent to call the router's `/send` endpoint (via a tool or hook).

### Router environment variables

| Variable | Required | Description |
|---|---|---|
| `OPENCODE_URL` | Yes | OpenCode server URL |
| `OPENCODE_DIRECTORY` | Yes | Default workspace directory |
| `OPENCODE_SERVER_USERNAME` | If auth | OpenCode basic auth username |
| `OPENCODE_SERVER_PASSWORD` | If auth | OpenCode basic auth password |
| `TELEGRAM_BOT_TOKEN` | Alt | Single-bot shorthand (alternative to `opencode-router telegram add`) |
| `SLACK_BOT_TOKEN` | Alt | Single-app shorthand (`xoxb-...`) |
| `SLACK_APP_TOKEN` | Alt | Single-app shorthand (`xapp-...`) |
| `SLACK_ENABLED` | Alt | Set `true` with the env var shorthand |
| `MATTERMOST_SERVER_URL` | Alt | Mattermost server URL (e.g. `https://mm.example.com`) |
| `MATTERMOST_ACCESS_TOKEN` | Alt | Personal access token |
| `MATTERMOST_ENABLED` | Alt | Set `true` with the env var shorthand |
| `GROUPS_ENABLED` | No | Set `true` to allow group/channel messages |
| `OPENCODE_ROUTER_HEALTH_PORT` | No | HTTP API port (default: auto) |

**OpenCode server env var** (set on the `opencode serve` process, not the router):

| Variable | Required | Description |
|---|---|---|
| `OPENCODE_ENABLE_QUESTION_TOOL` | No | Set `1` to enable the interactive question tool (agent can ask users questions via chat) |

### Config and data paths

- Config: `~/.openwork/opencode-router/opencode-router.json`
- Database: `~/.openwork/opencode-router/opencode-router.db` (SQLite)
- Downloaded media: `<workspace>/.opencode-router/media/`

---

## Scenario 2: Server only (web UI, no chat)

Run the OpenWork server for browser-based access. Two sub-options for the UI.

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

## Scenario 3: Router + Server (chat and web UI)

Run both for maximum flexibility — chat via Telegram/Slack/Mattermost and monitor via web browser.

```bash
# Terminal 1: OpenCode
OPENCODE_ENABLE_QUESTION_TOOL=1 opencode serve --hostname 127.0.0.1 --port 3456

# Terminal 2: OpenWork server (web UI at /ui)
openwork-server \
  --workspace /path/to/workspace \
  --opencode-base-url http://127.0.0.1:3456 \
  --host 0.0.0.0 \
  --port 8787 \
  --cors '*' \
  --approval auto

# Terminal 3: Router (Telegram/Slack/Mattermost)
OPENCODE_URL=http://127.0.0.1:3456 \
OPENCODE_DIRECTORY=/path/to/workspace \
  opencode-router start
```

For the full React UI instead of Toy UI, build it as described in Scenario 2b and serve the static files.

---

## Scenario 4: Orchestrator (all-in-one, manages its own OpenCode)

The orchestrator downloads and supervises opencode + server + router as a single process tree. **You typically don't need this** since you're running your own `opencode serve`, but it's useful if you want a single command that manages everything.

```bash
npm install -g openwork-orchestrator

# Manages its own OpenCode sidecar + server + router
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

The orchestrator provides a TUI dashboard in the terminal and the Toy UI at `/ui`.

---

## Summary

| Scenario | What you run | Web UI | Chat | Best for |
|---|---|---|---|---|
| 1. Router only | `opencode serve` + `opencode-router` | None | Telegram/Slack/Mattermost | Headless agent control |
| 2a. Server (Toy UI) | `opencode serve` + `openwork-server` | Toy UI at `/ui` | None | Quick browser access |
| 2b. Server (full UI) | `opencode serve` + `openwork-server` + static files | Full React UI | None | Production web UI |
| 3. Router + Server | `opencode serve` + `openwork-server` + `opencode-router` | Toy UI or full | Telegram/Slack/Mattermost | Full remote control |
| 4. Orchestrator | `openwork start` | Toy UI at `/ui` | Optional | Single-command setup |

All scenarios work with this fork or dax's `opencode-ai`. The orchestrator (scenario 4) is the only one that can manage its own OpenCode subprocess — all others expect you to run `opencode serve` yourself.

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
