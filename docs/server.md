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

## Conversation-owned flows (fork extension)

These endpoints are additive to the SDK-compatible session API and the existing
standalone `/flow` runner. They execute top-level work in an ordinary primary-agent
conversation. A flow step or subagent cannot start another flow.

| Method | Path | Purpose |
|---|---|---|
| GET | `/session-flow/capabilities` | Protocol version, supported operations, durable-storage and distributed-recovery support |
| POST | `/session-flow/admit` | Admit an authenticated bridge conversation and its job/peer origin |
| POST | `/session-flow/release` | Release an idle or suspended admitted conversation |
| GET | `/session/{sessionID}/flow` | Current or latest run snapshot, including steps and current output |
| POST | `/session/{sessionID}/flow` | `plan`, `create`, `run`, `update`, `list`, `status`, or `control` |
| GET | `/session/{sessionID}/flow/events?after=<cursor>&limit=100` | Durable, ordered lifecycle replay; maximum limit 500 |

All routes use the server's existing HTTP Basic authentication. The session must
be an ordinary root belonging to the server's project. In pool mode, read and
write operations require that exact conversation's local admission; a shared
MySQL database does not widen access to other conversations.

A generated plan request looks like:

```http
POST /session/<conversation>/flow
Idempotency-Key: <stable-request-id>
Content-Type: application/json

{"operation":"plan","goal":"Implement and verify the requested change"}
```

To execute an existing definition without generating or editing YAML:

```json
{"operation":"run","flowId":"review-and-fix","args":{"ticket":"EXAMPLE-1"}}
```

`plan` uses the bundled flow-creator guidance and configured complexity policy.
`create` submits generated YAML directly; `update` is restricted to unentered
steps of the owning generated run. Named runs retain an immutable parsed snapshot
and reject amendments. `control` takes `action` (`cancel`, `resume`, `skip`, or
`terminate`) and, where needed, `stepId`. Starting returns 202; inspection and
control return 200. The response includes `runId`, `conversationId`, kind and
status. A stable `Idempotency-Key` is essential for safe retries; the server echoes
it (and generates one when absent). Caller JSON cannot set the host invocation ID.

Bridge admission takes `jobId`, a complete `peer`, optional `sessionId` and
`agent`, plus optional `requester`, `deadline` (Unix milliseconds), and `jobArgs`.
If the peer is already bound, that conversation and its selected agent are
preserved. A conflicting requested session/agent is refused. Pool admission also
requires the worker's exact `workspace` and `workspaceRef`, and is mutually
exclusive with standalone flow starts, recycling and workspace rebinding.
An existing recoverable run is reserved before its peer can be rebound, so a live
foreign owner prevents takeover. Requests from the admitted bridge include
`origin: {"peer": {...}, "jobId": "..."}`; trusted metadata is restored from the
admission, not accepted from an arbitrary replacement origin body.

Release takes `sessionId` and `jobId`. It refuses active turns and flow work;
explicit suspension preserves the durable run for a later owner. Server shutdown
cancels local execution before closing stores. `/session/{sessionID}/abort` also
cancels a locally executing conversation flow.

The shared `/event` stream emits **`session.flow.*`**, with run and conversation
identity, sequence, revision and ownership fence. These events never masquerade
as the standalone runner's `flow.completed` or `flow.failed`. Replay returns
`{"events": [...], "cursor": "<runId>:<sequence>"}`. Cursors belong to one run;
when a conversation starts another run, begin replay with an empty cursor.
Capability negotiation uses `protocolVersion: 1`. Distributed recovery is
advertised only when the configured backend supports it; durable local storage
and remote-worker coordination are separate capabilities.
