# YandexCloud Provider

**Date**: 2026-04-05
**Status**: Draft
**Author**: AI-assisted

## Overview

Add YandexCloud as a new LLM provider in OpenCode. YandexCloud AI Studio exposes an OpenAI-compatible Completions API at `https://llm.api.cloud.yandex.net/v1`, so the implementation reuses the existing `OpenAIClient` with YandexCloud-specific configuration for authentication and model URI construction.

## Motivation

### Current State

OpenCode supports five providers: `anthropic`, `openai`, `gemini`, `vertexai`, `bedrock`, plus `local` for generic OpenAI-compatible endpoints. Users who want to use YandexCloud models must configure them through the `local` provider, which has limitations:

```json
{
  "providers": {
    "local": {
      "apiKey": "...",
      "baseURL": "https://llm.api.cloud.yandex.net/v1"
    }
  }
}
```

This creates problems:

1. **No model registry**: Local models are discovered dynamically at startup by querying the endpoint. YandexCloud does not expose a `/v1/models` listing endpoint the way Ollama does, so no models are discovered.
2. **No folder-ID handling**: YandexCloud model URIs follow the `gpt://<folder_id>/<model-name>` pattern. The folder ID must be baked into the model string sent in each request. The local provider has no mechanism for this.
3. **No first-class UX**: Users cannot simply set `YANDEXCLOUD_API_KEY` and have models appear; they must manually wire everything.

### Desired State

```json
{
  "agents": {
    "coder": {
      "model": "yandexcloud.yandexgpt-pro-5"
    }
  },
  "providers": {
    "yandexcloud": {
      "apiKey": "AQVN..."
    }
  }
}
```

Or via environment variables:

```bash
export YANDEXCLOUD_API_KEY="AQVN..."
export YANDEXCLOUD_FOLDER_ID="b1g..."
```

Models appear in the registry out of the box. The provider handles folder-ID injection into model URIs transparently.

## Research Findings

### YandexCloud OpenAI Compatibility

