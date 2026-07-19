# kimi-provider Specification

## ADDED Requirements

### Requirement: Kimi K3 is a registered model provider
The system SHALL register a `kimi` model provider with model `kimi.kimi-k3` (API model `kimi-k3`) declaring: 1,000,000-token context window, default max output tokens 131072, reasoning support with adaptive thinking and maximum effort, attachment (vision) support, and pricing of $3.00/1M input, $15.00/1M output, $0.30/1M cached reads.

#### Scenario: Model available in the registry
- **WHEN** the application starts
- **THEN** `kimi.kimi-k3` is present in `SupportedModels` with provider `kimi` and the declared capabilities, and appears in the generated `.opencode.json` schema's model enum

#### Scenario: Agent configured with kimi model
- **WHEN** an agent's config sets `"model": "kimi.kimi-k3"` and a kimi API key is available
- **THEN** provider construction succeeds and requests use the anthropic Messages dialect

### Requirement: Kimi requests use the Anthropic-compatible endpoint with Bearer auth
Kimi provider requests SHALL default to base URL `https://api.moonshot.ai/anthropic`, authenticate with the configured API key as a Bearer token, and honor a user-configured `providers.kimi.baseURL` override.

#### Scenario: Default base URL
- **WHEN** `providers.kimi.apiKey` is set and no base URL override exists
- **THEN** requests target `https://api.moonshot.ai/anthropic` with `Authorization: Bearer <key>`

#### Scenario: Base URL override
- **WHEN** the user sets `providers.kimi.baseURL`
- **THEN** requests target the override instead of the default

### Requirement: Kimi credentials resolve from environment variables
The system SHALL default `providers.kimi.apiKey` from `MOONSHOT_API_KEY`, falling back to `KIMI_API_KEY`, with explicit config taking precedence over both.

#### Scenario: MOONSHOT_API_KEY set
- **WHEN** only `MOONSHOT_API_KEY` is set in the environment
- **THEN** the kimi provider is enabled with that key

#### Scenario: KIMI_API_KEY fallback
- **WHEN** `MOONSHOT_API_KEY` is unset and `KIMI_API_KEY` is set
- **THEN** the kimi provider is enabled with the `KIMI_API_KEY` value

#### Scenario: Kimi is the only credential
- **WHEN** a kimi key is the only provider credential present and no agent models are configured
- **THEN** agent defaults resolve to `kimi.kimi-k3` with reasoning effort `max`

### Requirement: Kimi reasoning requests default to maximum effort with adaptive thinking
Requests for kimi reasoning models SHALL send `thinking: {type: "adaptive"}` with `output_config.effort` defaulting to `max` when the agent does not configure a reasoning effort. A user-configured effort value SHALL be passed through.

#### Scenario: Effort unset
- **WHEN** an agent uses `kimi.kimi-k3` without setting `reasoningEffort`
- **THEN** the resolved agent config carries reasoning effort `max` and the request carries `output_config.effort: "max"` with adaptive thinking

#### Scenario: Effort explicitly configured
- **WHEN** an agent sets `reasoningEffort: "max"` (or another value)
- **THEN** the configured value is sent unchanged

### Requirement: Missing count_tokens endpoint degrades quietly
When an Anthropic-dialect endpoint responds 404 or 405 to a token-count request, the client SHALL mark the endpoint unsupported for the remainder of the session and resolve subsequent token counts via the local estimation strategy without further count_tokens HTTP calls.

#### Scenario: Endpoint without count_tokens
- **WHEN** a count_tokens call returns HTTP 404
- **THEN** the call falls back to the local estimate, and subsequent iterations skip the HTTP call entirely while auto-compaction continues to function against the model's context window

#### Scenario: Endpoint with count_tokens
- **WHEN** count_tokens succeeds
- **THEN** behavior is unchanged from other anthropic-dialect providers (endpoint result floored by the local estimate)

### Requirement: Kimi is part of the public config contract
The generated `.opencode.json` schema SHALL list `kimi` among known provider keys and include kimi model IDs in agent model enums; the README SHALL document the provider and its environment variables.

#### Scenario: Schema regenerated
- **WHEN** `go run cmd/schema/main.go` runs after this change
- **THEN** the emitted schema's provider enum contains `kimi` and the model enum contains `kimi.kimi-k3`
