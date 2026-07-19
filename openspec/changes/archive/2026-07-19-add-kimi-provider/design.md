# Design — Kimi Provider

## Context

Moonshot exposes two API dialects for Kimi K3:

| | `https://api.moonshot.ai/v1` (primary) | `https://api.moonshot.ai/anthropic` (compat) |
|---|---|---|
| Dialect | OpenAI Chat Completions + extensions | Anthropic Messages |
| Reasoning | `reasoning_content` extension field (dropped by openai-go structs); effort only `"max"` | `thinking: {type: adaptive}` + `output_config.effort: "max"`; `thinking_delta` SSE |
| Usage/cache | top-level `usage.cached_tokens` (not where openai-go reads) | Anthropic usage shape (mapping to be spot-verified) |
| Multi-turn contract | "always preserve reasoning_content of each historical assistant message" | thinking-block replay (see `preserve-thinking-blocks`) |
| Official consumer | their SDK docs | **Claude Code** (documented setup: `ANTHROPIC_BASE_URL=…/anthropic`, `ANTHROPIC_MODEL=kimi-k3`, Bearer auth, `CLAUDE_CODE_EFFORT_LEVEL=max`) |

opencode's stack is Anthropic-native: the anthropic client already produces exactly the compat endpoint's documented request shape — adaptive thinking config, effort passthrough (`config.go`'s adaptive-model validation accepts `max` gated on `SupportsMaximumThinking`), Bearer auth when a base URL is set (`anthropic.go:121-125`), streaming with thinking/tool deltas, and count_tokens with automatic local-estimate fallback. The `local`/`yandexcloud` providers establish the precedent of a provider constant mapping onto a shared client with a default base URL.

## Goals / Non-Goals

**Goals:**
- `kimi.kimi-k3` usable everywhere a model ID is accepted (agents, TUI switcher, flows) with thinking display, tool streaming, vision, auto-compaction against the 1M window, and cost accounting.
- Zero-config beyond an API key: sane base URL, effort default `max`, agent auto-defaults when kimi is the only credential.
- Quiet degradation for auxiliary endpoints Moonshot may not implement (count_tokens).

**Non-Goals:**
- A dedicated Kimi client on the `/v1` OpenAI dialect (revisit if the compat endpoint proves limiting; the model registration and config wiring built here carry over).
- Third-party hosts (OpenRouter, self-hosted vLLM after the July 27 weights drop) — reachable today via the `local` provider; first-class support is out of scope.
- K2.x model family registration (K3 only; additive later).
- Kimi-specific context-caching controls — their caching is automatic server-side; we only account for it in usage/cost.

## Decisions

1. **Route `ProviderKimi` through the anthropic client** (`NewProvider` case → `baseProvider[AnthropicClient]`, default base URL `https://api.moonshot.ai/anthropic`). Alternatives: (a) openai client reuse — rejected: silent reasoning loss (extension fields not in openai-go structs), `reasoning_effort` clamped to low/medium/high by both `WithReasoningEffort` and config branch 1, cache reads misattributed (top-level `cached_tokens` vs `prompt_tokens_details`), and it violates Moonshot's replay contract; (b) new dedicated client — rejected for now: ~500 lines to reimplement what the compat endpoint + anthropic client already deliver, and Moonshot's strongest compatibility investment is exactly this endpoint (it's their Claude Code funnel).

2. **Distinct provider constant (`kimi`) rather than anthropic-with-baseURL.** Users could point `providers.anthropic.baseURL` at Moonshot, but model IDs, pricing, context windows, and env-key resolution are per-provider; a first-class constant gives correct cost math, model registry entries, schema enum, and independent credentials alongside a real Anthropic account.

3. **Model flags: `CanReason + SupportsAdaptiveThinking + SupportsMaximumThinking + SupportsAttachments`.** This makes `preparedMessages` emit `thinking: adaptive` + `output_config.effort` and `temperature: 1` — matching K3's locked `temperature=1.0` and documented thinking contract. `SupportsXHighThinking`/`SupportsTaskBudget` stay false (`xhigh` unsupported by K3; task budgets are an Anthropic beta gated behind a beta header we must not send). `DefaultMaxTokens: 131072` per K3's documented default output cap.

4. **Effort defaults to `max` when unset, at config-validation time.** The adaptive-thinking validation branch keys on model flags (provider-agnostic) and currently leaves empty effort alone, letting anthropic.go default to `high` — K3 documents only `max` (their Claude Code guide sets `CLAUDE_CODE_EFFORT_LEVEL=max`). Setting the default in `Validate()` (mirroring the openai branch's `medium` default) keeps the resolved value visible in config and testable. Non-`max` user values are passed through unvalidated-by-us (Moonshot may clamp or reject; their error surfaces normally); we do not hard-restrict so lighter levels work the day Moonshot ships them.

5. **Env keys: `MOONSHOT_API_KEY` primary, `KIMI_API_KEY` alias** (checked in that order; Moonshot's own docs use MOONSHOT_API_KEY). Wired as viper defaults for `providers.kimi.apiKey` plus `getProviderAPIKey`. Agent auto-default block appended at lowest priority (after AWS) in `setDefaultModelForAgent`: all agents → `kimi.kimi-k3` (descriptor capped maxTokens 80), effort `max`.

6. **count_tokens disable-on-404.** The anthropic client's `countTokens` gets a sticky per-client guard: on HTTP 404/405 it records "unsupported" (atomic) and short-circuits future calls with `errors.ErrUnsupported`, so `CountTokens` falls back to the local estimate (existing behavior) without an extra HTTP round-trip per agent-loop iteration. Provider-agnostic: also benefits any anthropic-compatible proxy lacking the endpoint. If Moonshot implements count_tokens, the guard never trips and we use it.

7. **Costs: `CostPer1MIn=3.0, CostPer1MOut=15.0, CostPer1MOutCached=0.30, CostPer1MInCached=3.0`.** Moonshot's caching is automatic with no documented write premium; if the endpoint never reports cache-creation tokens, the InCached rate is inert; if it does, billing them at the normal input rate is the safe assumption.

## Risks / Trade-offs

- [Compat endpoint drift] Moonshot could change/deprecate `/anthropic` → base URL is config-overridable; the model registration and config wiring survive a future client swap (non-goal (a) path).
- [Unverified live behaviors] count_tokens support, anthropic-shape cache usage fields, tolerance of effort values ≠ `max` → all have safe fallbacks (local estimation; zero cache attribution merely overstates cost; effort defaults to `max`). A spike script (`kimi_spike.sh`, T1–T13) exists to confirm once a key is available; results feed follow-up tuning, not this change's correctness.
- [Beta-header leakage] User-configured `anthropic-beta` headers would be forwarded to Moonshot → existing `filterBetaHeaders` only strips context-1m for small models; kimi-k3 is 1M so irrelevant; task-budget beta is gated on `SupportsTaskBudget=false`. No default beta headers are sent.
- [Auth shape] Moonshot expects Bearer (`ANTHROPIC_AUTH_TOKEN` semantics); the anthropic client already switches to `WithAuthToken` whenever a base URL is set — covered by a unit test.
- [1M-context cost blowups] Auto-compaction thresholds derive from `ContextWindow=1_000_000` and the local estimator, same as Claude 1M models — no new mechanism.

## Migration Plan

Purely additive: new provider key, new model IDs, regenerated schema. No existing config changes meaning. Rollback = revert. Schema regeneration (`go run cmd/schema/main.go > opencode-schema.json`) committed alongside per the repo contract.

## Open Questions

- Does `/anthropic` implement `count_tokens`? (Safe either way — decision 6.) — spike T8.
- Exact cache usage fields on `/anthropic` responses (cache_read/cache_creation mapping). — spike T7.
- Whether effort values other than `max` are clamped or rejected. — spike T1/T2.
