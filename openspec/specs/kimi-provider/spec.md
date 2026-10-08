# Kimi Provider

## Purpose

Defines Kimi (Moonshot AI) as a first-class model provider served through Moonshot's Anthropic-compatible endpoint (`https://api.moonshot.ai/anthropic`) — the integration path Moonshot documents and maintains for Claude Code. The provider rides the shared anthropic client, inheriting streaming thinking/tool deltas, adaptive-thinking request shaping, attachment handling, auto-compaction, and thinking-block replay (see `thinking-block-replay`). Covers model registration (`kimi.kimi-k3`), credential resolution from environment variables, request-shaping defaults (adaptive thinking, effort `max`), and graceful degradation for auxiliary endpoints the compat surface may not implement (count_tokens). Also covers the K2.7 Code models on the same endpoint, and Kimi K3 as served by AWS Bedrock (`bedrock.kimi-k3`, `bedrock.us-kimi-k3`), which rides the bedrock provider's OpenAI chat-completions path while keeping K3's effort semantics.

## Requirements

### Requirement: Kimi K3 is a registered model provider
The system SHALL register a `kimi` model provider with model `kimi.kimi-k3` (API model `kimi-k3`) declaring: 1,000,000-token context window, default max output tokens 131072, reasoning support with adaptive thinking and maximum effort, attachment (vision) support, and pricing of $3.00/1M input, $15.00/1M output, $0.30/1M cached reads.

#### Scenario: Model available in the registry
- **WHEN** the application starts
- **THEN** `kimi.kimi-k3` is present in `SupportedModels` with provider `kimi` and the declared capabilities, and appears in the generated `.opencode.json` schema's model enum

#### Scenario: Agent configured with kimi model
- **WHEN** an agent's config sets `"model": "kimi.kimi-k3"` and a kimi API key is available
- **THEN** provider construction succeeds and requests use the anthropic Messages dialect

### Requirement: Kimi K2.7 Code models are registered
The system SHALL register `kimi.kimi-k2.7-code` (API model `kimi-k2.7-code`) and `kimi.kimi-k2.7-code-highspeed` (API model `kimi-k2.7-code-highspeed`) on the `kimi` provider, each declaring: 262,144-token context window, default max output tokens 32768, reasoning support with adaptive thinking and maximum effort, attachment (vision) support, and pricing of $0.95/1M input, $4.00/1M output, $0.19/1M cached reads for Code — doubled ($1.90 / $8.00 / $0.38) for Highspeed.

#### Scenario: Models available in the registry
- **WHEN** the application starts
- **THEN** both K2.7 Code model IDs are present in `SupportedModels` with provider `kimi` and the declared capabilities, and appear in the generated `.opencode.json` schema's model enum

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

### Requirement: Kimi K3 reasoning requests default to maximum effort with adaptive thinking
Requests for `kimi.kimi-k3` SHALL send `thinking: {type: "adaptive"}` with `output_config.effort` defaulting to `max` when the agent does not configure a reasoning effort. A user-configured effort value SHALL be passed through. The K2.7 Code models accept every level from `low` to `max`; an unset effort on them SHALL keep the anthropic client's default rather than being pinned to `max`.

#### Scenario: Effort unset
- **WHEN** an agent uses `kimi.kimi-k3` without setting `reasoningEffort`
- **THEN** the resolved agent config carries reasoning effort `max` and the request carries `output_config.effort: "max"` with adaptive thinking

#### Scenario: Effort explicitly configured
- **WHEN** an agent sets `reasoningEffort: "max"` (or another value)
- **THEN** the configured value is sent unchanged

#### Scenario: K2.7 Code effort unset
- **WHEN** an agent uses `kimi.kimi-k2.7-code` without setting `reasoningEffort`
- **THEN** the resolved agent config carries no reasoning effort and the request carries the anthropic client's default effort (`high`) with adaptive thinking

### Requirement: Kimi K3 on Bedrock keeps K3's effort semantics
The system SHALL register `bedrock.kimi-k3` (API model `global.moonshotai.kimi-k3`) and `bedrock.us-kimi-k3` (API model `us.moonshotai.kimi-k3`, priced at the 10% regional premium) on the `bedrock` provider, served through the OpenAI chat-completions client with requests rewritten onto Bedrock's `/model/{id}/invoke` and `/model/{id}/invoke-with-response-stream` routes and the EventStream reply re-encoded as SSE. Reasoning SHALL be requested through the top-level `reasoning_effort` field with K3's levels: an unset effort SHALL resolve to `max` at config load, `low`/`medium`/`high`/`max` SHALL pass through unchanged, and `xhigh` SHALL fold to `high` with a warning. Streamed `delta.reasoning_content` SHALL surface as thinking deltas, and `prompt_tokens_details.cache_write_tokens` SHALL be reported as cache-creation tokens (billed at the cache-write price) separately from `cached_tokens` reads.

