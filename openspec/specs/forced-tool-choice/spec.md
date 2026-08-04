# forced-tool-choice Specification

## Purpose
TBD - created by archiving change force-struct-output-final-turn. Update Purpose after archive.
## Requirements
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

### Requirement: Agent loop forces struct_output on the max-turns wrap-up for schema-bearing runs

When the agentic loop reaches its max-turns limit and the run's tool set includes the `struct_output` tool (i.e. a schema-bearing step), the final wrap-up turn MUST force `tool_choice=struct_output` via the forced-tool-choice mechanism and MUST capture the resulting `struct_output` as the run's output. The runtime MUST NOT discard a `struct_output` tool call produced on this turn, and MUST NOT instead request a free-text summary for such a run.

A run whose tool set does NOT include `struct_output` (a plain, no-schema step) keeps the existing behavior: a text wrap-up turn whose stray tool calls are discarded.

If the forced wrap-up turn errors, or yields no non-error `struct_output`, the loop MUST fall back to returning whatever `struct_output` was captured on an earlier turn (possibly none), without a hard failure — the flow layer's own guard then applies.

#### Scenario: Schema-bearing run at max turns forces and captures struct_output

- **WHEN** the agentic loop reaches max turns on a run whose tool set includes `struct_output`, and the model never called `struct_output` on a normal turn
- **THEN** the final wrap-up turn MUST be issued with `struct_output` forced via `tool_choice`
- **AND** a non-error `struct_output` returned on that turn MUST be captured as the run's structured output
- **AND** that `struct_output` tool call MUST NOT be discarded

#### Scenario: Plain run at max turns still gets a text wrap-up

- **WHEN** the agentic loop reaches max turns on a run whose tool set does NOT include `struct_output`
- **THEN** the final wrap-up turn MUST request a free-text final response
- **AND** any tool call the model makes on that turn MUST be discarded

#### Scenario: Forced wrap-up turn yields nothing usable

- **WHEN** the forced max-turns wrap-up turn errors or returns no non-error `struct_output`
- **THEN** the loop MUST NOT hard-fail on that basis
- **AND** MUST return whatever `struct_output` (if any) was captured on an earlier turn

