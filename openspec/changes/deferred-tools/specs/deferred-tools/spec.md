# deferred-tools

On-demand tool loading: agents opt in to marking enabled tools as *deferred*
so their schemas stay out of the request payload until discovered via tool
search. Covers the config surface, the `toolsearch` tool, native (Anthropic
server-side tool search) and fallback activation paths, announcement blocks,
and cache-stability guarantees.

## ADDED Requirements

### Requirement: Deferral is opt-in per agent and default-off

Agents SHALL accept a `deferredTools` map (`map[string]bool`) in
`.opencode.json` agent config and in markdown-agent YAML frontmatter, merged
with the same precedence rules as the existing `tools` map and declared in
the generated JSON schema. A tool is deferred only when it is enabled, it
matches a truthy `deferredTools` pattern (exact match wins over wildcard,
most-specific wildcard wins), and it is not a hard-excluded tool. Pattern
matching SHALL be case-insensitive. When an agent has no `deferredTools`
config, runtime behavior and provider request payloads MUST be identical to
the pre-feature system: no wrapper, no `toolsearch` tool, no prompt additions.

#### Scenario: No config means no change

- **WHEN** an agent without `deferredTools` runs a session
- **THEN** its toolset, system prompt, and serialized provider request are identical to the pre-feature behavior

#### Scenario: Wildcard deferral of MCP tools

- **WHEN** an agent config sets `"deferredTools": {"jira_*": true}`
- **THEN** every enabled MCP tool whose name matches `jira_*` (case-insensitively) is deferred
- **AND** builtin tools not matching any pattern are unaffected

#### Scenario: Viper round-trip preserves semantics despite key case-folding

- **WHEN** `.opencode.json` declares `deferredTools` keys containing uppercase (e.g. `"mcp_Slack_*": true`) and the config is loaded through viper
- **THEN** the loaded patterns still match the intended tools, verified by a `viper.Unmarshal` round-trip unit test

#### Scenario: Hard exclusions cannot be deferred

- **WHEN** `deferredTools` matches `toolsearch` or `struct_output` (directly or via wildcard)
- **THEN** those tools remain fully loaded and callable

#### Scenario: Disabled tools are unaffected by deferral

- **WHEN** a tool is disabled via `tools` and matched by `deferredTools`
- **THEN** the tool is not created at all — deferral never resurrects a disabled tool

### Requirement: toolsearch tool discovers and activates deferred tools

The system SHALL register a `toolsearch` tool in the agent's toolset when
the agent has at least one deferred tool and the resolved model does not use
the native path; the system MUST NOT register `toolsearch` when nothing is
deferred. `toolsearch` SHALL support exact
name queries, `select:name1,name2` multi-select, and keyword search scored
over tool names and descriptions with a minimum-score floor. Matching tools
SHALL be activated (subsequent requests include their full schema) and their
full contract (name, description, parameters, required fields) returned in
the tool result wrapped in `<system-reminder>` tags. A query matching
nothing SHALL return the list of still-deferred tool names; already-activated
tools SHALL be excluded from search results.

#### Scenario: Keyword search activates a deferred tool

- **WHEN** the model calls `toolsearch` with `query: "send slack message"` and a deferred `mcp_slack_send_message` tool exists
- **THEN** the result contains that tool's full contract and the tool is activated for subsequent requests

#### Scenario: No-match reply teaches the model

- **WHEN** `toolsearch` matches no deferred tool
- **THEN** the result states no match and lists the available deferred tool names

#### Scenario: toolsearch absent without deferred tools

- **WHEN** an agent has no deferred tools
- **THEN** `toolsearch` is not registered in its toolset

### Requirement: Native activation path on models with SupportsToolSearch

Models SHALL carry a `SupportsToolSearch` capability flag, set only where
the serving endpoint executes Anthropic server-side tool search (Claude
models on anthropic, bedrock, vertexai; NOT kimi, openai-compatible, or
gemini). For such models the anthropic client SHALL: emit deferred tools
with their full schema plus `defer_loading: true` (the flag remains set
after activation); include the GA server tool-search tool
(`tool_search_tool_regex_20251119`) instead of the client-side `toolsearch`;
place the tools-section `cache_control` breakpoint on the last non-deferred
tool; and persist and replay `server_tool_use` / `tool_search_tool_result`
(including `tool_reference` blocks) message parts across requests so
discovered schemas remain expanded for the rest of the session.

