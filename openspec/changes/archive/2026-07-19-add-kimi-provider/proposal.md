# Add Kimi Provider (Moonshot AI, Kimi K3)

## Why

Kimi K3 (released 2026-07-16) is a frontier-class open-weight coding/agentic model — 1M context, native vision, always-on reasoning — at $3/$15 per 1M tokens with $0.30 cache hits. Moonshot maintains an **Anthropic-compatible endpoint** (`https://api.moonshot.ai/anthropic`) specifically so Anthropic-native agents (Claude Code and alike) work against K3 unchanged, including adaptive thinking (`thinking: {type: adaptive}` + `output_config.effort: max`), streaming thinking deltas, and tool-use streaming. Routing a new `kimi` provider through opencode's existing anthropic client yields near-full feature parity with our Anthropic experience for a small, low-risk diff — versus the openai-client path, which would silently drop reasoning (`reasoning_content` extension), misread cache usage, and clamp the required `max` reasoning effort.

## What Changes

- New model provider `kimi` with model `kimi.kimi-k3` (1M context window, adaptive+max thinking, attachments/vision, $3/$15/$0.30 pricing).
- `NewProvider` routes `ProviderKimi` to the existing anthropic client with default base URL `https://api.moonshot.ai/anthropic` (Bearer auth via the existing baseURL→AuthToken path). `providers.kimi.baseURL` remains overridable.
- Config wiring: `MOONSHOT_API_KEY` (primary) / `KIMI_API_KEY` (fallback) env defaults for `providers.kimi.apiKey`; `getProviderAPIKey` support; kimi added as the lowest-priority auto-default provider for agents when it is the only key present (effort `max`).
- Reasoning effort defaults to `max` for kimi models when unset (K3 currently exposes only `max`; Moonshot's own Claude Code guide mandates effort max).
- Anthropic client `countTokens` learns to disable itself for the session after a 404/405 (endpoints without `count_tokens` degrade to the existing local estimation without per-iteration HTTP).
- `cmd/schema/main.go` known-provider enum gains `kimi` (and the missing `yandexcloud`, a pre-existing gap); `opencode-schema.json` regenerated; README providers/models updated.
- Depends on `preserve-thinking-blocks` for Moonshot's documented reasoning replay contract ("always preserve the reasoning_content of each historical assistant message").

## Capabilities

### New Capabilities

- `kimi-provider`: Kimi (Moonshot) as a first-class model provider over its Anthropic-compatible endpoint — model registration, credential/env wiring, request shaping defaults (adaptive thinking, effort max), and graceful degradation for unsupported auxiliary endpoints.

### Modified Capabilities

<!-- none — no existing spec covers provider registration -->

## Impact

- New: `internal/llm/models/kimi.go`. Modified: `internal/llm/models/bootstrap.go`, `internal/llm/models/models.go` (popularity), `internal/llm/provider/provider.go`, `internal/llm/provider/anthropic.go` (count_tokens disable-on-404), `internal/llm/agent/agent.go` (anthropic-options branch), `internal/config/config.go` (env defaults, provider key lookup, effort default, agent auto-defaults), `cmd/schema/main.go` + regenerated `opencode-schema.json`, `README.md`.
- `.opencode.json` public contract: new `providers.kimi` key and `kimi.kimi-k3` model ID in the generated schema (additive).
- No runtime dependency changes (reuses anthropic-sdk-go).
- Live-endpoint behaviors that need a `MOONSHOT_API_KEY` to confirm (count_tokens support, cache usage field mapping, effort-value tolerance) are captured as verification tasks with safe fallbacks either way.
