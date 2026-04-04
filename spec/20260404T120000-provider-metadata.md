# Provider Metadata

**Date**: 2026-04-04
**Status**: Draft
**Author**: AI-assisted

## Overview

Add optional per-provider metadata configuration that attaches identifying key-value pairs (session ID, user ID) to every LLM API request body. Metadata keys are declared in provider config with user-chosen field names; values are resolved at runtime from context or environment.

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

Providers send requests without any identifying metadata. There is no way to correlate API usage with sessions or users for observability, cost tracking, or abuse detection — whether hitting provider APIs directly or through a proxy gateway.

### Desired State

```json
{
  "providers": {
    "anthropic": {
      "metadata": {
        "sessionId": "session_id",
        "userId": "user_id"
      }
    }
  }
}
```

Each provider attaches declared metadata to every request using a `metadata` object in the request body. The config keys (`sessionId`, `userId`) are built-in identifiers that OpenCode knows how to resolve. The values (`session_id`, `user_id`) are the actual field names used in the metadata object sent to the API — letting users match whatever their proxy or provider expects (e.g., `litellm_session_id`, `trace_user_id`, `user_id`).

## Wire Format Contract

All providers MUST produce a `metadata` object in the JSON request body with the user-configured field names. This ensures consistent behavior regardless of which provider or proxy is in use.

For a config of `{"sessionId": "my_session", "userId": "my_user"}`, the request body must contain:

```json
{
  "metadata": {
    "my_session": "<resolved-session-id>",
    "my_user": "<resolved-user-id>"
  },
  ...provider-specific fields...
}
```

Each provider achieves this through its SDK's native mechanism:
- **Anthropic**: `MetadataParam` struct with `SetExtraFields` for non-native fields
- **OpenAI**: `shared.Metadata` map (free-form `map[string]string`)
- **Gemini**: `HTTPOptions.ExtraBody` merged at request body root level

Empty or unresolvable values MUST be omitted from the metadata object. If no values resolve, the metadata object is not sent.

## Research Findings

### SDK Metadata Support

| Provider | SDK Version | Native Metadata Field | Notes |
|----------|------------|----------------------|-------|
| Anthropic | v1.21.0 | `MetadataParam.UserID` | Only `user_id` is a typed field. No other fields in SDK. |
| OpenAI | v0.1.0-beta.2 | `shared.Metadata` (`map[string]string`) | Free-form key-value map on `ChatCompletionNewParams`. |
| Gemini | google.golang.org/genai v1.43.0 | None | No API-level metadata. `HTTPOptions.ExtraBody` injects arbitrary JSON at request body root. |
| Bedrock | — | Delegates to Anthropic | Inherits Anthropic behavior. |
| VertexAI | — | Delegates to Anthropic or Gemini | Inherits from child provider. |

### Anthropic Metadata Injection

The Anthropic Go SDK `MetadataParam` struct has exactly one typed field: `UserID`. For additional fields, `SetExtraFields(map[string]any{...})` adds arbitrary keys to the metadata JSON object.

```go
meta := anthropic.MetadataParam{}
meta.UserID = anthropic.F("some-user-id")
meta.SetExtraFields(map[string]any{"session_id": "some-session-id"})
// serializes to: {"user_id": "some-user-id", "session_id": "some-session-id"}
```

**Key finding**: `SetExtraFields` keeps all metadata on a single struct. When the user-configured field name for `userId` is `"user_id"`, it maps to the SDK's native `MetadataParam.UserID`. For any other field name or for `sessionId`, `SetExtraFields` is used. This also means a future SDK addition of `SessionID` would be a straightforward one-line migration.

**Existing pattern**: `anthropic.go:342` already reads `ctx.Value(toolsPkg.SessionIDContextKey)` for debug logging, so extracting session ID from context in the Anthropic provider is established.

### Gemini ExtraBody Merge Behavior

