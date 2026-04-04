# Provider Metadata

**Date**: 2026-04-04
**Status**: Draft
**Author**: AI-assisted

## Overview

Add optional per-provider metadata configuration that attaches `session_id` and `user_id` key-value pairs to every LLM API request. Metadata keys are declared in provider config; values are resolved at runtime from context (session) or environment.

## Motivation

### Current State

```json
{
  "providers": {
    "anthropic": {
      "apiKey": "...",
      "headers": { "X-Custom": "value" }
    }
  }
}
```

```go
type Provider struct {
    APIKey   string            `json:"apiKey"`
    Disabled bool              `json:"disabled"`
    BaseURL  string            `json:"baseURL"`
    Headers  map[string]string `json:"headers,omitempty"`
}
```

Providers send requests without any identifying metadata. There is no way to correlate API usage with sessions or users for billing, abuse detection, or observability.

### Desired State

```json
{
  "providers": {
    "anthropic": {
      "metadata": ["session_id", "user_id"]
    }
  }
}
```

Each provider attaches declared metadata to every request using the provider's native mechanism.

## Research Findings

### SDK Metadata Support

| Provider | SDK Version | Native Metadata Field | Notes |
|----------|------------|----------------------|-------|
| Anthropic | v1.21.0 | `MetadataParam.UserID` | Only `user_id` is a typed field. `session_id` not in SDK. |
| OpenAI | v0.1.0-beta.2 | `shared.Metadata` (`map[string]string`) | Free-form key-value map on `ChatCompletionNewParams`. |
| Gemini | google.golang.org/genai v1.43.0 | None | No API-level metadata. `HTTPOptions.ExtraBody` can inject arbitrary JSON into request body. |
| Bedrock | — | Delegates to Anthropic | Inherits Anthropic behavior. |
| VertexAI | — | Delegates to Anthropic or Gemini | Inherits from child provider. |

### Anthropic `session_id` Support

The Anthropic Go SDK `MetadataParam` struct has exactly one field: `UserID`. There is no `SessionID` field.

Escape hatches available:
- `option.WithJSONSet("metadata.session_id", value)` — injects arbitrary JSON path into the request body per call
- `MetadataParam.SetExtraFields(map[string]any{"session_id": "..."})` — adds extra fields to the metadata object

**Key finding**: `option.WithJSONSet` is cleanest because it doesn't require mutating the params struct and works per-request on both `Messages.New()` and `Messages.NewStreaming()`. Both methods accept variadic `...option.RequestOption` as trailing args.

**Existing usage**: `anthropic.go:342` already reads `ctx.Value(toolsPkg.SessionIDContextKey)` for debug request logging — so the pattern of extracting session ID from context in the Anthropic provider is established.

### Gemini ExtraBody

`GenerateContentConfig.HTTPOptions` has `ExtraBody map[string]any` which merges arbitrary JSON into the request body. This is useful for LiteLLM proxy passthrough scenarios where metadata can be extracted from the body during forwarding.

**Key finding**: Gemini API itself ignores unknown fields, but proxy layers (LiteLLM) can extract them. Also, there is an existing bug where `stream()` in `gemini.go` does not apply `HTTPOptions` headers — this should be fixed as part of this work.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Config field type | `[]MetadataKey` (enum of allowed string values) | We control value resolution; arbitrary keys are meaningless since we wouldn't know what value to send. |
| Allowed keys | `session_id`, `user_id` only | Start small; extend later. Validate at config load. |
| `session_id` source | Read from `context.Context` via `SessionIDContextKey` | Session ID is per-request, changes with each session. |
| `user_id` source | `OPENCODE_USER_ID` env var; if unset, generate UUID at process startup | Stable identifier for the process lifetime. UUID format per Anthropic docs recommendation. |
| Anthropic `session_id` | `option.WithJSONSet("metadata.session_id", value)` | SDK has no typed field; `WithJSONSet` is the cleanest per-request injection. |
| Anthropic `user_id` | Native `MetadataParam.UserID` field | SDK supports this natively. |
| OpenAI metadata | `ChatCompletionNewParams.Metadata` map | Free-form `map[string]string`, pass both keys directly. |
| Gemini metadata | `HTTPOptions.ExtraBody` with `metadata` map | No native metadata; ExtraBody enables LiteLLM proxy extraction. Users can omit metadata config if it causes issues. |
| Gemini ExtraBody merge | Merge into existing ExtraBody, don't overwrite | User or other code may set ExtraBody for other purposes; metadata injection must not clobber it. |
| OpenAI `user_id` destination | `Metadata` map (not `User` field) | OpenAI has a dedicated `User string` field on `ChatCompletionNewParams`, but using `Metadata` map is more consistent across providers and avoids special-casing. |
| Config validation | Reject unknown and duplicate metadata keys at config load | Fail fast on typos and duplicates; only `session_id` and `user_id` are valid. |
| Where metadata is resolved | At request time in `send()`/`stream()` methods | `session_id` must come from ctx, so resolution can't happen at init. |

