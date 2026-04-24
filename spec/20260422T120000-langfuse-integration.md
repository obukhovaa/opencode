# Langfuse Direct Integration

**Date**: 2026-04-22
**Updated**: 2026-04-24
**Status**: Implemented (v2 — OTEL backend)
**Author**: AI-assisted

## Overview

Integrate Langfuse observability directly into OpenCode via the OpenTelemetry (OTEL) OTLP HTTP endpoint. This replaces the current approach of passing metadata through provider request bodies (which breaks Bedrock passthrough) with a dedicated tracing layer that captures LLM generations, **tool call executions**, token usage, cost, timing, session hierarchy, and agent context.

### v2 Changes (2026-04-24)

Migrated from the legacy Langfuse REST ingestion API (`POST /api/public/ingestion`) to the **OpenTelemetry OTLP HTTP** endpoint (`POST /api/public/otel/v1/traces`). Key improvements:

- **First-class tool call tracing** — each tool execution is a Langfuse `tool` observation with name, input, output, timing, and error state
- **Native OTEL SDK** — uses `go.opentelemetry.io/otel` TracerProvider with batch span exporter; no custom HTTP batching code
- **Proper span hierarchy** — root span = trace, child spans = generations and tools as siblings
- **Future-proof** — Langfuse recommends OTEL over the legacy API; supports `langfuse.observation.type` for all 10 observation types

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
- Captures rich observability data: model, tokens, cost, timing, session hierarchy, agent context, **tool call details**
- Does not modify LLM request bodies at all (if `provider.metadata` is empty/nil)
- Runs asynchronously — never blocks or slows LLM calls
- Flushes remaining events on graceful shutdown
- Uses standard OpenTelemetry protocol for forward compatibility

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
| **Trace** | Root OTEL span per `processGeneration()` | Represents an agent turn; parent of all generation and tool spans |
| **Generation** | Child span per `StreamResponse()`/`SendMessages()` | Individual LLM API call with model, tokens, cost, timing |
| **Tool** | Child span per `tool.Run()` call | Individual tool execution with name, input, output, timing |
| **User ID** | `getUserID()` | From `OPENCODE_USER_ID` / `telemetry.userId` / auto UUID |
| **Tags** | `resolveTags()` | Static (config) + dynamic (context, includes agent name) |
| **Trace Name** | `"{agentID}"` | e.g. "coder", "explorer", "summarizer" |
| **Generation Name** | `"{agentID}/{model}"` | e.g. "coder/claude-sonnet-4-6" |
| **Tool Name** | Tool name from LLM response | e.g. "bash", "read", "edit", "glob" |
| **Trace Metadata** | OTEL attributes | `langfuse.trace.metadata.{agent_id,session_id,parent_session_id}` |
| **Generation Metadata** | OTEL attributes | `langfuse.observation.metadata.{opencode_version,agent_id}` |
| **Tool Input/Output** | OTEL attributes | `langfuse.observation.input`, `langfuse.observation.output` (truncated to 10KB) |
| **Release** | `version.Version` | OpenCode build version via `langfuse.release` attribute |

### Session & Span Hierarchy

```
User Conversation (RootSessionID = "abc-123")
├── Trace: coder agent turn 1 (root OTEL span, sessionId = "abc-123")
│   ├── Generation: coder/claude-sonnet-4-6  [type=generation, cycle 1]
│   ├── Tool: bash                           [type=tool, input={command: "go test"}]
│   ├── Tool: read                           [type=tool, input={path: "/foo/bar.go"}]
│   ├── Generation: coder/claude-sonnet-4-6  [type=generation, cycle 2]
│   ├── Tool: edit                           [type=tool, input={file: "main.go", ...}]
│   └── Generation: coder/claude-sonnet-4-6  [type=generation, cycle 3]
├── Trace: explorer subagent (sessionId = "abc-123")
│   ├── Generation: claude-sonnet-4-6
│   ├── Tool: glob
│   ├── Tool: grep
│   └── Generation: claude-sonnet-4-6
├── Trace: coder agent turn 2 (sessionId = "abc-123")
│   └── Generation: claude-sonnet-4-6
└── Trace: title generation (sessionId = "abc-123")
    └── Generation: gemini-flash
```

