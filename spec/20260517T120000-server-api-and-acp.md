# Server API and Agent Client Protocol (ACP) Support

**Status:** Implemented  
**Created:** 2026-05-17

## Problem

Our Go-based OpenCode fork is a TUI-only terminal application. It cannot be used as a backend for external UIs like [OpenWork](https://github.com/different-ai/openwork) or integrated into code editors via the Agent Client Protocol. The upstream dax/opencode (TypeScript) has a full HTTP REST API server and ACP support, enabling:

1. **Web/desktop UI** (OpenWork) connecting via `@opencode-ai/sdk` over HTTP
2. **Editor integration** (Zed, JetBrains) connecting via ACP over stdio
3. **Programmatic access** for automation and scripting

We need to implement compatible server functionality so our fork works with the same ecosystem of clients.

## Goals

1. Implement an HTTP REST API server compatible with the `@opencode-ai/sdk/v2` client
2. Implement ACP (Agent Client Protocol) over stdio for editor integration
3. Maintain backward compatibility — the TUI remains the default mode
4. Keep the implementation minimal — only endpoints that OpenWork actually uses

## Non-Goals

- Full parity with every dax/opencode endpoint (there are 100+)
- Workspace/multi-project management (our fork is single-project)
- OAuth provider flows, TUI remote control endpoints
- PTY/terminal multiplexing endpoints
- File browser, VCS integration endpoints (these are nice-to-have, not needed for OpenWork MVP)

## Current Architecture

### What Exists

- **App struct** (`internal/app/app.go`) — central orchestrator holding all services
- **Session service** (`internal/session/`) — CRUD for sessions with pubsub events
- **Message service** (`internal/message/`) — CRUD for messages with pubsub events
- **Agent service** (`internal/llm/agent/`) — LLM agent loop with tool execution
- **Permission service** (`internal/permission/`) — permission request/response flow
- **PubSub broker** (`internal/pubsub/`) — in-memory event bus with typed channels
- **Config** (`internal/config/`) — application configuration
- **Cobra CLI** (`cmd/root.go`) — command-line interface

### What Does NOT Exist

- No HTTP server or router
- No SSE (Server-Sent Events) streaming
- No REST API endpoints
- No ACP protocol implementation
- No `serve` or `acp` CLI subcommand

## Design

### Architecture Overview

```
┌─────────────────────────────────────────────────────────┐
│                    CLI (cobra)                           │
│  ┌──────┐  ┌──────────┐  ┌─────────────┐               │
│  │ TUI  │  │ serve    │  │ acp (stdio) │               │
│  │ mode │  │ (HTTP)   │  │ JSON-RPC    │               │
│  └──┬───┘  └────┬─────┘  └──────┬──────┘               │
│     │           │               │                       │
│     └─────┬─────┴───────┬───────┘                       │
│           │             │                               │
│     ┌─────▼─────┐ ┌─────▼──────┐                        │
│     │    App    │ │  API Layer │                        │
│     │  struct   │ │ (handlers) │                        │
│     └─────┬─────┘ └─────┬──────┘                        │
│           │             │                               │
│     ┌─────▼─────────────▼──────┐                        │
│     │     Core Services        │                        │
│     │  Sessions, Messages,     │                        │
│     │  Agent, Permissions,     │                        │
│     │  Config, PubSub          │                        │
│     └──────────────────────────┘                        │
└─────────────────────────────────────────────────────────┘
```

### Two New Modes

1. **`opencode serve`** — Headless HTTP server mode. Starts the app without TUI, listens on a port, serves REST API + SSE events.
2. **`opencode acp`** — ACP mode. Communicates over stdio using JSON-RPC 2.0, implementing the Agent Client Protocol for editor integration.

Both modes share the same core services (sessions, messages, agent, etc.) — the difference is only the transport layer.

## Implementation Plan

### Phase 1: HTTP REST API Server (Priority: Critical)

This is what OpenWork connects to directly.

#### 1.1 New Package: `internal/api`

Create a new package for HTTP API handlers using Go's standard `net/http` with a lightweight router (chi or just `http.ServeMux` from Go 1.22+).

**Directory structure:**
```
internal/api/
├── server.go              # Server setup, middleware, lifecycle
├── middleware.go           # CORS, auth, logging, error handling
├── handler_health.go       # GET /global/health
├── handler_config.go       # GET /config, PATCH /config, GET /config/providers
├── handler_session.go      # Session CRUD + prompt + abort
├── handler_message.go      # Message listing
├── handler_event.go        # SSE event streaming
├── handler_permission.go   # Permission request/reply
├── handler_agent.go        # GET /agent
├── convert_session.go      # Internal Session -> API Session
├── convert_message.go      # Internal Message -> API Message (complex)
├── convert_provider.go     # Internal models -> API Provider/Model format
├── types.go                # API request/response type definitions
└── errors.go               # API error types
```

#### 1.2 Core Endpoints (MVP for OpenWork)

These are the endpoints that OpenWork's `@opencode-ai/sdk/v2` client actually calls:

**Global:**
| Method | Path | Handler | Description |
|--------|------|---------|-------------|
| GET | `/global/health` | `healthHandler` | Health check with version |
| GET | `/global/event` | `globalEventHandler` | Global SSE event stream |
| GET | `/global/config` | `globalConfigGet` | Get global config |

**Config:**
| Method | Path | Handler | Description |
|--------|------|---------|-------------|
| GET | `/config` | `configGet` | Get project config |
| PATCH | `/config` | `configUpdate` | Update config |
| GET | `/config/providers` | `configProviders` | List providers + models |

**Sessions:**
| Method | Path | Handler | Description |
|--------|------|---------|-------------|
| GET | `/session` | `sessionList` | List all sessions |
| POST | `/session` | `sessionCreate` | Create new session |
| GET | `/session/{sessionID}` | `sessionGet` | Get session by ID |
| DELETE | `/session/{sessionID}` | `sessionDelete` | Delete session |
| PATCH | `/session/{sessionID}` | `sessionUpdate` | Update session title etc. |
| GET | `/session/status` | `sessionStatus` | Get all session statuses |
| POST | `/session/{sessionID}/abort` | `sessionAbort` | Cancel active agent run |
| POST | `/session/{sessionID}/fork` | `sessionFork` | Fork a session |

**Messages & Prompting:**
| Method | Path | Handler | Description |
|--------|------|---------|-------------|
| GET | `/session/{sessionID}/message` | `messageList` | List messages in session |
| GET | `/session/{sessionID}/message/{messageID}` | `messageGet` | Get specific message |
| POST | `/session/{sessionID}/message` | `sessionPrompt` | Send prompt (sync, streaming response) |
| POST | `/session/{sessionID}/prompt_async` | `sessionPromptAsync` | Send prompt (async, returns immediately) |
| POST | `/session/{sessionID}/summarize` | `sessionSummarize` | Summarize/compact session |

**Events:**
| Method | Path | Handler | Description |
|--------|------|---------|-------------|
| GET | `/event` | `eventSubscribe` | SSE event stream (per-project) |

**Permissions:**
| Method | Path | Handler | Description |
|--------|------|---------|-------------|
| GET | `/permission` | `permissionList` | List pending permissions |
| POST | `/permission/{requestID}/reply` | `permissionReply` | Respond to permission |

**Agents:**
| Method | Path | Handler | Description |
|--------|------|---------|-------------|
| GET | `/agent` | `agentList` | List available agents |

**Skills/Commands:**
| Method | Path | Handler | Description |
|--------|------|---------|-------------|
| GET | `/command` | `commandList` | List available slash commands |
| POST | `/session/{sessionID}/command` | `sessionCommand` | Execute a slash command |

#### 1.3 SSE Event System

The SSE event stream is critical — it's how the frontend receives real-time updates about:
- Message creation and updates (streaming text deltas)
- Tool call start/progress/completion
- Session status changes
- Permission requests
- Agent errors

**Event types** (matching dax/opencode format):

```go
type EventType string

const (
    // Message events
    EventMessageCreated     EventType = "message.created"
    EventMessageUpdated     EventType = "message.updated"
    EventMessageDeleted     EventType = "message.deleted"
    EventMessagePartUpdated EventType = "message.part.updated"
    EventMessagePartDelta   EventType = "message.part.delta"
    
    // Session events
    EventSessionCreated     EventType = "session.created"
    EventSessionUpdated     EventType = "session.updated"
    EventSessionDeleted     EventType = "session.deleted"
    EventSessionError       EventType = "session.error"
    
    // Permission events
    EventPermissionAsked    EventType = "permission.asked"
    EventPermissionResolved EventType = "permission.resolved"
    
    // Agent events
    EventAgentStatus        EventType = "agent.status"
)

type SSEEvent struct {
    Type       EventType       `json:"type"`
    Properties json.RawMessage `json:"properties"`
}
```

**Implementation approach:**
- Bridge the existing `pubsub.Broker` events to SSE
- Subscribe to `message.Broker`, `session.Broker`, `permission.Broker`
- Transform internal events into the dax/opencode event format
- Each SSE client gets its own goroutine with a channel
- Heartbeat every 30s to keep connections alive

#### 1.4 Message Format Compatibility

Our fork stores messages differently from dax/opencode. The API layer must translate between internal and external formats.

**Internal message format** (our fork):
```go
type Message struct {
    ID        string
    SessionID string
    Role      MessageRole  // "user", "assistant"
    Parts     []ContentPart // Text, ToolCall, ToolResult, etc.
    Model     models.ModelID
    // ... tokens, cost, etc.
}
```

**External API format** (dax/opencode SDK expects):
```json
{
    "info": {
        "id": "msg_xxx",
        "sessionID": "ses_xxx",
        "role": "assistant",
        "providerID": "anthropic",
        "modelID": "claude-sonnet-4-20250514",
        "tokens": { "input": 1000, "output": 500, "reasoning": 0 },
        "cost": 0.0045,
        "time": { "created": 1716000000, "updated": 1716000001 }
    },
    "parts": [
        { "id": "part_1", "type": "text", "text": "Hello..." },
        { "id": "part_2", "type": "tool", "tool": "bash", "callID": "call_1", 
          "state": { "status": "completed", "input": {...}, "output": "..." } }
    ]
}
```

The API handlers will include a **translation layer** (`internal/api/types.go`) that converts between these formats.

#### 1.5 Authentication

Simple HTTP Basic auth matching dax/opencode:
- Configured via `OPENCODE_SERVER_PASSWORD` env var
- If not set, no auth required (local development)
- Username defaults to `opencode`

#### 1.6 CORS

Allow cross-origin requests for web frontends:
- Configurable allowed origins (default: `*` for local dev)
- Standard CORS headers on preflight and actual requests

#### 1.7 CLI Integration

New cobra subcommand `serve`:

```go
var serveCmd = &cobra.Command{
    Use:   "serve",
    Short: "Start the OpenCode HTTP API server",
    RunE: func(cmd *cobra.Command, args []string) error {
        // Initialize app (same as TUI mode but headless)
        // Start HTTP server
        // Block until signal
    },
}
```

Flags:
- `--port` / `-p` (default: 4096, fallback to random if busy)
- `--hostname` (default: `127.0.0.1`)
- `--cors-origin` (default: `*`)

### Phase 2: Agent Client Protocol (Priority: High)

ACP enables editor integration (Zed, JetBrains AI, etc.)

#### 2.1 New Package: `internal/acp`

```
internal/acp/
├── server.go          # ACP server lifecycle (stdio JSON-RPC)
├── agent.go           # Implements ACP Agent interface
├── session.go         # ACP session state management
├── types.go           # ACP protocol types
└── transport.go       # JSON-RPC over stdio
```

#### 2.2 Protocol Implementation

ACP uses JSON-RPC 2.0 over stdio. We implement the agent side:

**Initialization:**
```json
// Client -> Agent
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}

// Agent -> Client
{"jsonrpc":"2.0","id":1,"result":{
    "protocolVersion": 1,
    "agentCapabilities": {
        "loadSession": true,
        "sessionCapabilities": {"close": {}, "fork": {}, "list": {}, "resume": {}},
        "promptCapabilities": {"embeddedContext": true, "image": true}
    },
    "agentInfo": {"name": "OpenCode", "version": "0.1.0"}
}}
```

**Session lifecycle:**
- `session/new` — Create session, return available models/modes
- `session/load` — Load existing session, replay messages
- `session/list` — List available sessions
- `session/close` — Close and cleanup session
- `session/resume` — Resume a previous session

**Prompting:**
- `session/prompt` — Send user prompt, wait for completion
- `session/cancel` — Cancel in-progress prompt

**Real-time updates** (agent -> client notifications):
- `session/update` with types: `agent_message_chunk`, `agent_thought_chunk`, `tool_call`, `tool_call_update`, `usage_update`, `plan`, `config_option_update`

**Permission delegation:**
- `permission/request` — Agent asks client to approve a tool call
- Client responds with allow/deny

#### 2.3 JSON-RPC Transport

Use a simple JSON-RPC 2.0 implementation over stdin/stdout:
- Read JSON-RPC requests from stdin (line-delimited)
- Write JSON-RPC responses and notifications to stdout
- Logging goes to stderr (not stdout, to avoid protocol corruption)

#### 2.4 CLI Integration

New cobra subcommand `acp`:

```go
var acpCmd = &cobra.Command{
    Use:   "acp",
    Short: "Start the ACP server (JSON-RPC over stdio)",
    RunE: func(cmd *cobra.Command, args []string) error {
        // Initialize app headless
        // Start JSON-RPC stdio loop
        // Block until EOF or signal
    },
}
```

Flags:
- `--cwd` (working directory, default: current)

### Phase 3: Provider/Model API Compatibility

The SDK expects specific response shapes for provider and model listing.

#### 3.1 Provider List Response

```json
{
    "providers": [
        {
            "id": "anthropic",
            "name": "Anthropic",
            "models": {
                "claude-sonnet-4-20250514": {
                    "id": "claude-sonnet-4-20250514",
                    "name": "Claude Sonnet 4",
                    "providerID": "anthropic",
                    "limit": { "context": 200000, "output": 64000 },
                    "attachment": true,
                    "reasoning": true,
                    "variants": { "default": {}, "high": {}, "low": {} }
                }
            }
        }
    ],
    "default": { "anthropic": "claude-sonnet-4-20250514" }
}
```

This maps to our existing `internal/llm/models` package. The API layer translates our model definitions into this format.

## Data Flow Examples

### Prompt Flow (HTTP API)

```
Client                          Server                        Agent
  │                               │                             │
  │ POST /session/{id}/message    │                             │
  │ {parts: [{type:"text",...}]}  │                             │
  │──────────────────────────────>│                             │
  │                               │ agent.Run(sessionID, text)  │
  │                               │────────────────────────────>│
  │                               │                             │
  │ SSE: message.created          │<────── pubsub event ────────│
  │<──────────────────────────────│                             │
  │                               │                             │
  │ SSE: message.part.delta       │<────── pubsub event ────────│
  │<──────────────────────────────│ (streaming text)            │
  │                               │                             │
  │ SSE: message.part.updated     │<────── pubsub event ────────│
  │<──────────────────────────────│ (tool call started)         │
  │                               │                             │
  │ SSE: permission.asked         │<────── pubsub event ────────│
  │<──────────────────────────────│                             │
  │                               │                             │
  │ POST /permission/{id}/reply   │                             │
  │ {reply: "once"}               │                             │
  │──────────────────────────────>│ permission.Respond()        │
  │                               │────────────────────────────>│
  │                               │                             │
  │ SSE: message.part.updated     │<────── pubsub event ────────│
  │<──────────────────────────────│ (tool completed)            │
  │                               │                             │
  │ SSE: message.part.delta       │<────── pubsub event ────────│
  │<──────────────────────────────│ (more streaming text)       │
  │                               │                             │
  │  200 OK (final response)      │<────── agent.Run returns ───│
  │<──────────────────────────────│                             │
```

### Prompt Flow (ACP/stdio)

```
Editor (Client)              ACP Agent                    Core Services
  │                            │                              │
  │ session/prompt             │                              │
  │ {sessionId, prompt:[...]}  │                              │
  │───────────────────────────>│ agent.Run(sessionID, text)   │
  │                            │─────────────────────────────>│
  │                            │                              │
  │ session/update             │<───── pubsub event ──────────│
  │ {agent_message_chunk}      │                              │
  │<───────────────────────────│                              │
  │                            │                              │
  │ session/update             │<───── pubsub event ──────────│
  │ {tool_call}                │                              │
  │<───────────────────────────│                              │
  │                            │                              │
  │ permission/request         │                              │
  │ {toolCall, options}        │                              │
  │<───────────────────────────│                              │
  │                            │                              │
  │ permission/response        │                              │
  │ {optionId: "once"}         │                              │
  │───────────────────────────>│ permission.Respond()         │
  │                            │─────────────────────────────>│
  │                            │                              │
  │ session/prompt result      │<───── agent.Run returns ─────│
  │ {stopReason: "end_turn"}   │                              │
  │<───────────────────────────│                              │
```

## Implementation Phases and Order

### Step 1: HTTP Server Skeleton (Est: 1-2 days)
- [ ] Add `chi` router dependency (or use Go 1.22 `http.ServeMux`)
- [ ] Create `internal/api/server.go` with server lifecycle
- [ ] Create `cmd/serve.go` with `serve` subcommand
- [ ] Implement health endpoint and CORS middleware
- [ ] Implement basic auth middleware

### Step 2: Session & Config Endpoints (Est: 2-3 days)
- [ ] Implement session CRUD endpoints
- [ ] Implement config endpoints with provider/model listing
- [ ] Implement agent listing endpoint
- [ ] Create type translation layer (`internal/api/types.go`)

### Step 3: SSE Event Streaming (Est: 2-3 days)
- [ ] Implement SSE handler with pubsub bridge
- [ ] Bridge message events (created, updated, parts, deltas)
- [ ] Bridge session events
- [ ] Bridge permission events
- [ ] Implement heartbeat mechanism

### Step 4: Prompting & Message Endpoints (Est: 2-3 days)
- [ ] Implement sync prompt endpoint (POST message)
- [ ] Implement async prompt endpoint
- [ ] Implement message listing with part format translation
- [ ] Implement session abort
- [ ] Implement session summarize/compact

### Step 5: Permission Flow (Est: 1 day)
- [ ] Implement permission list endpoint
- [ ] Implement permission reply endpoint
- [ ] Bridge permission events to SSE

### Step 6: ACP Server (Est: 3-4 days)
- [ ] Implement JSON-RPC 2.0 transport over stdio
- [ ] Implement `initialize` handler
- [ ] Implement session lifecycle (new, load, list, close, resume)
- [ ] Implement `prompt` handler with streaming updates
- [ ] Implement permission delegation
- [ ] Implement `cancel` handler
- [ ] Add `acp` CLI subcommand

### Step 7: Testing & Documentation (Est: 2 days)
- [ ] Write integration tests for key API flows
- [ ] Write ACP protocol conformance tests
- [ ] Update README with server usage docs
- [ ] Create docs/server.md with API reference
- [ ] Create docs/acp.md with editor setup guide

## Dependencies

### New Go dependencies
- `github.com/go-chi/chi/v5` — HTTP router (lightweight, idiomatic)
- `github.com/go-chi/cors` — CORS middleware

### No dependency needed for
- SSE — standard HTTP with `text/event-stream` content type
- JSON-RPC — simple enough to implement inline (< 200 lines)
- Basic auth — stdlib `net/http`

## Known Gaps and Implementation Notes

### Message Part Deltas

The existing `pubsub.Broker[Message]` only fires `UpdatedEvent` with the full `Message`. There are no incremental delta events for streaming text. To produce `message.part.delta` events needed by the frontend:

**Approach:** Add a new event type to the message broker that carries deltas. When the agent streams tokens, it should publish `DeltaEvent` containing the part ID and incremental text, in addition to the existing `UpdatedEvent` with the full message. The SSE bridge subscribes to both.

Alternatively, the SSE bridge can diff the previous and current `Parts` array on each `UpdatedEvent`, but this is fragile and wasteful. The delta approach is preferred.

### Provider ID Extraction

Internal `Message` has only `Model` (a `models.ModelID` like `"claude-sonnet-4-20250514"`). The SDK expects a separate `providerID` field. The translation layer must reverse-lookup: `models.SupportedModels[msg.Model].Provider`. This information is readily available.

### Per-Message Token Counts

The SDK expects `tokens: {input, output, reasoning}` per assistant message. Our internal `Message` type does not track per-message tokens — only the `Session` has aggregate token counts. 

**Approach:** Add `PromptTokens`, `CompletionTokens` fields to `Message`. The agent already has this info from the LLM response — it just needs to persist it to the message. Until then, the API returns zeros for per-message tokens and uses session-level aggregates for cost.

### Permission List Endpoint

The current `permission.Service` stores pending requests as `chan bool` in a `sync.Map`, keyed by request ID. There is no way to enumerate pending permissions or retrieve the original `PermissionRequest` metadata.

**Approach:** Add a secondary `pendingDetails sync.Map` that maps request ID -> `PermissionRequest` (tool name, metadata, session ID). The API handler iterates this map for `GET /permission`.

### Session Fork

No `Fork` method exists on `session.Service`. Forking means creating a new session that copies messages from the source session up to a certain point.

**Approach:** `Fork(ctx, sessionID) -> Session` creates a new session with `ParentSessionID` set, copies all messages from the source, returns the new session. Uses existing `CreateTaskSession` pattern as a base.

### Command/Skill Extraction

Slash commands are currently TUI-only constructs registered in the Bubble Tea model. To expose them via API, we need to extract command definitions into a registry that both TUI and API can query.

**Approach:** Create a simple `SlashCommandRegistry` that returns `[]CommandInfo{Name, Description}`. Both TUI and API read from it. Execution routes through the same `agent.Run()` path.

### Broker Event Drops

`pubsub.Broker.Publish` uses non-blocking send (`select { default: }`) which silently drops events if a subscriber's channel is full. For SSE reliability:
- Use a larger buffer for SSE subscriber channels (256 vs default 64)
- Log warnings on drops
- SSE clients can reconnect and catch up via message listing

### App Struct Thread Safety

`ActiveAgentIdx` and `activeAgent` are mutated by `SwitchAgent()` without synchronization. The HTTP API must not use these directly. Instead, each API request should specify the agent explicitly, or the server should maintain its own agent selection with proper locking.

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Message format incompatibility | SDK client fails to parse responses | Careful type mapping with tests against real SDK payloads |
| SSE event format mismatch | Frontend doesn't receive updates | Test with actual OpenWork frontend |
| Concurrent session access | Race conditions between TUI and API | Use existing `activeRequests sync.Map` for mutual exclusion |
| Permission flow timing | Deadlocks if permission never answered | Timeout mechanism on permission requests |
| ACP protocol version drift | Editors fail to connect | Pin to ACP v1, test with Zed |
| Broker drops under load | SSE clients miss events | Larger buffers + reconnect-and-catch-up via message list |
| App struct races | Concurrent API calls corrupt state | Per-request agent resolution, mutex on mutable state |
| No per-message tokens | SDK shows 0 for token counts | Accept as initial limitation, add later |

## Testing Strategy

1. **Unit tests** for type translation (internal -> API format)
2. **Integration tests** using Go's `httptest` for API endpoints
3. **SSE tests** verifying event stream format and ordering
4. **ACP tests** with scripted JSON-RPC conversations
5. **Manual testing** with OpenWork frontend and Zed editor

## References

- [Agent Client Protocol Specification](https://agentclientprotocol.com/)
- [ACP TypeScript SDK](https://github.com/agentclientprotocol/typescript-sdk) (v0.21.0)
- [OpenCode SDK](https://github.com/anomalyco/opencode/tree/main/packages/sdk) — `@opencode-ai/sdk/v2`
- [OpenCode Server Docs](https://opencode.ai/docs/server/)
- [OpenWork](https://github.com/different-ai/openwork) — reference client
- [dax/opencode OpenAPI spec](packages/sdk/openapi.json) — canonical API shape