## Architecture

```
┌──────────────────────────────────────────────────┐
│  .opencode.json                                  │
│  providers.anthropic.metadata: [session_id, …]   │
└──────────────────────┬───────────────────────────┘
                       │ config load + validate
                       ▼
┌──────────────────────────────────────────────────┐
│  config.Provider                                 │
│  ├── Metadata []MetadataKey                      │
│  └── (existing: APIKey, Headers, …)              │
└──────────────────────┬───────────────────────────┘
                       │ WithMetadata(providerCfg.Metadata)
                       ▼
┌──────────────────────────────────────────────────┐
│  providerClientOptions                           │
│  ├── metadata []config.MetadataKey               │
│  └── (existing fields)                           │
└──────────────────────┬───────────────────────────┘
                       │ per-request resolution
                       ▼
┌──────────────────────────────────────────────────┐
│  resolveMetadata(ctx, keys) map[string]string    │
│  ├── session_id → ctx.Value(SessionIDContextKey) │
│  └── user_id    → processUserID (env or UUID)    │
└──────────┬───────────┬───────────┬───────────────┘
           │           │           │
     ┌─────▼──┐  ┌─────▼──┐  ┌────▼───┐
     │Anthropic│  │ OpenAI │  │ Gemini │
     │Metadata │  │Metadata│  │ExtraBody│
     │Param +  │  │  map   │  │metadata│
     │WithJSON │  │        │  │  map   │
     │Set      │  │        │  │        │
     └────────┘  └────────┘  └────────┘
```

### Value Resolution

```
STEP 1: Process startup
────────────────────────
Read OPENCODE_USER_ID env var.
If empty, generate UUIDv4 and store in package-level var.
This value is stable for the process lifetime.

STEP 2: Per-request metadata resolution
────────────────────────────────────────
resolveMetadata(ctx, []MetadataKey) → map[string]string
  - session_id: ctx.Value(SessionIDContextKey).(string)
  - user_id: package-level processUserID variable

STEP 3: Provider-specific injection
────────────────────────────────────
Anthropic: MetadataParam{UserID: F(val)} + option.WithJSONSet for session_id
OpenAI:    params.Metadata = shared.Metadata{...}
Gemini:    config.HTTPOptions.ExtraBody = map[string]any{"metadata": {...}}
```

## Implementation Plan

### Phase 1: Config and plumbing

- [ ] **1.1** Define `MetadataKey` type and constants (`session_id`, `user_id`) in `internal/config/config.go`
- [ ] **1.2** Add `Metadata []MetadataKey` field to `config.Provider` struct
- [ ] **1.3** Add config validation: reject unknown metadata keys during config load
- [ ] **1.4** Add `metadata []config.MetadataKey` field to `providerClientOptions` in `provider.go`
- [ ] **1.5** Add `WithMetadata([]config.MetadataKey) ProviderClientOption` constructor
- [ ] **1.6** Wire `WithMetadata` in `agent.go` where provider options are built from config

### Phase 2: Value resolution

