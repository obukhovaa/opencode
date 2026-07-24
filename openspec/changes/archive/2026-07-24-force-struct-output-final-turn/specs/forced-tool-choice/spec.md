## ADDED Requirements

### Requirement: Anthropic request builder can force a single named tool

The Anthropic client family request builder — native Anthropic, AWS Bedrock, GCP Vertex, and Moonshot/Kimi, which all share the Anthropic request builder — SHALL support forcing a specific named tool for a single request via a context signal. When the request context carries a non-empty forced-tool name, the builder MUST set `tool_choice` so the model is compelled to call that tool on that request. The forced tool MUST already be present in the request's tool set.

Providers outside the Anthropic client family (e.g. OpenAI, Gemini) are NOT required to honor the signal; they MUST ignore it without erroring, leaving their request unchanged. Callers that rely on forced output MUST therefore treat forcing as best-effort.

#### Scenario: Anthropic request forces the named tool

- **WHEN** an Anthropic request is built and the context carries the forced-tool signal set to `struct_output`
- **THEN** the resulting request MUST set `tool_choice` to the `struct_output` tool

#### Scenario: Non-Anthropic provider ignores the signal

- **WHEN** a provider outside the Anthropic client family builds a request while the forced-tool signal is set
- **THEN** the provider MUST NOT error
- **AND** MAY leave its request unchanged (no forced `tool_choice`)

#### Scenario: Absent signal preserves provider defaults

- **WHEN** an Anthropic request is built and the context does not carry the forced-tool signal
- **THEN** the request MUST NOT set a forced `tool_choice`
- **AND** MUST preserve the provider's existing thinking / reasoning behavior unchanged

### Requirement: Forcing a tool disables extended thinking for that request

Because the Anthropic Messages API rejects a forced `tool_choice` while extended thinking is enabled, the Anthropic request builder MUST omit all thinking configuration (both `thinking` and `OutputConfig`) for a request that carries the forced-tool signal, regardless of the model's usual reasoning-effort configuration.

#### Scenario: Reasoning-effort model is forced without thinking

- **WHEN** a model that would normally request extended thinking builds a request carrying the forced-tool signal
- **THEN** the request MUST omit `thinking` and `OutputConfig`
- **AND** MUST set the forced `tool_choice`
