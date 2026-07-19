# Tasks — Add Kimi Provider

## 1. Model registration

- [x] 1.1 Create `internal/llm/models/kimi.go`: `ProviderKimi = "kimi"`, `KimiK3 = "kimi.kimi-k3"` (`APIModel: "kimi-k3"`, ContextWindow 1_000_000, DefaultMaxTokens 131072, CanReason, SupportsAdaptiveThinking, SupportsMaximumThinking, SupportsAttachments, costs 3.0 / 15.0 / cached-in 3.0 / cached-out 0.30)
- [x] 1.2 Register in `bootstrap.go` (`maps.Copy(SupportedModels, KimiModels)`) and add `ProviderKimi: 7` to `ProviderPopularity`

## 2. Provider routing

- [x] 2.1 `provider.go` `NewProvider`: `case models.ProviderKimi` — default `baseURL` to `https://api.moonshot.ai/anthropic` when unset, return `baseProvider[AnthropicClient]` (Bearer auth follows from the existing baseURL→WithAuthToken path)
- [x] 2.2 `agent.go` `createAgentProvider`: include `ProviderKimi` in the anthropic-options branch (shouldThink fn, adaptive reasoning effort passthrough, disableCache passthrough; task budget stays gated on `SupportsTaskBudget`)
- [x] 2.3 anthropic.go `countTokens`: sticky disable on HTTP 404/405 (atomic flag on the client) returning `errors.ErrUnsupported` thereafter, so the provider layer's local-estimate fallback engages without per-iteration HTTP

## 3. Config wiring

- [x] 3.1 Env defaults: `MOONSHOT_API_KEY` → `providers.kimi.apiKey`, `KIMI_API_KEY` fallback (viper defaults, matching the existing per-provider pattern)
- [x] 3.2 `getProviderAPIKey`: `ProviderKimi` case (MOONSHOT_API_KEY then KIMI_API_KEY)
- [x] 3.3 Reasoning-effort validation: kimi reasoning models with empty effort default to `max` (adaptive branch; explicit values pass through, `max` already permitted via `SupportsMaximumThinking`)
- [x] 3.4 `setDefaultModelForAgent`: append lowest-priority kimi block (all agents → `KimiK3`, descriptor maxTokens 80, effort `max`) when a kimi key is the only credential

## 4. Public contract

- [x] 4.1 `cmd/schema/main.go`: add `kimi` (and missing `yandexcloud`) to `knownProviders`
- [x] 4.2 Regenerate `opencode-schema.json` (`go run cmd/schema/main.go > opencode-schema.json`) and verify `kimi.kimi-k3` appears in model enums
- [x] 4.3 README: document the kimi provider (env vars, default endpoint, K3 model row) alongside existing providers

## 5. Tests

- [x] 5.1 Config tests: MOONSHOT_API_KEY populates `providers.kimi.apiKey`; KIMI_API_KEY fallback; explicit config wins; kimi agent with empty effort resolves to `max` (mirror existing provider env tests)
- [x] 5.2 Provider test: `NewProvider(ProviderKimi)` succeeds, default base URL applied when unset, override respected
- [x] 5.3 anthropic client test: countTokens 404 → sticky ErrUnsupported (no second HTTP call), 200 path unchanged
- [x] 5.4 Run `go test ./internal/config/ ./internal/llm/provider/ ./internal/llm/models/ ./internal/llm/agent/`

## 6. Live verification — DEFERRED (blocked on MOONSHOT_API_KEY availability)

Deferred at archive time, 2026-07-19: no Moonshot credential exists yet in this
environment; every unverified live behavior has a safe in-code fallback
(count_tokens → sticky local estimation; cache fields → zero attribution merely
overstates cost; effort defaults to the only documented level `max`). The spike
script (`kimi_spike.sh`, tests T1–T13) is prepared. When a key is available:
run the spike against `/anthropic` (effort acceptance, temperature, tool loop
with/without thinking replay, cache usage fields, count_tokens, SSE event
vocabulary), fold contradicting findings back into model flags/defaults, and
smoke-test a TUI session on `kimi.kimi-k3` (thinking display, tool round-trip,
cost accounting).
