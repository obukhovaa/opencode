# Langfuse Direct Integration

**Date**: 2026-04-22
**Status**: Implemented
**Author**: AI-assisted

## Overview

Integrate Langfuse observability directly into OpenCode via Langfuse's REST ingestion API. This replaces the current approach of passing metadata through provider request bodies (which breaks Bedrock passthrough) with a dedicated tracing layer that captures LLM generations, token usage, cost, timing, session hierarchy, and agent context.

## Motivation

### Current State

Metadata is injected into LLM API request bodies via `provider.metadata` config:

```json
{
  "providers": {
    "bedrock": {
      "metadata": {
        "sessionId": "session_id",
        "userId": "trace_user_id",
        "tags": "tags"
      }
    }
  }
}
```

This causes problems:
- **Bedrock rejects unknown metadata fields** (`trace_user_id`, `tags`, `session_id`) — only `user_id` is accepted
- Metadata support varies across providers (Anthropic, OpenAI, Gemini each handle it differently)
- Proxy gateways like litellm may or may not forward metadata to observability backends
- Limited to what each provider's API accepts — no room for custom fields like version, agent ID, tool context

### Desired State

Direct Langfuse integration that:
- Works identically across all providers (no provider-specific metadata quirks)
- Captures rich observability data: model, tokens, cost, timing, session hierarchy, agent context
- Does not modify LLM request bodies at all (if `provider.metadata` is empty/nil)
- Runs asynchronously — never blocks or slows LLM calls
- Flushes remaining events on graceful shutdown

## Configuration

### Config Structure

```json
{
  "telemetry": {
    "userId": "artem@piano.io",
    "tags": ["team:composer"],
    "defaultTags": ["agent"],
    "langfuse": {
      "enabled": true,
      "secretKey": "env:LANGFUSE_SECRET_KEY",
      "publicKey": "env:LANGFUSE_PUBLIC_KEY",
      "baseURL": "https://cloud.langfuse.com"
    }
  }
}
```

### Config Fields

| Field | Type | Required | Description |
|---|---|---|---|
| `enabled` | bool | yes | Enable/disable Langfuse tracing |
| `secretKey` | string | yes | Langfuse secret key. Supports `env:VAR_NAME` syntax |
| `publicKey` | string | yes | Langfuse public key. Supports `env:VAR_NAME` syntax |
| `baseURL` | string | no | Langfuse host URL. Falls back to `LANGFUSE_BASE_URL` env var, then `https://cloud.langfuse.com` |

### Environment Variable Fallbacks

If config keys are not set, the client falls back to:
- `LANGFUSE_SECRET_KEY`
- `LANGFUSE_PUBLIC_KEY`
- `LANGFUSE_BASE_URL`

### Provider Metadata Behavior

When `provider.metadata` is nil or empty (no keys defined), providers send **no metadata** in the request body. This is already the current behavior — `resolveMetadata()` returns nil when `meta == nil`. Users switching to Langfuse can remove their `metadata` config entirely, eliminating Bedrock compatibility issues.

## Data Model Mapping

### Langfuse ← OpenCode

| Langfuse Concept | OpenCode Source | Notes |
|---|---|---|
| **Session** | `session.RootSessionID` | Groups all traces for a conversation including subagent sessions |
| **Trace** | One per `processGeneration()` call | Represents an agent turn (may contain multiple LLM calls if tool-use loops) |
| **Generation** | Each `StreamResponse()`/`SendMessages()` call | Individual LLM API call with model, tokens, cost, timing |
| **User ID** | `getUserID()` | From `OPENCODE_USER_ID` / `telemetry.userId` / auto UUID |
| **Tags** | `resolveTags()` | Static (config) + dynamic (context, includes agent name) |
| **Trace Name** | `"{agentID}"` | e.g. "coder", "explorer", "summarizer" |
| **Generation Name** | `"{agentID}/{model}"` | e.g. "coder/claude-sonnet-4-6" |
| **Trace Metadata** | Object | `opencode_version`, `agent_id`, `parent_session_id`, `session_id` |
| **Generation Metadata** | Object | `cycle` (loop iteration number) |
| **Release** | `version.Version` | OpenCode build version |

### Session Hierarchy

```
User Conversation (RootSessionID = "abc-123")
├── Trace: coder agent turn 1 (sessionId = "abc-123")
│   ├── Generation: claude-sonnet-4-6 call 1 (cycle 1)
│   ├── Generation: claude-sonnet-4-6 call 2 (cycle 2, after tool use)
│   └── Generation: claude-sonnet-4-6 call 3 (cycle 3, after tool use)
├── Trace: explorer subagent (sessionId = "abc-123")
│   └── Generation: claude-sonnet-4-6 call 1
├── Trace: coder agent turn 2 (sessionId = "abc-123")
│   └── Generation: claude-sonnet-4-6 call 1
└── Trace: title generation (sessionId = "abc-123")
    └── Generation: gemini-flash call 1
```

