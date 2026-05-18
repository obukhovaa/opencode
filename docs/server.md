# Server & ACP Mode

OpenCode can run as a headless HTTP server or as an ACP (Agent Client Protocol) server for editor integration. Both modes share the same core services — the difference is the transport layer.

## HTTP Server Mode

Start a REST API server compatible with the `@opencode-ai/sdk/v2` TypeScript SDK:

```bash
opencode serve
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `4096` | Port to listen on |
| `--hostname` | `127.0.0.1` | Hostname to bind to |
| `--cors` | `*` | Allowed CORS origin |
| `--cwd`, `-c` | current dir | Working directory |
| `--debug`, `-d` | `false` | Enable debug logging |

### Authentication

Set the `OPENCODE_SERVER_PASSWORD` environment variable to require HTTP Basic Auth on all requests:

```bash
OPENCODE_SERVER_PASSWORD=mysecret opencode serve
```

Clients authenticate with any username and the password as the password field. When the variable is unset, authentication is disabled.

### Endpoints

#### Global

| Method | Path | Description |
|--------|------|-------------|
| GET | `/global/health` | Health check (returns `{"healthy": true, "version": "..."}`) |
| GET | `/global/event` | Global SSE event stream |

#### Sessions

| Method | Path | Description |
|--------|------|-------------|
| GET | `/session` | List all sessions |
| POST | `/session` | Create a new session |
| GET | `/session/status` | Get busy/idle status of all sessions |
| GET | `/session/{sessionID}` | Get a session by ID |
| DELETE | `/session/{sessionID}` | Delete a session |
| PATCH | `/session/{sessionID}` | Update session title |
| POST | `/session/{sessionID}/abort` | Cancel the active agent run |

#### Messages & Prompting

| Method | Path | Description |
|--------|------|-------------|
| GET | `/session/{sessionID}/message` | List messages in a session |
| GET | `/session/{sessionID}/message/{messageID}` | Get a specific message |
| POST | `/session/{sessionID}/message` | Send a prompt (sync — waits for agent to complete) |
| POST | `/session/{sessionID}/prompt_async` | Send a prompt (async — returns immediately) |
| POST | `/session/{sessionID}/summarize` | Trigger session summarization |

#### Events (SSE)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/event` | SSE event stream (per-project) |

Connect to `/event` or `/global/event` to receive real-time updates. Events are sent as `data: {...}\n\n` frames with a 30-second heartbeat.

Event types:
- `message.created`, `message.updated`, `message.deleted`
- `session.created`, `session.updated`, `session.deleted`
- `permission.asked`

Each event payload has `type` and `properties` fields:

```json
{"type": "message.updated", "properties": {"info": {...}, "parts": [...]}}
```

#### Config

| Method | Path | Description |
|--------|------|-------------|
| GET | `/config` | Get current config (active model) |
| GET | `/config/providers` | List all providers and models |

#### Permissions

| Method | Path | Description |
|--------|------|-------------|
| POST | `/permission/{requestID}/reply` | Grant or deny a permission request |

Request body: `{"allow": true}` or `{"allow": false}`

#### Agents

| Method | Path | Description |
|--------|------|-------------|
| GET | `/agent` | List all registered agents |

### Connecting OpenWork

