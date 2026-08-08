# deferred-tools

On-demand tool loading: agents opt in to marking enabled tools as *deferred*
so their schemas stay out of the request payload until discovered via tool
search. Covers the config surface, the `toolsearch` tool, native (Anthropic
server-side tool search) and fallback activation paths, announcement blocks,
token accounting, and cache-stability guarantees.

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
the pre-feature system: no wrapper, no `toolsearch` tool, no prompt
additions. When the `toolsearch` tool is disabled for an agent via its
`tools` map while `deferredTools` is non-empty, the deferral config SHALL be
ignored entirely (fail-open: all tools load normally) and a warning logged
once — deferral without a discovery path would strand tools.

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

#### Scenario: Disabling toolsearch fails open

- **WHEN** an agent disables `toolsearch` via `tools` while declaring a non-empty `deferredTools`
- **THEN** no tool is deferred, all enabled tools load with full schemas, and a warning is logged

### Requirement: toolsearch tool discovers and activates deferred tools

The system SHALL register a `toolsearch` tool in the agent's toolset
whenever the agent has at least one deferred tool (and `toolsearch` is not
disabled via `tools`), regardless of the resolved model — the toolset is
frozen per agent instance, and a mid-session model switch must not strand
deferred tools. Whether `toolsearch` is serialized into a given request is a
per-request provider decision: fallback-path requests include it, native-path
requests omit it in favor of the server-side tool search tool. The system
MUST NOT register `toolsearch` when nothing is deferred.

`toolsearch` SHALL support exact name queries, `select:name1,name2`
multi-select, and keyword search scored over tool names and descriptions
with a minimum-score floor. Matching tools SHALL be activated for the
calling session and their full contract (name, description, parameters,
required fields) returned in the tool result wrapped in `<system-reminder>`
tags. A query matching nothing SHALL return the list of still-deferred tool
names; a query matching an already-activated tool SHALL state that the tool
is already loaded and directly callable.

#### Scenario: Keyword search activates a deferred tool

- **WHEN** the model calls `toolsearch` with `query: "send slack message"` and a deferred `mcp_slack_send_message` tool exists
- **THEN** the result contains that tool's full contract and the tool is activated for subsequent requests in this session

#### Scenario: No-match reply teaches the model

- **WHEN** `toolsearch` matches no deferred tool
- **THEN** the result states no match and lists the available deferred tool names

#### Scenario: Already-activated tool is disambiguated

- **WHEN** `toolsearch` is called for a tool already activated in this session
- **THEN** the result states the tool is already loaded and can be called directly

#### Scenario: toolsearch absent without deferred tools

- **WHEN** an agent has no deferred tools
- **THEN** `toolsearch` is not registered in its toolset

#### Scenario: Model switch mid-session keeps deferred tools reachable

- **WHEN** a session starts on a native-path model and switches to a fallback-path model
- **THEN** subsequent requests include the registered `toolsearch` tool and still-deferred tools remain discoverable

### Requirement: Native activation path on models with SupportsToolSearch

Models SHALL carry a `SupportsToolSearch` capability flag, set only where
the serving endpoint executes Anthropic server-side tool search (Claude
models on anthropic, bedrock, vertexai; NOT kimi, openai-compatible, or
gemini). For such models the anthropic client SHALL: emit deferred tools
with their full schema plus `defer_loading: true` (the flag remains set
after activation); include the GA server tool-search tool
(`tool_search_tool_regex_20251119`) in the serialized tools array while
omitting the client-side `toolsearch`; and persist and replay
`server_tool_use` / `tool_search_tool_result` (including `tool_reference`
blocks) message parts across requests — replayed only for the matching
provider family, mirroring the reasoning-replay gating — so discovered
schemas remain expanded for the rest of the session.

Session activation state SHALL be updated at discovery time: when a
response stream carries `tool_search_tool_result` references, the named
tools are marked activated for that session in the same turn, and replayed
blocks on session resume backfill the same state.

#### Scenario: Single-turn discovery and call

- **WHEN** a Claude model on the native path searches for a deferred tool
- **THEN** the server tool search returns tool references in the same response stream
- **AND** the model can invoke the referenced tool within the same assistant turn

#### Scenario: Cache prefix survives activation

- **WHEN** a session on the native path activates a deferred tool between turn N and N+1
- **THEN** the serialized tools section and system prompt bytes that form the cache prefix are unchanged between the two turns

#### Scenario: Discovery marks session activation immediately

- **WHEN** a native-path response stream contains `tool_search_tool_result` references
- **THEN** the referenced tools are activated for the session before the next request is built
- **AND** a subsequent switch to a fallback-path model serializes those tools' full schemas

#### Scenario: Kimi is excluded from the native path

- **WHEN** an agent with deferred tools resolves a kimi model (anthropic client, Moonshot compat endpoint)
- **THEN** the fallback path is used: no `defer_loading` flags, no server tool-search tool, client-side `toolsearch` serialized

### Requirement: Fallback activation path preserves the tool prefix