- [ ] **2.1** Use existing `tools.SessionIDContextKey` from `internal/llm/tools/tools.go` (already defined as `sessionIDContextKey("session_id")`, already set in context in `agent.go:209`)
- [ ] **2.2** Implement `processUserID` init: read `OPENCODE_USER_ID` env, fallback to UUIDv4 generated once at startup (use existing `github.com/google/uuid v1.6.0` dependency)
- [ ] **2.3** Implement `resolveMetadata(ctx context.Context, keys []config.MetadataKey) map[string]string` in `provider.go`

### Phase 3: Provider implementations

- [ ] **3.1** Anthropic: modify `preparedMessages()` to accept metadata; set `MetadataParam.UserID` for `user_id`; return `[]option.RequestOption` with `WithJSONSet` for `session_id`; update `send()` and `stream()` to pass request options
- [ ] **3.2** OpenAI: modify `preparedParams()` to accept `ctx`; set `params.Metadata` map from resolved values
- [ ] **3.3** Gemini: inject metadata via `HTTPOptions.ExtraBody` as `{"metadata": {"session_id": "...", "user_id": "..."}}` in both `send()` and `stream()` config construction; merge into existing ExtraBody if present (don't overwrite); fix existing bug where `stream()` doesn't apply `HTTPOptions.Headers`
- [ ] **3.4** Bedrock/VertexAI: verify metadata flows through delegation (no changes expected)

### Phase 4: Schema and tests

- [ ] **4.1** Regenerate `opencode-schema.json` via `go run cmd/schema/main.go`
- [ ] **4.2** Add config validation tests for valid and invalid metadata keys
- [ ] **4.3** Add unit tests for `resolveMetadata` function
- [ ] **4.4** Verify `make test` passes

## Edge Cases

### Missing session ID in context

1. `session_id` is configured but `ctx` has no `SessionIDContextKey` value
2. `resolveMetadata` returns empty string for that key
3. Key is omitted from the metadata map (don't send empty values)

### Provider doesn't support metadata natively

1. Gemini receives `metadata` config
2. ExtraBody is used — the Gemini API ignores unknown fields
3. LiteLLM or other proxy layers may extract it; direct Gemini calls silently ignore it

### Duplicate metadata keys in config

1. User provides `["session_id", "session_id"]` in metadata array
2. Config validation rejects with an error about duplicate keys
3. User fixes config to `["session_id"]`

### Anthropic API rejects unknown metadata fields

1. `session_id` is injected via `WithJSONSet("metadata.session_id", ...)`
2. If the API returns an error for unknown fields, the request fails
3. User removes `session_id` from metadata config to resolve — this is acceptable since the feature is opt-in

## Success Criteria

- [ ] Provider metadata is configurable via `providers.<name>.metadata` in `.opencode.json`
- [ ] Only `session_id` and `user_id` are accepted; unknown keys cause config validation error
- [ ] Anthropic requests include `metadata.user_id` and `metadata.session_id` when configured
- [ ] OpenAI requests include `metadata` map when configured
- [ ] Gemini requests include metadata in `ExtraBody` when configured
- [ ] `user_id` is read from `OPENCODE_USER_ID` env var or auto-generated as UUID
- [ ] `session_id` is read from request context per call
- [ ] Empty/missing values are omitted from metadata
- [ ] `make test` passes
- [ ] JSON schema is regenerated

## References

- `internal/config/config.go` — `Provider` struct, config validation
- `internal/llm/provider/provider.go` — `providerClientOptions`, `ProviderClientOption` pattern
- `internal/llm/provider/anthropic.go` — `preparedMessages()`, `send()`, `stream()`
- `internal/llm/provider/openai.go` — `preparedParams()`, `send()`, `stream()`
- `internal/llm/provider/gemini.go` — `GenerateContentConfig` construction, `HTTPOptions.ExtraBody`
- `internal/llm/agent/agent.go` — provider option wiring from config (line ~1380), session ID context injection (line 209)
- `internal/llm/tools/tools.go` — existing `SessionIDContextKey` definition (line 34)
- Anthropic SDK `option.WithJSONSet`: `github.com/anthropics/anthropic-sdk-go/option/requestoption.go`
- OpenAI SDK `shared.Metadata`: `github.com/openai/openai-go/shared/shared.go` — `type Metadata map[string]string`
- `github.com/google/uuid v1.6.0` — already in go.mod, use for user ID generation
