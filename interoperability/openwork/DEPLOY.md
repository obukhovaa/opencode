# Deploying OpenCode with Telegram, Slack, and Mattermost via opencode-router

Step-by-step guide for setting up secure remote agent control through Telegram, Slack, and Mattermost using the OpenWork router.

## Prerequisites

- OpenCode installed and working (`opencode serve` starts successfully)
- Node.js (for `npm install -g opencode-router`)
- A workspace directory for your agent to operate in

## 1. Install the router

```bash
npm install -g opencode-router
```

## 2. Start OpenCode

```bash
OPENCODE_ENABLE_QUESTION_TOOL=1 opencode serve --hostname 127.0.0.1 --port 3456
```

Keep this running in a dedicated terminal or as a background service.

The `OPENCODE_ENABLE_QUESTION_TOOL=1` env var enables the interactive question tool, which allows the agent to ask users questions with selectable options via chat. Without it, the question API is disabled and the agent cannot request structured input from users.

## 3. Configure Telegram (private bot)

### 3a. Create the bot

1. Message [@BotFather](https://t.me/BotFather) on Telegram.
2. Send `/newbot`, follow the prompts.
3. Copy the bot token (e.g. `123456:ABC-DEF...`).
4. Recommended: send `/setprivacy` to BotFather, select your bot, choose **Enable**. This ensures the bot only sees messages directed at it in groups.

### 3b. Register the bot as private

```bash
opencode-router telegram add <BOT_TOKEN> --id default
```

The CLI registers the bot but defaults to `public` access. To make it private (requiring `/pair` before use), edit the config file directly:

```bash
# Generate a pairing code hash (pick any secret phrase)
echo -n "MY-SECRET-CODE" | tr '[:lower:]' '[:upper:]' | tr -cd 'A-Z0-9' | shasum -a 256 | cut -d' ' -f1
```

Edit `~/.openwork/opencode-router/opencode-router.json`:

```json
{
  "version": 1,
  "opencodeUrl": "http://127.0.0.1:3456",
  "opencodeDirectory": "/path/to/workspace",
  "groupsEnabled": false,
  "toolUpdatesEnabled": true,
  "questionMode": "interactive",
  "permissionMode": "allow",
  "channels": {
    "telegram": {
      "enabled": true,
      "bots": [
        {
          "id": "default",
          "token": "<BOT_TOKEN>",
          "enabled": true,
          "directory": "/path/to/workspace",
          "access": "private",
          "pairingCodeHash": "<SHA256_HEX_FROM_ABOVE>"
        }
      ]
    }
  }
}
```

Key fields:
- `access: "private"` — rejects all messages until the user pairs.
- `pairingCodeHash` — SHA-256 hex of your secret code (uppercased, non-alphanumeric stripped). The user must send `/pair MY-SECRET-CODE` to authenticate.
- `directory` — default workspace for this bot. Prevents `/dir` from escaping the workspace root.
- `groupsEnabled: false` — disables group chat messages (only DMs accepted).

- `toolUpdatesEnabled` – send tool output to the chat
- `questionMode` – use special opencode structured question api
- `permissionMode` – allow | deny

### 3c. Pair from Telegram

1. Open Telegram and message your bot.
2. The bot replies that pairing is required.
3. Send `/pair MY-SECRET-CODE` (the plaintext, not the hash).
4. The bot confirms pairing. All future messages from your chat are accepted.

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

### 4f. Register in the router

```bash
opencode-router slack add <XOXB_TOKEN> <XAPP_TOKEN> --id default
```

Or edit `~/.openwork/opencode-router/opencode-router.json` to add a Slack section:

```json
{
  "version": 1,
  "opencodeUrl": "http://127.0.0.1:3456",
  "opencodeDirectory": "/path/to/workspace",
  "groupsEnabled": false,
  "toolUpdatesEnabled": true,
  "questionMode": "interactive",
  "permissionMode": "allow",
  "channels": {
    "slack": {
      "enabled": true,
      "apps": [
        {
          "id": "default",
          "botToken": "xoxb-...",
          "appToken": "xapp-...",
          "enabled": true,
          "directory": "/path/to/workspace"
        }
      ]
    }
  }
}
```

### 4g. Slack-specific security notes

- Slack apps are inherently scoped to your workspace — only members can interact.
- Socket Mode means no public webhook URL is exposed.
- Restrict the bot to specific channels by only subscribing to `app_mention` (the bot must be @mentioned) rather than reading all channel messages.
- For DMs, only users in your Slack workspace can reach the bot.

## 5. Configure Mattermost

Mattermost uses a token + native WebSocket. No external npm dependencies required. If you don't already have a Mattermost server, section 5a walks through running one locally; skip to 5b if you have one.

### 5a. (Optional) Run a local Mattermost server for testing

The official [`mattermost/docker`](https://github.com/mattermost/docker) repo runs Mattermost + PostgreSQL in containers. Minimal localhost setup (no nginx, no TLS):

```bash
git clone https://github.com/mattermost/docker.git mattermost-docker
cd mattermost-docker
cp env.example .env
```

Edit `.env`:

```bash
# Localhost (no DNS)
DOMAIN=localhost

# Team Edition is free; Enterprise Edition requires a license
MATTERMOST_IMAGE=mattermost-team-edition

# Plain HTTP on 8065, since no nginx
MM_SERVICESETTINGS_SITEURL=http://${DOMAIN}:8065
```

Create the volume directories:

```bash
mkdir -p ./volumes/app/mattermost/{config,data,logs,plugins,client/plugins,bleve-indexes}
mkdir -p ./volumes/db/var/lib/postgresql/data
# On Linux you also need: sudo chown -R 2000:2000 ./volumes/app/mattermost
# On macOS Docker Desktop handles UID mapping in the VM — skip the chown.
```

Start postgres + mattermost (no nginx):

```bash
docker compose -f docker-compose.yml -f docker-compose.without-nginx.yml up -d
```

> **Apple Silicon (M1/M2/M3/M4) caveat:** `mattermost-team-edition` only publishes `linux/amd64`. Add a per-service platform override so Postgres stays native arm64 but Mattermost runs under Rosetta:
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

Open `http://localhost:8065` in a browser. The first visit creates the admin account; pick any email (outbound mail isn't configured), set a strong password, then create your first team.

Day-to-day:

```bash
# Tail logs
docker compose -f docker-compose.yml -f docker-compose.without-nginx.yml [-f docker-compose.platform.yml] logs -f mattermost

# Stop
docker compose -f docker-compose.yml -f docker-compose.without-nginx.yml [-f docker-compose.platform.yml] down

# Start again (data persists in ./volumes/)
docker compose -f docker-compose.yml -f docker-compose.without-nginx.yml [-f docker-compose.platform.yml] up -d
```

### 5b. Install a desktop client

Optional but recommended over the browser — proper notifications, multi-server tabs.

**macOS:**
```bash
brew install --cask mattermost
```
Or the `.dmg` from <https://mattermost.com/download/#desktop>.

**Linux / Windows:** download from the same page.

On first launch, enter the server URL (e.g. `http://localhost:8065`) and sign in with the admin account.

### 5c. Create a Bot Account + access token (recommended)

Bot accounts are the right primitive for the router: a separate identity with its own username/avatar, no license seat consumed, and no "DM-to-yourself" issue.

1. **Enable bot account creation.** Avatar → **System Console** → **Integrations** → **Bot Accounts** → set **"Enable Bot Account Creation"** to **true** → Save.
2. **Enable personal access tokens** in the same area: **Integrations → Integration Management → Enable Personal Access Tokens** → true → Save. (Bot tokens use the same flag.)
3. **Create the bot.** Back in the main UI: avatar → **Integrations** → **Bot Accounts** → **Add Bot Account**.
   - Username: e.g. `opencode-agent`
   - Display name: e.g. `OpenCode Agent`
   - Role: `Member` (or `System Admin` if you want it to read all channels without explicit membership)
4. Click **Create Bot Account** — Mattermost shows the access token **once**. Copy it now.

> **Alternative — personal access token on your own user.** Avatar → **Profile** → **Security** → **Personal Access Tokens** → **Create Token**. Works identically but DMs to yourself are not possible in Mattermost, so the bridge can only respond in channels or to messages from a different user. Use a bot account unless you have a specific reason not to.

### 5d. Register in the router

```bash
opencode-router mattermost add https://mm.example.com <ACCESS_TOKEN> --id default
```

Or edit `~/.openwork/opencode-router/opencode-router.json` to add a Mattermost section:

```json
{
  "version": 1,
  "opencodeUrl": "http://127.0.0.1:3456",
  "opencodeDirectory": "/path/to/workspace",
  "groupsEnabled": false,
  "toolUpdatesEnabled": true,
  "questionMode": "interactive",
  "permissionMode": "allow",
  "channels": {
    "mattermost": {
      "enabled": true,
      "instances": [
        {
          "id": "default",
          "serverUrl": "https://mm.example.com",
          "accessToken": "<BOT_ACCESS_TOKEN>",
          "enabled": true,
          "directory": "/path/to/workspace"
        }
      ]
    }
  }
}
```

For the local server in 5a, use `"serverUrl": "http://localhost:8065"`.

After restart, the router logs `mattermost adapter started` for your `identityId`. To talk to the bot: open a DM with it (search for it in the new-message dialog) or add it to a channel and `@<bot-username>` it. The bridge keys routing by channel ID, so each DM peer / channel is a separate conversation.

### 5e. Mattermost-specific behavior

- **DMs and group DMs** (`D` / `G` channel types): the bot responds to all messages automatically.
- **Public/private channels** (`O` / `P` channel types): the bot only responds when `groupsEnabled: true` AND the user @mentions the bot by username.
- The bot ignores its own messages and posts from webhooks/integrations (`props.from_webhook` / `props.from_bot`) to prevent feedback loops.
- WebSocket reconnects automatically with exponential backoff (1s → 30s cap, 20 max attempts).

### 5f. Mattermost-specific security notes

- Bot accounts (preferred) are separate identities — restrict them by channel membership rather than via the token itself.
- Personal access tokens bypass MFA by design in Mattermost; prefer bot tokens for routing.
- The WebSocket connection is outbound-only (no inbound webhook URL needed).
- For self-hosted instances with self-signed TLS certificates: valid certificates are required. Self-signed certs are not supported in v1.
- The local-server setup in 5a uses plain HTTP — fine for local dev, **do not** expose port 8065 beyond `127.0.0.1` without putting nginx + TLS in front.

## 6. Bind chats to workspaces

Bindings map a specific chat to a workspace directory. Without a binding, the router uses the default `directory` from the identity config.

```bash
# Telegram — use numeric chat_id (not @username)
opencode-router bindings set \
  --channel telegram \
  --identity default \
  --peer <CHAT_ID> \
  --dir /path/to/workspace

# Slack — use channel or DM ID (e.g. D05ABCDEF)
opencode-router bindings set \
  --channel slack \
  --identity default \
  --peer <CHANNEL_ID> \
  --dir /path/to/workspace

# Mattermost — use channel ID
opencode-router bindings set \
  --channel mattermost \
  --identity default \
  --peer <CHANNEL_ID> \
  --dir /path/to/workspace
```

To find your Telegram `chat_id`: message the bot, then check:
```bash
curl -s "https://api.telegram.org/bot<BOT_TOKEN>/getUpdates" | jq '.result[-1].message.chat.id'
```

Directories are confined to the workspace root — the router rejects paths that escape it.

## 7. Start the router

```bash
OPENCODE_URL=http://127.0.0.1:3456 \
OPENCODE_DIRECTORY=/path/to/workspace \
  opencode-router start
```

Or if OpenCode uses authentication:

```bash
OPENCODE_URL=http://127.0.0.1:3456 \
OPENCODE_DIRECTORY=/path/to/workspace \
OPENCODE_SERVER_USERNAME=user \
OPENCODE_SERVER_PASSWORD=pass \
  opencode-router start
```

The router logs startup details including which bots/apps are active.

## 8. Chat commands

Once paired (Telegram) or installed (Slack), these commands are available in chat:

| Command | Description |
|---|---|
| `/reset` | Clear session and model, start fresh |
| `/dir` | Show current workspace directory |
| `/dir <path>` | Switch workspace (resets session) |
| `/model` | Show current model |
| `/opus` | Switch to Claude Opus |
| `/codex` | Switch to GPT Codex |
| `/agent` | Show workspace agent config |
| `/pair <code>` | Pair with a private Telegram bot |
| `/help` | List commands |

Any non-command message is forwarded to OpenCode as a prompt.

## Security checklist

### Telegram
- [ ] Bot set to `access: "private"` with a strong `pairingCodeHash`
- [ ] `groupsEnabled: false` (only DMs)
- [ ] `directory` set to a specific workspace (not `/` or `$HOME`)
- [ ] Bot privacy enabled via BotFather (`/setprivacy` > Enable)

### Slack
- [ ] Socket Mode enabled (no public webhook URL)
- [ ] Minimal bot scopes (only what's listed above)
- [ ] App installed only to your workspace
- [ ] Bot added only to intended channels

### Mattermost
- [ ] Dedicated bot account with restricted channel access (not a personal access token on an admin user)
- [ ] Bot Account Creation enabled in System Console → Integrations → Bot Accounts
- [ ] Valid TLS certificate on the Mattermost server (for non-local deployments)
- [ ] `groupsEnabled: false` unless channel @mention responses are desired
- [ ] `directory` set to a specific workspace
- [ ] Local Mattermost (section 5a): bound to `127.0.0.1`/loopback, not exposed publicly without nginx + TLS

### OpenCode
- [ ] `opencode serve` bound to `127.0.0.1` (not `0.0.0.0`) unless remote access is intentional
- [ ] Basic auth enabled (`OPENCODE_SERVER_USERNAME` / `OPENCODE_SERVER_PASSWORD`) if exposed beyond localhost
- [ ] `OPENCODE_ENABLE_QUESTION_TOOL=1` set to enable interactive questions via chat

### Router
- [ ] `OPENCODE_DIRECTORY` set to a specific workspace path
- [ ] `PERMISSION_MODE=deny` if you want the router to reject tool permission requests rather than auto-allow
- [ ] Router health API bound to localhost (default `127.0.0.1`, controlled by `OPENCODE_ROUTER_HEALTH_HOST`)

## Combined config example (Telegram + Slack + Mattermost)

`~/.openwork/opencode-router/opencode-router.json`:

```json
{
  "version": 1,
  "opencodeUrl": "http://127.0.0.1:3456",
  "opencodeDirectory": "/path/to/workspace",
  "groupsEnabled": false,
  "toolUpdatesEnabled": true,
  "questionMode": "interactive",
  "permissionMode": "allow",
  "channels": {
    "telegram": {
      "enabled": true,
      "bots": [
        {
          "id": "default",
          "token": "<TELEGRAM_BOT_TOKEN>",
          "enabled": true,
          "directory": "/path/to/workspace",
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
          "enabled": true,
          "directory": "/path/to/workspace"
        }
      ]
    },
    "mattermost": {
      "enabled": true,
      "instances": [
        {
          "id": "default",
          "serverUrl": "https://mm.example.com",
          "accessToken": "<PERSONAL_ACCESS_TOKEN>",
          "enabled": true,
          "directory": "/path/to/workspace"
        }
      ]
    }
  }
}
```

## File exchange

**Receiving files:** send a file in Telegram/Slack with a message. The router downloads it to `<workspace>/.opencode-router/media/` and includes the path in the prompt. The agent can read it.

**Sending files back:** text replies are automatic. To send files, the agent calls the router's HTTP API:

```bash
curl http://127.0.0.1:${OPENCODE_ROUTER_HEALTH_PORT}/send \
  -H 'Content-Type: application/json' \
  -d '{
    "channel": "telegram",
    "directory": "/path/to/workspace",
    "text": "Updated document",
    "parts": [{"type": "file", "filePath": "./output/report.docx"}]
  }'
```

## Network requirements

The router uses **outbound-only connections** for all three channels. No public ingress or webhook URLs are needed.

- **Telegram** — the router uses long polling via grammY (`getUpdates`). It makes outbound HTTPS requests to `api.telegram.org` and waits for new messages. No inbound connections.
- **Slack** — the router uses Socket Mode (`@slack/socket-mode`), which opens an outbound WebSocket to Slack's servers. No webhook URL.
- **Mattermost** — the router opens an outbound WebSocket (`wss://`) to the Mattermost server for real-time events and uses REST API calls for sending. No webhook URL.

The only listening port is the router's health/send HTTP API (`OPENCODE_ROUTER_HEALTH_PORT`), which defaults to `127.0.0.1` and is only needed for local diagnostics and the `/send` endpoint for proactive file delivery.

### Required outbound access

| Destination | Protocol | Purpose |
|---|---|---|
| `api.telegram.org` | HTTPS (443) | Telegram Bot API (polling + sending) |
| `wss-primary.slack.com` | WSS (443) | Slack Socket Mode |
| Your Mattermost server | HTTPS/WSS (443) | Mattermost REST API + WebSocket |
| OpenCode (e.g. `127.0.0.1:3456`) | HTTP | Agent API |

### No inbound access required

The router does not need to be reachable from the internet. It can run behind NAT, in a private network, or inside a Kubernetes pod with no public ingress.

## Kubernetes sidecar deployment

The router is well-suited for running as a sidecar container alongside OpenCode in a Kubernetes pod. Since both channels use outbound connections, the pod needs no Ingress or Service with external exposure.

### Pod architecture

```
┌─── Pod (no public ingress) ──────────────────────────┐
│                                                       │
│  ┌──────────────┐         ┌────────────────────────┐  │
│  │   opencode    │◄──────►│   opencode-router      │  │
│  │   serve       │ :3456  │                        │  │
│  │               │        │   outbound:            │  │
│  └──────────────┘         │   → api.telegram.org   │  │
│                           │   → wss.slack.com      │  │
│                           │   → mm.example.com     │  │
│                           └────────────────────────┘  │
│                                                       │
│  shared volume: /workspace                            │
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
      command: ["opencode", "serve", "--hostname", "127.0.0.1", "--port", "3456"]
      env:
        - name: OPENCODE_ENABLE_QUESTION_TOOL
          value: "1"
      volumeMounts:
        - name: workspace
          mountPath: /workspace
      readinessProbe:
        httpGet:
          path: /health
          port: 3456
        initialDelaySeconds: 5
        periodSeconds: 10

    - name: router
      image: node:22-slim
      command:
        - sh
        - -c
        - |
          npm install -g opencode-router && \
          opencode-router start
      env:
        - name: OPENCODE_URL
          value: "http://127.0.0.1:3456"
        - name: OPENCODE_DIRECTORY
          value: "/workspace"
        - name: TELEGRAM_BOT_TOKEN
          valueFrom:
            secretKeyRef:
              name: router-secrets
              key: telegram-bot-token
      volumeMounts:
        - name: workspace
          mountPath: /workspace
        - name: router-config
          mountPath: /root/.openwork/opencode-router
          readOnly: true

  volumes:
    - name: workspace
      emptyDir: {}
    - name: router-config
      secret:
        secretName: router-config
```

### Kubernetes secrets

Store sensitive values as Kubernetes secrets:

```bash
# Create the router config as a secret
kubectl create secret generic router-config \
  --from-file=opencode-router.json=./opencode-router.json

# Store tokens separately for env injection
kubectl create secret generic router-secrets \
  --from-literal=telegram-bot-token=<BOT_TOKEN>
```

### NetworkPolicy (optional, recommended)

Lock down the pod to only allow required outbound traffic:

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
          protocol: TCP  # Telegram API + Slack WSS + Mattermost
    - to:
        - ipBlock:
            cidr: 10.0.0.0/8  # adjust for your cluster DNS
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

# Start OpenCode in background
OPENCODE_ENABLE_QUESTION_TOOL=1 opencode serve --hostname 127.0.0.1 --port "$OPENCODE_PORT" &
OPENCODE_PID=$!

# Wait for OpenCode to be ready
until curl -sf "http://127.0.0.1:${OPENCODE_PORT}/health" > /dev/null 2>&1; do
  sleep 1
done

# Start the router
OPENCODE_URL="http://127.0.0.1:${OPENCODE_PORT}" \
OPENCODE_DIRECTORY="$WORKSPACE" \
  opencode-router start &
ROUTER_PID=$!

trap "kill $OPENCODE_PID $ROUTER_PID 2>/dev/null" EXIT

wait
```