YandexCloud AI Studio is fully OpenAI-compatible. From their official docs, tutorials, and the [yandex-ai-studio-sdk](https://github.com/yandex-cloud/yandex-ai-studio-sdk) source code:

- **Base URL**: `https://llm.api.cloud.yandex.net/v1`
- **Auth**: Standard Bearer token via `Authorization: Bearer <api_key_or_iam_token>`
- **Folder ID**: Carried **only** in the `model` field of the request body as `gpt://<folder_id>/model/version`. The SDK does NOT send `x-folder-id` header for the OpenAI-compatible endpoint (that header is for the older native REST/gRPC API). For reference, the `/models` listing endpoint uses `OpenAI-Project: <folder_id>` header, but completions rely solely on the model URI.
- **Streaming**: Supported via standard OpenAI streaming protocol
- **Function calling / tools**: Supported — all text models support tool invocation
- **Max tokens**: Uses `max_tokens` (legacy OpenAI parameter), NOT `max_completion_tokens`. Confirmed by SDK source — `_build_request_json` sends `max_tokens` key, zero occurrences of `max_completion_tokens` in the entire repo.

**Key finding**: The OpenAI Go SDK's `option.WithBaseURL` is sufficient to integrate YandexCloud. No custom middleware, headers, or request transformation is needed (unlike Bedrock/VertexAI).

**Implication**: The implementation is structurally identical to `ProviderLocal` — route to `newOpenAIClient` with custom base URL — but with a hardcoded model registry and folder-ID injection into the `APIModel` string.

### Authentication Methods

YandexCloud supports multiple auth methods:

- **API key** (simplest, does not expire): Created in IAM console, used as Bearer token directly
- **IAM token** (short-lived, ~12h): Exchanged from OAuth token or service account JWT
- **OAuth token**: For Yandex account users

For OpenCode, API key is the right choice — it matches the existing `apiKey` config pattern and does not expire.

### Available Models (Common Instance)

From the official models page, the following text generation models support the OpenAI-compatible API:

| Model | Model Path (in URI) | Context | Notes |
|---|---|---|---|
| Alice AI LLM | `aliceai-llm` | 32,768 | Yandex flagship, best for chat |
| YandexGPT Pro 5.1 | `yandexgpt/rc` | 32,768 | RC branch, newer generation |
| YandexGPT Pro 5 | `yandexgpt/latest` | 32,768 | Stable branch |
| YandexGPT Lite 5 | `yandexgpt-lite` | 32,768 | Fastest, cheapest Yandex model |
| DeepSeek V3.2 | `deepseek-v32/` | 131,072 | Open-source, hosted on YC |
| Qwen3 235B | `qwen3-235b-a22b-fp8/latest` | 262,144 | Largest Qwen, MoE |
| Qwen3.5 35B | `qwen3.5-35b-a3b-fp8` | 262,144 | Newest Qwen, small MoE |
| gpt-oss-120b | `gpt-oss-120b/latest` | 131,072 | Yandex open-source model |

Full model URI sent to the API: `gpt://<folder_id>/<model_path>`

### Pricing (Synchronous Mode, USD)

Prices from [the official pricing page](https://aistudio.yandex.ru/docs/en/ai-studio/pricing.html). Per 1,000 tokens, without VAT. The `CostPer1M*` fields in the Model struct need values multiplied by 1,000.

YandexCloud supports automatic prompt caching: *"Caching is enabled automatically where possible and applicable. Caching is not guaranteed and does not apply to output tokens."* No explicit API parameters are needed — it works like OpenAI's automatic caching. Native Yandex models happen to price cached tokens at the same rate as regular input tokens (no discount), while some hosted open-source models offer a lower cached rate.

| Model | Input / 1K tok | Cached / 1K tok | Output / 1K tok | CostPer1MIn | CostPer1MInCached | CostPer1MOut |
|---|---|---|---|---|---|---|
| Alice AI LLM | $0.00409836 | $0.00409836 | $0.009836064 | $4.098 | $4.098 | $9.836 |
| YandexGPT Pro 5.1 | $0.006557376 | $0.006557376 | $0.006557376 | $6.557 | $6.557 | $6.557 |
| YandexGPT Pro 5 | $0.009836064 | $0.009836064 | $0.009836064 | $9.836 | $9.836 | $9.836 |
| YandexGPT Lite 5 | $0.001639344 | $0.001639344 | $0.001639344 | $1.639 | $1.639 | $1.639 |
| DeepSeek V3.2 | $0.00409836 | $0.001065574 | $0.006557376 | $4.098 | $1.066 | $6.557 |
| Qwen3 235B | $0.00409836 | $0.00409836 | $0.00409836 | $4.098 | $4.098 | $4.098 |
| gpt-oss-120b | $0.002459016 | $0.002459016 | $0.002459016 | $2.459 | $2.459 | $2.459 |
| Qwen3.5 35B | $0.001639344 | $0.000409836 | $0.002459016 | $1.639 | $0.410 | $2.459 |

### Existing Provider Patterns

| Pattern | Example | Approach |
|---|---|---|
| Direct client | `openai`, `anthropic` | Own SDK client, own model registry |
| Wrapper provider | `bedrock`, `vertexai` | Delegates to another provider's client with middleware |
| OpenAI-reuse | `local` | Uses `newOpenAIClient` with custom base URL, dynamic model discovery |

**YandexCloud fits the "OpenAI-reuse" pattern** but with static model registry instead of dynamic discovery. Closest to how `ProviderLocal` works, but with hardcoded models.

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Client implementation | Reuse `newOpenAIClient` | API is fully OpenAI-compatible; no custom protocol needed |
| Model registry | Static, hardcoded in `models/yandexcloud.go` | YandexCloud does not expose `/v1/models` listing; models are well-documented and stable |
| Model ID format | `yandexcloud.<name>` (e.g., `yandexcloud.yandexgpt-pro-5`) | Matches existing convention: `bedrock.<name>`, `vertexai.<name>` |
| APIModel storage | Store only the model path (e.g., `yandexgpt/latest`), NOT the full `gpt://` URI | Folder ID is config, not model metadata. The provider constructs the full `gpt://<folder_id>/<apiModel>` URI at init time. |
| Folder ID delivery | Embedded in the `model` field of the request body only | Verified in [yandex-ai-studio-sdk](https://github.com/yandex-cloud/yandex-ai-studio-sdk): the SDK puts `gpt://<folder_id>/model` in the JSON body `model` field. No `x-folder-id` header is sent for the OpenAI-compatible endpoint. |
| Folder ID source | Read `YANDEXCLOUD_FOLDER_ID` env var directly in `NewProvider` switch case | Same pattern as VertexAI which reads `VERTEXAI_PROJECT` directly in `newVertexAIClient`. No provider-specific fields added to the shared `Provider` config struct. |
| Provider name | `yandexcloud` | Consistent with cloud-provider naming (`vertexai`, `bedrock`) |
| Auth env var | `YANDEXCLOUD_API_KEY` | Consistent with `OPENAI_API_KEY`, `ANTHROPIC_API_KEY` pattern |
| `UseLegacyMaxTokens` | `true` | Confirmed: YandexCloud uses `max_tokens`, not `max_completion_tokens`. Verified in SDK source. |
| Models to register | 8 models (see model table) | Alice AI LLM, YandexGPT Pro 5.1/5, YandexGPT Lite 5, DeepSeek V3.2, Qwen3 235B, Qwen3.5 35B, gpt-oss-120b |
| Pricing | USD per 1M tokens from official pricing page | Yandex publishes USD prices directly (for non-Russian entities); no conversion needed |

## Architecture

```
User config                       Provider routing                    API call
─────────────                     ────────────────                    ────────
.opencode.json / env              provider.go NewProvider()           OpenAI SDK
┌──────────────────┐              ┌──────────────────┐               ┌──────────────────┐
│ providers:       │              │ case "yandexcloud"│               │ POST /v1/chat/   │
│   yandexcloud:   │──────────▶   │   opts.baseURL =  │──────────▶   │   completions    │
│     apiKey: ...  │              │     "https://..."  │              │ Host: llm.api.   │
│                  │              │   apiModel =       │              │   cloud.yandex.. │
│ env:             │              │     gpt://fid/path │              │ Auth: Bearer ... │
│ YANDEXCLOUD_     │              │   newOpenAIClient()│              │ body.model:      │
│   FOLDER_ID=...  │              └──────────────────┘               │   gpt://fid/...  │
└──────────────────┘                     │                           └──────────────────┘
                                         ▼
                                  ┌──────────────────┐
                                  │ models/yandex.go  │
                                  │ YandexCloudModels │
                                  │   ID: yandexcloud │
                                  │     .yandexgpt-.. │
                                  │   APIModel: model │
                                  │     path only     │
                                  └──────────────────┘
```

### Folder ID and APIModel Construction

The `APIModel` field in model definitions stores only the model path (e.g., `yandexgpt/latest`). At provider construction time, the provider:

1. Reads the folder ID from config (sourced from `YANDEXCLOUD_FOLDER_ID` env var or a config field)
2. Constructs the full model URI: `gpt://<folder_id>/<apiModel>`
3. Sets `opts.model.APIModel` to the full URI
4. Passes everything to `newOpenAIClient` — no special headers needed

```
STEP 1: Config resolution
──────────────────────────
Read YANDEXCLOUD_API_KEY from env → providers.yandexcloud.apiKey
Read YANDEXCLOUD_FOLDER_ID from env → stored for URI construction

STEP 2: Model APIModel construction
────────────────────────────────────
For model "yandexcloud.yandexgpt-pro-5":
  APIModel (stored): "yandexgpt/latest"
  Folder ID (from config): "b1g..."
  APIModel (constructed): "gpt://b1g.../yandexgpt/latest"

STEP 3: OpenAI client construction
───────────────────────────────────
newOpenAIClient(opts) with:
  apiKey = YANDEXCLOUD_API_KEY
  baseURL = "https://llm.api.cloud.yandex.net/v1"
  model.APIModel = "gpt://b1g.../yandexgpt/latest"
  openaiOptions = {legacyMaxTokens: true}
```

## Implementation Plan

### Phase 1: Model definitions

- [ ] **1.1** Create `internal/llm/models/yandexcloud.go`
  - Define `ProviderYandexCloud ModelProvider = "yandexcloud"`
  - Define `YandexCloudModels map[ModelID]Model` with 8 model entries:

```go
// Model ID → APIModel (path only), Name, Context, Pricing per 1M tokens
"yandexcloud.aliceai-llm"       → "aliceai-llm",                 "Alice AI LLM",       32768,  In:4.098,  Cached:4.098,  Out:9.836
"yandexcloud.yandexgpt-pro-5.1" → "yandexgpt/rc",                "YandexGPT Pro 5.1",  32768,  In:6.557,  Cached:6.557,  Out:6.557
"yandexcloud.yandexgpt-pro-5"   → "yandexgpt/latest",            "YandexGPT Pro 5",    32768,  In:9.836,  Cached:9.836,  Out:9.836
"yandexcloud.yandexgpt-lite-5"  → "yandexgpt-lite",              "YandexGPT Lite 5",   32768,  In:1.639,  Cached:1.639,  Out:1.639
"yandexcloud.deepseek-v3.2"     → "deepseek-v32/",               "DeepSeek V3.2",      131072, In:4.098,  Cached:1.066,  Out:6.557
"yandexcloud.qwen3-235b"        → "qwen3-235b-a22b-fp8/latest",  "Qwen3 235B",         262144, In:4.098,  Cached:4.098,  Out:4.098
"yandexcloud.qwen3.5-35b"       → "qwen3.5-35b-a3b-fp8",         "Qwen3.5 35B",        262144, In:1.639,  Cached:0.410,  Out:2.459
"yandexcloud.gpt-oss-120b"      → "gpt-oss-120b/latest",         "gpt-oss-120b",       131072, In:2.459,  Cached:2.459,  Out:2.459
```

  - All models: `UseLegacyMaxTokens: true`
  - Set `DefaultMaxTokens` per model to maximize output capacity:
    - YandexGPT / Alice AI (32K context): `DefaultMaxTokens: 8192`
    - DeepSeek V3.2 (131K context): `DefaultMaxTokens: 8192` (DeepSeek v3 standard output limit)
    - Qwen3 235B (262K context): `DefaultMaxTokens: 32768`
    - Qwen3.5 35B (262K context): `DefaultMaxTokens: 32768`
    - gpt-oss-120b (131K context): `DefaultMaxTokens: 8192`

- [ ] **1.2** Register in `internal/llm/models/bootstrap.go`
  - Add `maps.Copy(SupportedModels, YandexCloudModels)` in `init()`

### Phase 2: Provider routing and config

- [ ] **2.1** Add yandexcloud case to `NewProvider` switch in `internal/llm/provider/provider.go`
  - Set `opts.baseURL` to `https://llm.api.cloud.yandex.net/v1` if not overridden by config
  - Read folder ID directly from `os.Getenv("YANDEXCLOUD_FOLDER_ID")` — same pattern as VertexAI reads `VERTEXAI_PROJECT` in `newVertexAIClient`
  - If folder ID is empty, return error: "YandexCloud provider requires folder_id — set YANDEXCLOUD_FOLDER_ID env var"
  - Construct full APIModel: `opts.model.APIModel = "gpt://" + folderID + "/" + opts.model.APIModel`
  - Add `WithOpenAIOptions(WithLegacyMaxTokens(true))`
  - Return `newBaseProvider(newOpenAIClient(opts), opts)`

- [ ] **2.2** Add env var defaults in `internal/config/config.go` `setProviderDefaults()`
  - `YANDEXCLOUD_API_KEY` → `providers.yandexcloud.apiKey`
  - Add `getProviderAPIKey` case for `ProviderYandexCloud`
  - Note: `YANDEXCLOUD_FOLDER_ID` is NOT wired through config — it is read directly in the provider, same as `VERTEXAI_PROJECT`

### Phase 3: Documentation and schema

- [ ] **3.1** Update README.md with YandexCloud provider info
- [ ] **3.2** Regenerate `opencode-schema.json` via `go run cmd/schema/main.go > opencode-schema.json`
- [ ] **3.3** Run `make test` to verify nothing breaks

## Edge Cases

### Missing folder ID

1. User configures `YANDEXCLOUD_API_KEY` but not `YANDEXCLOUD_FOLDER_ID`
2. Provider construction should fail early with a clear error message
3. Do not silently produce broken model URIs like `gpt:///yandexgpt/latest`

### Model versioning (branches)

1. YandexCloud models have `/latest`, `/rc`, `/deprecated` branches
2. Different branches may point to different model generations (e.g., `yandexgpt/latest` is Pro 5, `yandexgpt/rc` is Pro 5.1)
3. We register distinct model entries for each relevant branch rather than exposing branch as a config option

### Base URL override

1. User sets `providers.yandexcloud.baseURL` to a custom endpoint (e.g., for testing or a proxy)
2. The provider should respect the override, same as other providers
3. Only use the default `https://llm.api.cloud.yandex.net/v1` if no override is set

### Token counting

1. `OpenAIClient.countTokens()` returns `errors.ErrUnsupported` — falls back to local estimation
2. This is acceptable; YandexCloud does not expose a token-counting endpoint via OpenAI-compatible API

## Resolved Questions

1. **Trailing slash in DeepSeek V3.2 model path** → Preserve as documented (`deepseek-v32/`); test and adjust if needed.
2. **Folder ID storage** → Option (c): read `YANDEXCLOUD_FOLDER_ID` directly via `os.Getenv` in the `NewProvider` switch case, same pattern as VertexAI reads `VERTEXAI_PROJECT` in `newVertexAIClient`. No changes to `Provider` config struct.
3. **DefaultMaxTokens** → Model-specific values: 8192 for 32K/131K-context models, 32768 for 262K-context Qwen models.

## Success Criteria

- [ ] `yandexcloud` appears as a valid provider in config
- [ ] Setting `YANDEXCLOUD_API_KEY` and `YANDEXCLOUD_FOLDER_ID` enables YandexCloud models
- [ ] All 8 registered models appear in model selection
- [ ] Chat completions work with at least one YandexCloud model via the OpenAI-compatible API
- [ ] Streaming responses work correctly
- [ ] Tool use / function calling works with at least one model
- [ ] Missing folder ID produces a clear error
- [ ] Pricing information is correctly set on all models
- [ ] `make test` passes
- [ ] Schema is regenerated

## References

- `internal/llm/models/models.go` — Model struct definition
- `internal/llm/models/openai.go` — Example model definitions for reference
- `internal/llm/models/bootstrap.go` — Model registration
- `internal/llm/provider/provider.go` — Provider routing, `NewProvider` switch, `providerClientOptions`
- `internal/llm/provider/openai.go` — OpenAI client (will be reused)
- `internal/config/config.go` — Provider config, env var defaults
- `spec/20260201T102141-cleanup-models-and-providers.md` — Previous provider cleanup spec
- [YandexCloud AI Studio models](https://aistudio.yandex.ru/docs/en/ai-studio/concepts/generation/models.html)
- [YandexCloud AI Studio pricing](https://aistudio.yandex.ru/docs/en/ai-studio/pricing.html)
- [YandexCloud OpenAI compatibility](https://yandex.cloud/en/docs/ai-studio/concepts/openai-compatibility)
- [YandexCloud VS Code integration tutorial](https://yandex.cloud/en/docs/tutorials/ml-ai/ai-model-ide-integration)
- [Yandex AI Studio SDK (Python)](https://github.com/yandex-cloud/yandex-ai-studio-sdk) — confirmed `max_tokens` usage, no `x-folder-id` header for completions