#### Scenario: Effort unset on Bedrock
- **WHEN** an agent uses `bedrock.kimi-k3` without setting `reasoningEffort`
- **THEN** the resolved agent config carries reasoning effort `max` and the request body carries `reasoning_effort: "max"`

#### Scenario: Max is not downgraded
- **WHEN** an agent sets `reasoningEffort: "max"` on `bedrock.us-kimi-k3`
- **THEN** the request body carries `reasoning_effort: "max"`; it is not coerced to an OpenAI level

#### Scenario: Flow step override
- **WHEN** a flow step overrides `reasoningEffort` for an agent on `bedrock.kimi-k3`
- **THEN** `max` passes `models.ValidateReasoningEffort` and `xhigh` is rejected

### Requirement: Missing count_tokens endpoint degrades quietly
When an Anthropic-dialect endpoint responds 404 or 405 to a token-count request, the client SHALL mark the endpoint unsupported for the remainder of the session and resolve subsequent token counts via the local estimation strategy without further count_tokens HTTP calls.

#### Scenario: Endpoint without count_tokens
- **WHEN** a count_tokens call returns HTTP 404
- **THEN** the call falls back to the local estimate, and subsequent iterations skip the HTTP call entirely while auto-compaction continues to function against the model's context window

#### Scenario: Endpoint with count_tokens
- **WHEN** count_tokens succeeds
- **THEN** behavior is unchanged from other anthropic-dialect providers (endpoint result floored by the local estimate)

### Requirement: Kimi requests carry no document blocks
Moonshot's Anthropic-compatible endpoint rejects every `document` content block — base64 PDF and plain-text source alike — with a bare `400 invalid_request_error` ("Invalid request Error") on both `/v1/messages` and `/v1/messages/count_tokens`, while text and image blocks pass. Because attachments persist in session history, one such block fails every later turn of the session. Requests to a `kimi` provider model SHALL therefore never contain a document block: a PDF attachment SHALL be replaced by the unsupported-attachment note naming its saved path, a valid UTF-8 `text/*` attachment SHALL be inlined as a text block headed `[Attached file, saved at "<path>"]` (or `[Attached file]` when unsaved), and image attachments SHALL keep their image blocks. Token counting SHALL use the same conversion. Other anthropic-dialect providers keep document blocks. Kimi K3 on Bedrock is unaffected: its OpenAI chat-completions path sends a PDF as a `file` part, which Bedrock accepts.

#### Scenario: PDF in a Kimi session
- **WHEN** a session on `kimi.kimi-k3` holds a PDF attachment saved at `.opencode/bridge/media/scan.pdf`
- **THEN** the request carries a text block noting the omitted attachment and that path, no document block, and the turn succeeds; the model can read the file through file tools

#### Scenario: Text file in a Kimi session
- **WHEN** a session on a `kimi` model holds a `text/plain` attachment
- **THEN** its content is sent as a text block under the attachment header, not as a document block

#### Scenario: Token count of a session with attachments
- **WHEN** count_tokens runs for a `kimi` model session holding PDF or text attachments
- **THEN** the count_tokens request body carries no document block and the endpoint answers with a count instead of a 400

#### Scenario: Claude on the anthropic client
- **WHEN** the same history is sent to a Claude model
- **THEN** the PDF and text attachments are sent as document blocks, unchanged

#### Scenario: Kimi K3 on Bedrock
- **WHEN** a session on `bedrock.kimi-k3` holds a PDF attachment
- **THEN** the PDF is sent as a chat-completions `file` part with a data URL and the model reads its content

### Requirement: Kimi is part of the public config contract
The generated `.opencode.json` schema SHALL list `kimi` among known provider keys and include kimi model IDs in agent model enums; the README SHALL document the provider and its environment variables.

#### Scenario: Schema regenerated
- **WHEN** `go run cmd/schema/main.go` runs after this change
- **THEN** the emitted schema's provider enum contains `kimi` and the model enum contains `kimi.kimi-k3`, `kimi.kimi-k2.7-code`, `kimi.kimi-k2.7-code-highspeed`, `bedrock.kimi-k3` and `bedrock.us-kimi-k3`