Confirmed via SDK source (`api_client.go`): in `buildRequest()`, the SDK calls `recursiveMapMerge(body, patchedHTTPOptions.ExtraBody)` which merges ExtraBody into the request body at the **root level**. The merge is recursive — if both body and ExtraBody have a key whose value is `map[string]any`, they are deep-merged; otherwise ExtraBody values overwrite.

For our use case: `ExtraBody = map[string]any{"metadata": map[string]any{...}}` adds a `metadata` key at the body root alongside `contents`, `generationConfig`, etc.

**Key finding**: Gemini's API itself ignores unknown fields, but proxy gateways can extract them from the body. The ExtraBody mechanism works for both `send()` and `stream()` paths since both go through `buildRequest()`.

**Bug**: there is an existing bug where `stream()` in `gemini.go` does not apply `HTTPOptions` headers (the `if len(g.providerOptions.headers) != 0` block is absent). This should be fixed as part of this work.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Config shape | `map[string]string` with built-in keys | Keys identify what value to resolve (`sessionId`, `userId`). Values are the field names in the metadata object, letting users match their proxy's expectations. |
| Built-in keys | `sessionId`, `userId` only | Start small; extend later. Validate at config load. camelCase for consistency with other config fields. |
| Config validation | Reject unknown keys and empty field name values at config load | Fail fast on typos; only `sessionId` and `userId` are valid keys. Field name values must be non-empty strings. |
| `sessionId` source | Read from `context.Context` via `tools.SessionIDContextKey` | Session ID is per-request, changes with each session. Already set in context in `agent.go:209`. |
| `userId` source | `OPENCODE_USER_ID` env var; if unset, generate UUID at process startup | Stable identifier for the process lifetime. UUID format per Anthropic docs recommendation. |
| Anthropic metadata injection | `MetadataParam` with `SetExtraFields` | Single struct for all metadata. Native `UserID` field used when field name is `"user_id"`; `SetExtraFields` for everything else. Clean migration path when SDK adds new typed fields. |
| OpenAI metadata | `ChatCompletionNewParams.Metadata` map | Free-form `map[string]string`, pass all configured fields directly. |
| OpenAI `userId` destination | `Metadata` map (not `User` field) | OpenAI has a dedicated `User string` field on `ChatCompletionNewParams`, but using `Metadata` map is more consistent with the wire format contract. |
| Gemini metadata | `HTTPOptions.ExtraBody` with `metadata` map | No native metadata; ExtraBody merges at request body root via `recursiveMapMerge`. Users can omit metadata config if it causes issues with direct Gemini API. |
| Gemini ExtraBody merge | Merge into existing ExtraBody, don't overwrite | User or other code may set ExtraBody for other purposes; metadata injection must not clobber it. |
| Where metadata is resolved | At request time in `send()`/`stream()` methods | `sessionId` must come from ctx, so resolution can't happen at init. |
| Shared resolution, per-provider injection | `resolveMetadata()` shared; injection is SDK-specific per provider | Each SDK takes fundamentally different types; sharing the map-building logic is the right abstraction boundary. Injection code is 3-5 lines per provider and not worth abstracting further. |

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│  .opencode.json                                          │
│  providers.anthropic.metadata:                           │
│    { "sessionId": "session_id", "userId": "user_id" }   │
└──────────────────────────┬───────────────────────────────┘
                           │ config load + validate
                           ▼
┌──────────────────────────────────────────────────────────┐
│  config.Provider                                         │
│  ├── Metadata  ProviderMetadata                          │
│  └── (existing: APIKey, Headers, …)                      │
└──────────────────────────┬───────────────────────────────┘
                           │ WithMetadata(providerCfg.Metadata)
                           ▼
┌──────────────────────────────────────────────────────────┐
│  providerClientOptions                                   │
│  ├── metadata  *config.ProviderMetadata                  │
│  └── (existing fields)                                   │
└──────────────────────────┬───────────────────────────────┘
                           │ per-request resolution
                           ▼