All traces share the same `sessionId` (= `RootSessionID`), so Langfuse groups them into one session view. The `metadata.parent_session_id` field on subagent traces links them to the parent.

## Technical Design

### New Package: `internal/langfuse`

#### `types.go` — API types

```go
// IngestionRequest is the batch request body for POST /api/public/ingestion
type IngestionRequest struct {
    Batch []IngestionEvent `json:"batch"`
}

type IngestionEvent struct {
    ID        string    `json:"id"`
    Type      string    `json:"type"` // "trace-create", "generation-create", "generation-update"
    Timestamp time.Time `json:"timestamp"`
    Body      any       `json:"body"`
}

type TraceBody struct {
    ID        string         `json:"id"`
    Name      string         `json:"name,omitempty"`
    SessionID string         `json:"sessionId,omitempty"`
    UserID    string         `json:"userId,omitempty"`
    Tags      []string       `json:"tags,omitempty"`
    Release   string         `json:"release,omitempty"`
    Metadata  map[string]any `json:"metadata,omitempty"`
    Input     any            `json:"input,omitempty"`
    Output    any            `json:"output,omitempty"`
}

type GenerationBody struct {
    ID                  string         `json:"id"`
    TraceID             string         `json:"traceId"`
    Name                string         `json:"name,omitempty"`
    Model               string         `json:"model,omitempty"`
    ModelParameters     map[string]any `json:"modelParameters,omitempty"`
    Input               any            `json:"input,omitempty"`
    Output              any            `json:"output,omitempty"`
    StartTime           time.Time      `json:"startTime"`
    EndTime             *time.Time     `json:"endTime,omitempty"`
    CompletionStartTime *time.Time     `json:"completionStartTime,omitempty"`
    Usage               *Usage         `json:"usage,omitempty"`
    Metadata            map[string]any `json:"metadata,omitempty"`
    Level               string         `json:"level,omitempty"` // DEFAULT, ERROR
    StatusMessage       string         `json:"statusMessage,omitempty"`
}

type Usage struct {
    Input      int64   `json:"input,omitempty"`
    Output     int64   `json:"output,omitempty"`
    Total      int64   `json:"total,omitempty"`
    Unit       string  `json:"unit"` // "TOKENS"
    InputCost  float64 `json:"inputCost,omitempty"`
    OutputCost float64 `json:"outputCost,omitempty"`
    TotalCost  float64 `json:"totalCost,omitempty"`
}
```

#### `client.go` — Client with batching

```go
type Client struct {
    publicKey  string
    secretKey  string
    baseURL    string
    httpClient *http.Client
    queue      chan IngestionEvent
    wg         sync.WaitGroup
    done       chan struct{}
}

func New(cfg config.LangfuseConfig) *Client { ... }
func (c *Client) Enqueue(event IngestionEvent) { ... }
func (c *Client) Flush() { ... }          // flush remaining events
func (c *Client) Shutdown() { ... }       // flush + stop background goroutine
func (c *Client) flushLoop() { ... }      // background: flush every 1s or when batch hits 10 events
func (c *Client) sendBatch(events []IngestionEvent) { ... } // POST /api/public/ingestion
```

**Batching behavior:**
- Events queued in a buffered channel (capacity 1000)
- Background goroutine flushes every 1 second or when 10 events accumulate
- `Shutdown()` drains the queue and sends final batch before returning
- Auth: HTTP Basic Auth (username = publicKey, password = secretKey)
- On HTTP error: log warning, never block

#### `context.go` — Context propagation

```go
type contextKey string

const (
    TraceIDKey         contextKey = "langfuse_trace_id"
    SessionIDKey       contextKey = "langfuse_session_id"
    ParentSessionIDKey contextKey = "langfuse_parent_session_id"
)

func WithTraceID(ctx context.Context, traceID string) context.Context
func GetTraceID(ctx context.Context) string
func WithSessionID(ctx context.Context, sessionID string) context.Context
func GetSessionID(ctx context.Context) string
```

### Integration Points

#### 1. Config (`internal/config/config.go`)

Add `LangfuseConfig` struct and embed in `TelemetryConfig`:

```go
type LangfuseConfig struct {
    Enabled   bool   `json:"enabled,omitempty"`
    SecretKey string `json:"secretKey,omitempty"`
    PublicKey string `json:"publicKey,omitempty"`
    BaseURL   string `json:"baseURL,omitempty"`
}

type TelemetryConfig struct {
    UserID      string          `json:"userId,omitempty"`
    Tags        []string        `json:"tags,omitempty"`
    DefaultTags []string        `json:"defaultTags,omitempty"`
    Langfuse    *LangfuseConfig `json:"langfuse,omitempty"`
}
```