#### Scenario: Single-turn discovery and call

- **WHEN** a Claude model on the native path searches for a deferred tool
- **THEN** the server tool search returns tool references in the same response stream
- **AND** the model can invoke the referenced tool within the same assistant turn

#### Scenario: Cache prefix survives activation

- **WHEN** a session on the native path activates a deferred tool between turn N and N+1
- **THEN** the serialized tools section and system prompt bytes that form the cache prefix are unchanged between the two turns

#### Scenario: Breakpoint never lands on a deferred tool

- **WHEN** the tools array ends with one or more deferred tools
- **THEN** the ephemeral cache breakpoint is attached to the last non-deferred tool definition

#### Scenario: Kimi is excluded from the native path

- **WHEN** an agent with deferred tools resolves a kimi model (anthropic client, Moonshot compat endpoint)
- **THEN** the fallback path is used: no `defer_loading` flags, no server tool-search tool, client-side `toolsearch` registered

### Requirement: Fallback activation path preserves the tool prefix

On models without `SupportsToolSearch`, providers SHALL omit non-activated
deferred tools from the request payload entirely. After `toolsearch`
activates a tool, subsequent requests SHALL include it **appended after**
the stable tool ordering (baseline insertion order, then externals sorted by
name), never inserted mid-list, so previously serialized tool positions are
unchanged. Activation state SHALL live on the tool wrapper (not in message
history), survive compaction, apply per agent instance, and carry across a
mid-session provider or model switch.

#### Scenario: Two-turn activation on OpenAI-compatible models

- **WHEN** the model calls `toolsearch` for a deferred tool on the fallback path
- **THEN** the current request's payload did not contain the tool, the result carries its contract as text, and the next request's payload includes it appended after the previously sent tools

#### Scenario: Compaction does not reset activation

- **WHEN** a session compacts after tools were activated
- **THEN** activated tools remain included in subsequent requests without re-searching

### Requirement: Deferred tools are announced without breaking cache

When an agent defers at least one tool, the system prompt SHALL gain a
`<system-reminder>` block explaining the tag convention and listing the
deferred **builtin** tool names with the instruction to load them via tool
search — computed from config at prompt-build time so it is identical on
every request. Deferred MCP tools (which resolve asynchronously) SHALL be
announced via a user-role delta message listing newly available deferred
names, injected only when the deferred pool actually changes, never on every
turn. Both blocks MUST be absent for agents with no deferred tools.

#### Scenario: Static announcement is cache-stable

- **WHEN** an agent defers builtin tools and runs multiple turns
- **THEN** the system prompt bytes, including the announcement block, are identical across turns

#### Scenario: MCP delta announced once

- **WHEN** deferred MCP tools finish resolving after the session started
- **THEN** exactly one delta message announces the new names
- **AND** no further delta is injected until the deferred pool changes again

### Requirement: Both activation paths are e2e-verified

The repository SHALL ship two e2e checks under `scripts/test/`. The
fallback-path check MUST be fully self-contained: it drives a session
against an in-process mock OpenAI-compatible server (the `cmd/*-e2e` driver
pattern) and asserts the wire-level contract — the first request omits the
deferred tool and includes `toolsearch`; after a `toolsearch` call the next
request includes the activated tool appended after the previously sent
tools; and with no `deferredTools` config the serialized tools payload is
byte-identical to a run without the feature. The native-path check runs a
real session against an Anthropic model with server-side tool search and
MUST skip (not fail) when no Anthropic credential is available in the
environment; when it runs, it asserts the request carried
`defer_loading: true` plus the server tool-search tool, and that the model
successfully discovered and invoked a deferred tool.

#### Scenario: Fallback e2e runs offline

- **WHEN** `make test-e2e` runs on a machine with no provider credentials
- **THEN** the fallback deferred-tools script executes against the local mock and passes
- **AND** the native-path script reports SKIP rather than failing

#### Scenario: Native e2e verifies live server-side tool search

- **WHEN** the native-path script runs with an Anthropic credential present
- **THEN** the session's requests include `defer_loading: true` on deferred tools and the server tool-search tool
- **AND** the model invokes a deferred tool that was discovered via tool search during the session
