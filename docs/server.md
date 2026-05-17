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
| `--cors-origin` | `*` | Allowed CORS origin |
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

[OpenWork](https://github.com/different-ai/openwork) connects to the server using the `@opencode-ai/sdk/v2` client:

1. Start the server: `opencode serve --port 4096`
2. Configure OpenWork to point to `http://localhost:4096`
3. OpenWork subscribes to SSE events and uses the REST API for sessions, messages, and permissions

## ACP Mode

Start an Agent Client Protocol server for editor integration:

```bash
opencode acp
```

ACP uses JSON-RPC 2.0 over stdio with LSP-style `Content-Length` framing. This is the protocol used by [Zed](https://zed.dev), JetBrains, and other ACP-compatible editors.

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

### Compatible Clients

- [AionUI](https://github.com/iOfficeAI/AionUi) — auto-detects `opencode` on PATH and connects via `opencode acp`
- [Zed](https://zed.dev) — ACP-compatible editor
- JetBrains IDEs with ACP plugin support

### Logging

All logging goes to stderr to avoid corrupting the JSON-RPC protocol stream on stdout.