#### 2. App Initialization (`cmd/` or main startup)

Initialize the global Langfuse client at startup, wire `Shutdown()` into graceful shutdown:

```go
if cfg.Telemetry != nil && cfg.Telemetry.Langfuse != nil && cfg.Telemetry.Langfuse.Enabled {
    langfuseClient = langfuse.New(*cfg.Telemetry.Langfuse)
    defer langfuseClient.Shutdown()
}
```

#### 3. Provider Layer (`internal/llm/provider/provider.go`)

Add Langfuse client to `providerClientOptions`. The `baseProvider` wrapper methods `SendMessages()` and `StreamResponse()` emit generation events:

```go
// In SendMessages():
generationID := uuid.New().String()
startTime := time.Now()
langfuseClient.GenerationCreate(ctx, generationID, model, startTime)

response, err := client.send(ctx, messages, tools)

langfuseClient.GenerationUpdate(ctx, generationID, response, err, startTime)
```

For streaming, `completionStartTime` is captured from the first content delta event.

#### 4. Agent Layer (`internal/llm/agent/agent.go`)

**`processGeneration()`** — Create trace at start:

```go
traceID := uuid.New().String()
ctx = langfuse.WithTraceID(ctx, traceID)
ctx = langfuse.WithSessionID(ctx, session.RootSessionID)

langfuseClient.TraceCreate(traceID, TraceParams{
    Name:      a.AgentID(),
    SessionID: session.RootSessionID,
    UserID:    getUserID(),
    Tags:      resolveTags(ctx),
    Release:   version.Version,
    Metadata: map[string]any{
        "agent_id":          a.AgentID(),
        "session_id":        sessionID,
        "parent_session_id": session.ParentSessionID,
    },
})
```

#### 5. Graceful Shutdown

The Langfuse client's `Shutdown()` method must be called before process exit to flush remaining events. This is wired into the app's existing shutdown path (signal handler or deferred call in main).

### Convenience Methods on Client

To keep integration call sites clean:

```go
func (c *Client) TraceCreate(params TraceBody)
func (c *Client) GenerationCreate(params GenerationBody)
func (c *Client) GenerationEnd(id string, update GenerationBody)
```

These construct `IngestionEvent` wrappers and call `Enqueue()`.

## Implementation Plan

### Phase 1: Core Package ✅
- [x] `internal/langfuse/types.go` — API types
- [x] `internal/langfuse/client.go` — Client with batching, flush, shutdown
- [x] `internal/langfuse/context.go` — Context helpers

### Phase 2: Config ✅
- [x] Add `LangfuseConfig` to `config.go`
- [x] Add validation in `Validate()`
- [x] Update JSON schema generation

### Phase 3: Provider Integration ✅
- [x] Add Langfuse client to provider options
- [x] Wrap `SendMessages()` with generation create/update
- [x] Wrap `StreamResponse()` with generation create/update + completionStartTime
- [x] Version and agent metadata on generations

### Phase 4: Agent Integration ✅
- [x] Create trace in `processGeneration()`
- [x] Set Langfuse context keys (traceID, sessionID)
- [x] Handle title/summarizer traces

### Phase 5: Lifecycle ✅
- [x] Initialize Langfuse client at startup
- [x] Wire `Shutdown()` into graceful shutdown via `defer`
- [x] Bedrock metadata stripping kept for body safety

### Phase 6: Testing ✅
- [x] Verify build passes (`go build ./...`)
- [x] Verify all tests pass (`go test ./...`)
- [x] JSON schema regenerated with langfuse config
- [x] Manual test with Langfuse cloud (pending user validation)

## Cost Calculation

Token costs are calculated using the existing model cost fields:

```go
inputCost  = float64(usage.InputTokens) * model.CostPer1MIn / 1_000_000
outputCost = float64(usage.OutputTokens) * model.CostPer1MOut / 1_000_000
```

Cache tokens use `CostPer1MInCached` rates when available.

## Risks & Mitigations

| Risk | Mitigation |
|---|---|
| Langfuse unreachable | Log warning, never block. Buffered channel drops events if full |
| High event volume | Batch at 10 events / 1s interval. Channel capacity 1000 |
| Sensitive data in prompts | No input/output logging by default. Only metadata, tokens, cost |
| Shutdown before flush | `Shutdown()` drains queue with timeout. Wired into signal handler |
| Config key leaks | Support `env:VAR_NAME` syntax, same as existing `apiKey` fields |