All traces share the same `langfuse.session.id` (= `RootSessionID`), so Langfuse groups them into one session view. The `langfuse.trace.metadata.parent_session_id` attribute on subagent traces links them to the parent.

Generation and tool spans are **siblings** — both children of the root trace span. This is because tool execution happens after the LLM stream completes (the generation span is already ended).

## Technical Design

### Transport: OpenTelemetry OTLP HTTP

Instead of the legacy Langfuse REST API, we use the **OTLP HTTP exporter** from the Go OTEL SDK:

- **Endpoint**: `{baseURL}/api/public/otel/v1/traces`
- **Auth**: `Authorization: Basic base64(publicKey:secretKey)`
- **Header**: `x-langfuse-ingestion-version: 4` (enables Fast Preview)
- **Protocol**: OTLP HTTP/protobuf (default Go OTEL SDK format)
- **Batching**: Handled by OTEL SDK's `BatchSpanProcessor` (flushes every 1s)

### Package: `internal/langfuse`

#### `types.go` — Parameter types

```go
type TraceParams struct {
    Name      string
    SessionID string
    UserID    string
    Tags      []string
    Release   string
    Metadata  map[string]any
}

type GenerationParams struct {
    Name     string
    Model    string
    Metadata map[string]any
}

type ToolParams struct {
    Name  string
    Input any
}

type Usage struct {
    Input      int64
    Output     int64
    Total      int64
    InputCost  float64
    OutputCost float64
    TotalCost  float64
}
```

#### `client.go` — OTEL-backed client

```go
type Client struct {
    tracer   trace.Tracer
    provider *sdktrace.TracerProvider
    enabled  bool
}

func New(publicKey, secretKey, baseURL string) *Client
func (c *Client) Enabled() bool
func (c *Client) Shutdown()

func (c *Client) TraceStart(ctx context.Context, params TraceParams) context.Context
func (c *Client) TraceEnd(ctx context.Context)
func (c *Client) GenerationStart(ctx context.Context, params GenerationParams) *Span
func (c *Client) ToolStart(ctx context.Context, params ToolParams) *Span

// Global helpers
func Init(publicKey, secretKey, baseURL string) bool
func Get() *Client
func ShutdownGlobal()
func EndTrace(ctx context.Context) // safe no-op if no root span
func FormatGenerationName(agentID, model string) string
```

**Client initialization:**
1. Resolves credentials from config or env vars
2. Creates OTLP HTTP exporter with Basic Auth + ingestion version header
3. Creates OTEL `TracerProvider` with batch span processor (1s flush interval)
4. Sets OTEL resource with `service.name=opencode`, `service.version=<version>`

#### `span.go` — Span handle

```go
type Span struct {
    span trace.Span
}

func (s *Span) End()                               // end the span
func (s *Span) SetUsage(u *Usage)                  // set token usage + cost as JSON attributes
func (s *Span) SetCompletionStartTime(t time.Time) // time-to-first-token
func (s *Span) SetError(err error)                 // mark ERROR level + status message
func (s *Span) SetOutput(output any)               // set output (truncated to 10KB)
```

All methods are nil-safe — calling on nil `*Span` is a no-op. This simplifies call sites (no nil checks needed when Langfuse is disabled).

#### `context.go` — Root span context

```go
// Stores the root trace span so child spans (generations, tools)
// are created as siblings under the same trace, not nested.
func withRootSpan(ctx context.Context, span trace.Span) context.Context
func getRootSpan(ctx context.Context) trace.Span
```

The root span is the OTEL span representing the Langfuse trace. When creating generation or tool child spans, we always derive from the root span's context to ensure they are siblings.

### OTEL Attribute Mapping

#### Trace-level (set on root span)