On models without `SupportsToolSearch`, providers SHALL omit non-activated
deferred tools from the request payload entirely. After `toolsearch`
activates a tool, subsequent requests SHALL include it **appended after**
the stable tool ordering (baseline insertion order, then externals sorted by
name), with multiple activated tools ordered by their session activation
sequence — never inserted mid-list — so previously serialized tool
positions are unchanged. Activation state SHALL be scoped per session
(sessions served by the same long-lived agent instance MUST NOT observe
each other's activations), survive compaction within a running process, and
carry across a mid-session provider or model switch. After a process
restart, native-path sessions recover activation from replayed
`tool_search_tool_result` blocks; fallback-path sessions MAY require
re-discovery (self-correcting via `toolsearch`).

#### Scenario: Two-turn activation on OpenAI-compatible models

- **WHEN** the model calls `toolsearch` for a deferred tool on the fallback path
- **THEN** the current request's payload did not contain the tool, the result carries its contract as text, and the next request's payload includes it appended after the previously sent tools

#### Scenario: Activation order is stable across multiple activations

- **WHEN** tool B is activated in turn 1 and tool A (earlier in toolset order) is activated in turn 3
- **THEN** requests after turn 3 serialize [stable prefix..., B, A] — B's position is unchanged from turn 2's payload

#### Scenario: Sessions are isolated

- **WHEN** session 1 on a long-lived agent instance activates a deferred tool and session 2 starts on the same agent
- **THEN** session 2's first request omits that tool's schema (still deferred for session 2)

#### Scenario: Compaction does not reset activation

- **WHEN** a session compacts after tools were activated
- **THEN** activated tools remain included in subsequent requests without re-searching

### Requirement: Cache breakpoint never lands on a deferred entry

For anthropic-family requests carrying deferred tools, the tools-section
`cache_control` breakpoint SHALL be attached to the last serialized tool
definition that participates in the rendered prefix — i.e. the last entry
without `defer_loading: true`; on the native path this is the appended
server tool-search tool. Deferred entries are stripped from the rendered
prefix by the API, so a breakpoint attached to one would silently vanish.

#### Scenario: Breakpoint placement with deferred tools present

- **WHEN** an anthropic-family request serializes deferred tools
- **THEN** exactly one tools-section cache breakpoint exists and it is on an entry without `defer_loading: true`

### Requirement: Token accounting reflects the deferred payload

Token counting used for auto-compaction SHALL mirror serialization: local
estimation counts only the tools that would be serialized for the session's
current request (activated + non-deferred + the search tool), not the full
schemas of non-activated deferred tools. The anthropic count_tokens request
SHALL remain valid when the native path is active: the server tool-search
entry and `defer_loading` flags are mapped into the count-tokens tool union
rather than dropped or sent as empty union members.

#### Scenario: Compaction not triggered by phantom tokens

- **WHEN** an agent defers many MCP tools on the fallback path
- **THEN** the token estimate excludes their unserialized schemas and auto-compaction does not fire earlier than an equivalent session without those tools

#### Scenario: count_tokens stays functional on the native path

- **WHEN** the native path serializes the server tool-search tool
- **THEN** count_tokens requests succeed (no malformed empty tool entries)

### Requirement: Deferred tools are announced without breaking cache

When an agent's effective `deferredTools` config is non-empty (regardless
of whether any currently known tool matches), the system prompt SHALL gain
a `<system-reminder>` block explaining the tag convention, listing deferred
**builtin** tool names when any exist, and stating that deferred MCP tools
are announced as they become available — computed from config at
prompt-build time so it is identical on every request. Deferred MCP tools
(which resolve asynchronously) SHALL be announced via a user-role delta
message listing newly available deferred names, injected only when that
session's announced-set changes — never on every turn — with the
announced-set scoped per session. Both blocks MUST be absent for agents
with no `deferredTools` config.

#### Scenario: Static announcement is cache-stable

- **WHEN** an agent defers builtin tools and runs multiple turns
- **THEN** the system prompt bytes, including the announcement block, are identical across turns

#### Scenario: MCP-only deferral still announces

- **WHEN** an agent's `deferredTools` contains only patterns matching MCP tools (no builtin matches)
- **THEN** the system prompt block is present (explainer + MCP sentence), and resolved MCP names arrive via delta messages

#### Scenario: MCP delta announced once per session

- **WHEN** deferred MCP tools finish resolving after a session started
- **THEN** exactly one delta message announces the new names in that session
- **AND** a different session on the same agent instance receives its own delta independently

### Requirement: Both activation paths are e2e-verified

The repository SHALL ship two e2e checks under `scripts/test/`. The
fallback-path check MUST be fully self-contained: it drives a session
against an in-process mock OpenAI-compatible server (the `cmd/*-e2e` driver
pattern) and asserts the wire-level contract — the first request omits the
deferred tool and includes `toolsearch`; after a `toolsearch` call the next
request includes the activated tool appended after the previously sent
tools; and a same-binary A/B comparison shows the tools payload of a
no-`deferredTools` run is identical to one produced with the deferral
machinery disabled. The native-path check runs a real session against an
Anthropic model with server-side tool search and MUST skip (not fail) when
no Anthropic credential is available in the environment; when it runs, it
asserts the request carried `defer_loading: true` plus the server
tool-search tool, and that the model successfully discovered and invoked a
deferred tool.

#### Scenario: Fallback e2e runs offline

- **WHEN** `make test-e2e` runs on a machine with no provider credentials
- **THEN** the fallback deferred-tools script executes against the local mock and passes
- **AND** the native-path script reports SKIP rather than failing

#### Scenario: No-config payload equivalence is falsifiable in one binary

- **WHEN** the fallback e2e driver serializes tools for an agent without `deferredTools`
- **THEN** the payload matches the driver's own feature-bypassed serialization of the same toolset, byte for byte

#### Scenario: Native e2e verifies live server-side tool search

- **WHEN** the native-path script runs with an Anthropic credential present
- **THEN** the session's requests include `defer_loading: true` on deferred tools and the server tool-search tool
- **AND** the model invokes a deferred tool that was discovered via tool search during the session