[OpenWork](https://github.com/different-ai/openwork) is a desktop UI that can connect to our opencode fork via the HTTP REST API.

#### Option A: Managed mode (OpenWork spawns opencode)

OpenWork can spawn and manage the `opencode serve` process automatically. Set the binary path and run:

```bash
OPENWORK_OPENCODE_BIN=/path/to/opencode pnpm dev
```

OpenWork will:
1. Find a free port and spawn `opencode serve --hostname 127.0.0.1 --port <port> --cors "*"`
2. Wait for the `opencode server listening on http://...` stdout sentinel
3. Set up Basic Auth using auto-generated credentials via `OPENCODE_SERVER_PASSWORD`
4. Proxy all `/opencode/*` requests from the UI to the opencode server

**Config**: OpenWork spawns opencode with `cwd` set to its managed workdir inside Application Support. To use your existing `.opencode.json`, symlink it there:

```bash
# macOS (dev build)
ln -sf ~/.opencode.json \
  ~/Library/Application\ Support/com.differentai.openwork.dev/managed-opencode-workdir/.opencode.json
```

#### Option B: External mode (you run opencode separately)

Start your own server and point OpenWork to it:

```bash
# Terminal 1: start opencode
OPENCODE_SERVER_PASSWORD=mysecret opencode serve --port 4096

# Terminal 2: start OpenWork, pointing to your server
OPENWORK_OPENCODE_BASE_URL=http://127.0.0.1:4096 \
OPENWORK_OPENCODE_PASSWORD=mysecret \
pnpm dev
```

Or for the desktop app, set these env vars before launching:

| Variable | Description |
|----------|-------------|
| `OPENWORK_OPENCODE_BASE_URL` | URL of your running opencode server |
| `OPENWORK_OPENCODE_PASSWORD` | Password for Basic Auth (matches `OPENCODE_SERVER_PASSWORD`) |
| `OPENWORK_OPENCODE_USERNAME` | Username for Basic Auth (optional, any value works) |
| `OPENWORK_OPENCODE_DIRECTORY` | Working directory for session scoping |
| `OPENWORK_OPENCODE_BIN` | Path to `opencode` binary (for managed mode) |
| `OPENWORK_MANAGED_OPENCODE_CWD` | Override cwd for managed opencode (CLI mode only) |

#### Known limitations with OpenWork

- **MCP display**: OpenWork reads MCP server config directly from the workspace `.opencode.json` file, looking for an `mcp` key (dax format). Our fork uses `mcpServers`. MCP servers work at runtime but won't appear in OpenWork's MCP panel.
- **Session directory**: OpenWork scopes sessions by directory using the `X-Opencode-Directory` header. Sessions created via the TUI use the project directory; sessions created via OpenWork use the managed workspace directory. They appear as separate session lists.

## ACP Mode

Start an Agent Client Protocol server for editor integration:

```bash
opencode acp
```

ACP uses JSON-RPC 2.0 over stdio with newline-delimited JSON (NDJSON) framing. This is the protocol used by [AionUI](https://github.com/iOfficeAI/AionUi), [Zed](https://zed.dev), JetBrains, and other ACP-compatible editors.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--cwd`, `-c` | current dir | Working directory |
| `--debug`, `-d` | `false` | Enable debug logging |

### Protocol

Messages use newline-delimited JSON (NDJSON) — one JSON-RPC message per line:

```
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}
```

Messages MUST NOT contain embedded newlines. Each message is terminated by `\n`.

### Methods

| Method | Description |
|--------|-------------|
| `initialize` | Handshake — returns agent capabilities and info |
| `session/new` | Create a new session |
| `session/load` | Load an existing session (replays messages) |
| `session/list` | List available sessions |
| `session/close` | Close a session and cancel any active run |
| `session/resume` | Resume a previous session |
| `session/prompt` | Send a user prompt (blocks until agent completes) |
| `session/cancel` | Cancel an in-progress prompt (notification, no response) |

### Notifications (Agent to Client)

The server sends `session/update` notifications with real-time updates:

- `agent_message_chunk` — text from the assistant
- `agent_thought_chunk` — reasoning/thinking content
- `tool_call` — new tool call started
- `tool_call_update` — tool call status change (pending/in_progress/completed/failed)
- `permission_request` — permission needed for a tool call

### Connecting AionUI

[AionUI](https://github.com/iOfficeAI/AionUi) is a desktop app that auto-detects CLI agents on your system and connects via ACP.

**Setup**: Just have `opencode` on your PATH. AionUI will:
1. Detect the `opencode` binary via `which opencode`
2. Spawn `opencode acp` with stdin/stdout wired for JSON-RPC
3. Send `initialize`, then `session/new` to start a conversation

No configuration needed — AionUI's built-in agent list includes OpenCode with `acpArgs: ["acp"]`.

If `opencode` is installed somewhere not on PATH, set the binary path in AionUI's settings or add it to PATH:

```bash
# Add to ~/.zshrc or ~/.bashrc
export PATH="$PATH:/path/to/opencode/bin"
```

### Other ACP Clients

Any ACP-compatible client can connect:

- [Zed](https://zed.dev) — ACP-compatible editor
- JetBrains IDEs with ACP plugin support
- Custom integrations — spawn `opencode acp` and communicate via NDJSON on stdio

### Logging

All logging goes to stderr to avoid corrupting the JSON-RPC protocol stream on stdout.