| Attribute | Value |
|---|---|
| `langfuse.trace.name` | Agent ID (e.g. "coder") |
| `langfuse.session.id` | Root session ID |
| `langfuse.user.id` | Resolved user ID |
| `langfuse.trace.tags` | `[]string` from config + dynamic tags |
| `langfuse.release` | OpenCode version |
| `langfuse.trace.metadata.agent_id` | Agent ID |
| `langfuse.trace.metadata.session_id` | Session ID |
| `langfuse.trace.metadata.parent_session_id` | Parent session (subagents) |

#### Generation spans

| Attribute | Value |
|---|---|
| `langfuse.observation.type` | `"generation"` |
| `langfuse.observation.model.name` | Model API name |
| `gen_ai.request.model` | Model API name (standard OTEL GenAI) |
| `langfuse.observation.metadata.opencode_version` | OpenCode version |
| `langfuse.observation.metadata.agent_id` | Agent ID |
| `langfuse.observation.usage_details` | JSON: `{"input": N, "output": N, "total": N}` |
| `langfuse.observation.cost_details` | JSON: `{"input": X, "output": X, "total": X}` |
| `langfuse.observation.completion_start_time` | ISO 8601 (time-to-first-token) |
| `langfuse.observation.level` | `"ERROR"` on failure |
| `langfuse.observation.status_message` | Error message |

#### Tool spans

| Attribute | Value |
|---|---|
| `langfuse.observation.type` | `"tool"` |
| `langfuse.observation.input` | Tool call input (JSON, truncated to 10KB) |
| `langfuse.observation.output` | Tool result content (truncated to 10KB) |
| `langfuse.observation.level` | `"ERROR"` on failure |
| `langfuse.observation.status_message` | Error message |

### Integration Points

#### 1. Config (`internal/config/config.go`)

Unchanged from v1:

```go
type LangfuseConfig struct {
    Enabled   bool   `json:"enabled,omitempty"`
    SecretKey string `json:"secretKey,omitempty"`
    PublicKey string `json:"publicKey,omitempty"`
    BaseURL   string `json:"baseURL,omitempty"`
}
```

#### 2. App Initialization (`cmd/root.go`)

Unchanged from v1:

```go
if cfg.Telemetry != nil && cfg.Telemetry.Langfuse != nil && cfg.Telemetry.Langfuse.Enabled {
    if langfuse.Init(lf.PublicKey, lf.SecretKey, lf.BaseURL) {
        defer langfuse.ShutdownGlobal()
    }
}
```

`Shutdown()` now calls `TracerProvider.Shutdown()` which flushes the batch exporter.

#### 3. Provider Layer (`internal/llm/provider/provider.go`)

Generation spans wrap each LLM call:

```go
// In SendMessages():
gen := lf.GenerationStart(ctx, langfuse.GenerationParams{...})
defer gen.End()
resp, err := p.client.send(ctx, messages, tools)
gen.SetUsage(p.buildUsage(resp.Usage))

// In StreamResponse():
gen := lf.GenerationStart(ctx, langfuse.GenerationParams{...})
// goroutine wraps stream, calls gen.SetUsage/SetCompletionStartTime/SetError
defer gen.End()
```

#### 4. Agent Layer (`internal/llm/agent/agent.go`)

**Trace lifecycle** — `createLangfuseTrace()` calls `TraceStart()`, `defer EndTrace(ctx)` ends it:

```go
ctx = a.createLangfuseTrace(ctx, session)
defer langfuse.EndTrace(ctx)
```

**Tool call spans** — wrap each `tool.Run()` in both parallel and sequential execution:

```go
// Parallel tools (goroutines):
toolSpan := lf.ToolStart(ctx, langfuse.ToolParams{Name: tc.Name, Input: tc.Input})
defer toolSpan.End()
result, err := tool.Run(ctx, toolCall)
toolSpan.SetOutput(result.Content)  // or SetError(err)

// Sequential tools:
toolSpan := lf.ToolStart(ctx, langfuse.ToolParams{Name: tc.Name, Input: tc.Input})
result, err := tool.Run(ctx, toolCall)
toolSpan.SetOutput(result.Content)  // or SetError(err)
toolSpan.End()
```

#### 5. Graceful Shutdown