┌──────────────────────────────────────────────────────────┐
│  resolveMetadata(ctx, meta) map[string]string            │
│  Iterates configured keys, resolves values:              │
│  ├── sessionId → ctx.Value(SessionIDContextKey)          │
│  └── userId    → processUserID (env or generated UUID)   │
│  Maps to user-chosen field names. Omits empty values.    │
└──────────┬───────────────┬───────────────┬───────────────┘
           │               │               │
     ┌─────▼──────┐  ┌────▼─────┐  ┌──────▼──────┐
     │  Anthropic  │  │  OpenAI  │  │   Gemini    │
     │ MetadataParam│ │ Metadata │  │  ExtraBody  │
     │ + SetExtra  │  │   map    │  │  {"metadata" │
     │   Fields    │  │          │  │    : {...}}  │
     └────────────┘  └──────────┘  └─────────────┘
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
resolveMetadata(ctx, *ProviderMetadata) → map[string]any
  For each configured key:
    - sessionId → ctx.Value(tools.SessionIDContextKey)
    - userId    → package-level processUserID variable
  The map keys are the user-configured field names.
  Empty values are omitted from the map.
  Returns nil if map is empty.

STEP 3: Provider-specific injection
────────────────────────────────────
Anthropic:
  meta := anthropic.MetadataParam{}
  if fieldName for userId == "user_id" → meta.UserID = F(val)
  else → add to extraFields
  for all other keys → add to extraFields
  meta.SetExtraFields(extraFields)
  params.Metadata = meta

OpenAI:
  params.Metadata = shared.Metadata(resolved)

Gemini:
  config.HTTPOptions.ExtraBody merge {"metadata": resolved}
```

## Implementation Plan

### Phase 1: Config and plumbing

- [ ] **1.1** Define `ProviderMetadata` struct with `SessionID string` and `UserID string` fields (json: `sessionId`, `userId`) in `internal/config/config.go`
- [ ] **1.2** Add `Metadata *ProviderMetadata` field to `config.Provider` struct
- [ ] **1.3** Add config validation: reject unknown fields (struct handles this via JSON unmarshalling), validate field name values are non-empty strings when set
- [ ] **1.4** Add `metadata *config.ProviderMetadata` field to `providerClientOptions` in `provider.go`
- [ ] **1.5** Add `WithMetadata(*config.ProviderMetadata) ProviderClientOption` constructor
- [ ] **1.6** Wire `WithMetadata` in `agent.go` where provider options are built from config

### Phase 2: Value resolution

- [ ] **2.1** Use existing `tools.SessionIDContextKey` from `internal/llm/tools/tools.go` (already defined as `sessionIDContextKey("session_id")`, already set in context in `agent.go:209`)
- [ ] **2.2** Implement `processUserID` init: read `OPENCODE_USER_ID` env, fallback to UUIDv4 generated once at startup (use existing `github.com/google/uuid v1.6.0` dependency)
- [ ] **2.3** Implement `resolveMetadata(ctx context.Context, meta *config.ProviderMetadata) map[string]string` in `provider.go` — returns a map of field-name → resolved-value, omitting empty values, returning nil if empty

### Phase 3: Provider implementations

- [ ] **3.1** Anthropic: in `send()` and `stream()`, call `resolveMetadata(ctx, ...)`. Build `MetadataParam`: use native `UserID` field when field name is `"user_id"`, use `SetExtraFields` for all other fields. Set `params.Metadata` on the prepared params.
- [ ] **3.2** OpenAI: in `send()` and `stream()`, call `resolveMetadata(ctx, ...)`. Set `params.Metadata = shared.Metadata(resolved)`.
- [ ] **3.3** Gemini: in `send()` and `stream()`, call `resolveMetadata(ctx, ...)`. Merge `{"metadata": resolved}` into `HTTPOptions.ExtraBody` (preserve existing ExtraBody entries). Fix existing bug where `stream()` doesn't apply `HTTPOptions.Headers`.
- [ ] **3.4** Bedrock/VertexAI: verify metadata flows through delegation (no changes expected)

### Phase 4: Schema and tests

- [ ] **4.1** Regenerate `opencode-schema.json` via `go run cmd/schema/main.go`
- [ ] **4.2** Add config validation tests for valid metadata, unknown keys, and empty field name values
- [ ] **4.3** Add unit tests for `resolveMetadata` — test both keys present, one key present, missing ctx value, empty metadata
- [ ] **4.4** Verify `make test` passes

## Edge Cases

### Missing session ID in context

1. `sessionId` is configured but `ctx` has no `SessionIDContextKey` value
2. `resolveMetadata` returns empty string for that key
3. Key is omitted from the metadata map (don't send empty values)

### All metadata values are empty

1. Both `sessionId` and `userId` are configured but neither resolves
2. `resolveMetadata` returns nil
3. No metadata object is sent in the request

### Anthropic native `user_id` field name

1. User configures `"userId": "user_id"` — matches Anthropic SDK's native field
2. Implementation uses `MetadataParam.UserID` for this case
3. All other field names go through `SetExtraFields`

### Anthropic non-standard `user_id` field name

1. User configures `"userId": "trace_user_id"` — does not match SDK native field
2. Implementation uses `SetExtraFields(map[string]any{"trace_user_id": value})`
3. Native `UserID` field is left unset

### Provider doesn't support metadata natively

1. Gemini receives `metadata` config
2. ExtraBody is used — the Gemini API ignores unknown body fields
3. Proxy gateways can extract the `metadata` object from the body; direct API calls silently ignore it

### Gemini ExtraBody conflict

1. User or other code already set ExtraBody with a `metadata` key
2. `recursiveMapMerge` deep-merges: our metadata fields are added to the existing `metadata` map
3. If the existing `metadata` has same-named keys, our values overwrite (standard merge behavior)

### Anthropic API rejects unknown metadata fields

1. A non-standard field is injected via `SetExtraFields`
2. If the API returns an error for unknown fields, the request fails
3. User removes or renames the field in metadata config to resolve — this is acceptable since the feature is opt-in

## Success Criteria

- [ ] Provider metadata is configurable via `providers.<name>.metadata` in `.opencode.json`
- [ ] Only `sessionId` and `userId` are accepted as config keys; field name values must be non-empty
- [ ] All providers produce a `metadata` object in the request body with user-configured field names
- [ ] Anthropic uses native `MetadataParam.UserID` when field name is `"user_id"`, `SetExtraFields` otherwise
- [ ] OpenAI uses `shared.Metadata` map
- [ ] Gemini uses `HTTPOptions.ExtraBody` with root-level merge
- [ ] `userId` value is read from `OPENCODE_USER_ID` env var or auto-generated as UUID at startup
- [ ] `sessionId` value is read from request context per call
- [ ] Empty/missing values are omitted from metadata; empty metadata object is not sent
- [ ] Gemini `stream()` headers bug is fixed
- [ ] `make test` passes
- [ ] JSON schema is regenerated

## References

- `internal/config/config.go` — `Provider` struct, config validation
- `internal/llm/provider/provider.go` — `providerClientOptions`, `ProviderClientOption` pattern
- `internal/llm/provider/anthropic.go` — `preparedMessages()`, `send()`, `stream()`; existing session ID ctx read at line 342
- `internal/llm/provider/openai.go` — `preparedParams()`, `send()`, `stream()`
- `internal/llm/provider/gemini.go` — `GenerateContentConfig` construction, `HTTPOptions.ExtraBody`
- `internal/llm/agent/agent.go` — provider option wiring from config (line ~1380), session ID context injection (line 209)
- `internal/llm/tools/tools.go` — existing `SessionIDContextKey` definition (line 34)
- Anthropic SDK `MetadataParam.SetExtraFields`: `github.com/anthropics/anthropic-sdk-go/message.go`
- OpenAI SDK `shared.Metadata`: `github.com/openai/openai-go/shared/shared.go` — `type Metadata map[string]string`
- Gemini SDK `recursiveMapMerge`: `google.golang.org/genai/api_client.go` — confirms root-level merge of ExtraBody
- `github.com/google/uuid v1.6.0` — already in go.mod, use for user ID generation