`ShutdownGlobal()` calls `TracerProvider.Shutdown()` with a 5-second timeout, which flushes all pending spans to the OTLP exporter.

## Implementation Plan

### Phase 1: Core Package (v1) ✅
- [x] `internal/langfuse/types.go` — Legacy API types
- [x] `internal/langfuse/client.go` — Custom HTTP batch client
- [x] `internal/langfuse/context.go` — Context helpers (TraceID, SessionID)

### Phase 2: Config ✅
- [x] Add `LangfuseConfig` to `config.go`
- [x] Add validation in `Validate()`
- [x] Update JSON schema generation

### Phase 3: Provider Integration (v1) ✅
- [x] Wrap `SendMessages()` with generation create/update
- [x] Wrap `StreamResponse()` with generation create/update + completionStartTime

### Phase 4: Agent Integration (v1) ✅
- [x] Create trace in `processGeneration()`
- [x] Handle title/summarizer traces

### Phase 5: OTEL Migration (v2) ✅
- [x] Replace custom HTTP batch client with OTEL `TracerProvider` + OTLP HTTP exporter
- [x] Rewrite `types.go` — simplified param structs (TraceParams, GenerationParams, ToolParams, Usage)
- [x] Rewrite `client.go` — OTEL-backed Client with TraceStart/GenerationStart/ToolStart
- [x] New `span.go` — nil-safe Span handle with SetUsage/SetError/SetOutput/SetCompletionStartTime
- [x] Rewrite `context.go` — root span context key (replaces manual TraceID/SessionID)
- [x] Update provider layer — GenerationStart returns Span, defer Span.End()
- [x] Update agent layer — TraceStart/EndTrace lifecycle, `defer langfuse.EndTrace(ctx)`

### Phase 6: Tool Call Tracing (v2) ✅
- [x] Add ToolStart/ToolEnd to client API
- [x] Wrap parallel tool execution with tool spans (goroutine-safe)
- [x] Wrap sequential tool execution with tool spans
- [x] Capture tool input, output (truncated), error state, timing

### Phase 7: Verification ✅
- [x] `go build ./...` — clean
- [x] `go test ./...` — all pass
- [x] JSON schema unchanged (config not affected)
- [x] Manual test with Langfuse cloud (pending user validation)

## Cost Calculation

Token costs are calculated using the existing model cost fields:

```go
inputCost  = float64(usage.InputTokens) * model.CostPer1MIn / 1_000_000
outputCost = float64(usage.OutputTokens) * model.CostPer1MOut / 1_000_000
```

Cache tokens use `CostPer1MInCached` rates when available. Costs are sent as `langfuse.observation.cost_details` JSON attribute.

## Dependencies Added (v2)

| Package | Purpose |
|---|---|
| `go.opentelemetry.io/otel` | Core OTEL API (trace, attribute) |
| `go.opentelemetry.io/otel/sdk/trace` | TracerProvider, BatchSpanProcessor |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp` | OTLP HTTP exporter |
| `go.opentelemetry.io/otel/sdk/resource` | Service name/version resource |
| `go.opentelemetry.io/otel/semconv/v1.26.0` | Semantic conventions |

Note: `go.opentelemetry.io/otel` and `go.opentelemetry.io/otel/trace` were already indirect deps (from gRPC instrumentation). The OTEL SDK and OTLP exporter are new direct deps.

## Risks & Mitigations

| Risk | Mitigation |
|---|---|
| Langfuse unreachable | OTEL batch exporter logs warning, never blocks. Drops spans if queue full |
| High event volume | OTEL BatchSpanProcessor handles batching (default 512 max batch, 1s interval) |
| Sensitive data in tool I/O | Tool input/output truncated to 10KB. No LLM prompt/completion content logged |
| Shutdown before flush | `TracerProvider.Shutdown()` with 5s timeout flushes remaining spans |
| Config key leaks | Support `env:VAR_NAME` syntax, same as existing `apiKey` fields |
| Legacy API deprecation | Already migrated to OTEL endpoint — no dependency on legacy API |
| OTEL SDK version drift | Pinned to v1.43.0; standard well-maintained library |
