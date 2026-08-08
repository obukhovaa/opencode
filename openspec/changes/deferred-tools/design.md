# Design: deferred tools & toolsearch

## Context

Grounding facts (verified against code and current platform docs, 2026-08-08):

- **Toolset lifecycle**: `NewToolSet` streams tools into a channel
  (`internal/llm/agent/tools.go:59`); `resolveTools()` drains it once under
  `sync.Once` (`tools.go:276`) and the same immutable `[]BaseTool` slice is
  passed to `provider.StreamResponse` on every turn (`agent.go:758,1207`).
  Any per-turn include/skip decision must therefore live in the providers'
  `convertTools`, not in toolset mutation.
- **Ordering & caching**: `OrderTools` (`tools.go:297`) = baseline tools in
  insertion order + external (MCP) tools sorted by name. The anthropic client
  sets one ephemeral `cache_control` breakpoint on the **last tool**
  (`anthropic.go:338`), plus system-prompt and rolling message breakpoints.
  OpenAI/Gemini rely on implicit prefix caching (no explicit breakpoints).
- **MCP**: process-wide singleton registry, per-server goroutines, 30-min
  TTL cache (`mcp-tool.go:86,169,241`); tools named `<server>_<toolname>`
  (`mcp-tool.go:315`) — may contain uppercase. MCP tools arrive after the
  system prompt is built.
- **Platform (actualized from the 2026-04 draft)**: Anthropic tool search is
  **GA** — server-side `tool_search_tool_regex_20251119` and
  `tool_search_tool_bm25_20251119` (undated aliases resolve to latest);
  `defer_loading` is a standard property on all tool definitions; docs
  guarantee deferred tools are *stripped from the rendered tools section
  before the cache key is computed*, so discovery/activation preserves an
  existing cache entry. No beta header. `anthropic-sdk-go v1.37.0` (pinned)
  has `ToolParam.DeferLoading`, both search-tool variants, and the
  `ToolSearchToolResult`/`ToolReference` blocks in the **stable** API.
- **Kimi rides the anthropic client** against Moonshot's compat endpoint
  (`provider.go:377`); Anthropic *server-side* tools won't exist there.
  Bedrock/VertexAI share the anthropic client and do get server tool search.
- **Forced tool choice** is anthropic-family-only and single-name
  (`anthropic.go:394,458`) — not extended by this change.

## Goals / Non-Goals

**Goals:**

- Opt-in per agent; absent config ⇒ byte-identical requests to today.
- Deferred tools cost ~one name line until needed; activation is single-turn
  on capable models and never invalidates the session's cache prefix there.
- Uniform UX across providers: same config, same `toolsearch` mental model;
  only latency and cache economics differ per path.
- Honest capability gating: native path only where the *serving endpoint*
  actually executes server-side tool search.

**Non-Goals:**

- No default deferral set for builtin agents in v1 (data first, defaults later).
- No semantic/embedding search — exact/`select:`/keyword matching only.
- No forcing of `toolsearch` via tool_choice; no OpenAI/Gemini tool_choice work.
- No server-side MCP connector (`mcp_toolset`) adoption — our MCP stays local.
- No TUI treatment changes: `toolsearch` renders like any other tool call.

## Decisions

### D1. Config: `deferredTools` map, same semantics as `tools`, case-insensitive

`DeferredTools map[string]bool` on `config.Agent` (json) and `AgentInfo`
(yaml — markdown frontmatter parses for free). A tool is deferred iff it is
enabled AND matches `deferredTools` truthy (exact > wildcard, most-specific
wins) AND is not excluded (`toolsearch`, `struct_output`). Matching is
**case-insensitive**: viper case-folds map keys on `.opencode.json` load and
MCP names like `mcp_Slack_send` contain uppercase — case-sensitive matching
would silently break for JSON-configured agents while working for markdown
ones. Per CLAUDE.md this map ships with a viper round-trip unit test.
Alternative considered — reusing the `tools` map with a `"defer"` string
value: rejected, breaks the existing boolean type and every consumer.

### D2. Per-turn decision in providers via `BaseTool.IsDeferred()` + wrapper

`BaseTool` gains `IsDeferred() bool` (concrete tools return false).
`NewToolSet` wraps matching tools in `deferredWrapper{inner, active atomic.Bool}`
where `IsDeferred() = !active.Load()`; `toolsearch` calls `Activate()`. The
toolset slice never mutates (respects the `sync.Once` architecture); the
providers' `convertTools` — which already run per request — make the
include/flag/skip decision. Activation state lives on the wrapper, so it
survives compaction and provider switches, and each subagent instance gets
independent state.

### D3. Native path (model capability `SupportsToolSearch`)

New bool on `models.Model`, set for Claude models served by anthropic,
bedrock, and vertexai; **not** set for kimi (compat endpoint can't run
Anthropic server tools), openai-compatible, or gemini families. When the
resolved model supports it and the agent has deferred tools, the anthropic
client:

1. Emits every deferred tool with **full schema + `DeferLoading: true`**
   (flag stays set after activation — the API strips deferred tools from the
   cache key, so a stable flag means a stable prefix).
2. Appends the server tool `tool_search_tool_regex_20251119` (regex variant;
   BM25 is a config follow-up if regex proves weak) to the tools array.
   Our client-side `toolsearch` is NOT sent on this path — the server tool
   owns discovery, giving single-turn discovery+invocation.
3. Moves the last-tool `cache_control` breakpoint to the last
   **non-deferred** tool: deferred tools don't exist in the rendered prefix,
   so a breakpoint on one would silently vanish.
4. Persists `server_tool_use` / `tool_search_tool_result` (with
   `tool_reference` blocks) message parts and replays them on subsequent
   requests, mirroring the `thinking-block-replay` mechanism — the API
   re-expands referenced schemas from the replayed blocks.

Alternative considered — client-side `toolsearch` + `tool_reference` result
blocks on Anthropic (the 2026-04 draft's design, pre-GA): rejected; the GA
server tool does the same with less code, no result-format branching, and
server-maintained search quality. The wrapper still tracks activation on
this path (from replayed `tool_reference` blocks) so provider switches
mid-session keep activated tools available (D5).

### D4. Fallback path (everything else)

`convertTools` in openai.go/gemini.go — and anthropic.go when the model
lacks `SupportsToolSearch` (Kimi) — skips tools where `IsDeferred()` is
true. Our client-side `toolsearch` tool is registered; its result returns
each match's full contract (name, description, parameters, required) as
`<system-reminder>`-wrapped text and activates the wrapper. Next request:
activated tools are **appended after the stable prefix** (baseline order +
sorted externals, then activated-deferred in activation order) so all
previously sent tool bytes keep their positions; one cache miss per
activation is the accepted cost (two-turn activation is inherent — these
APIs reject calls to undeclared tools). Search algorithm: exact name >
`select:a,b` > `+term` required-keyword > scored keyword match over
name+description, with a minimum-score floor; no-match responses list the
available deferred names.

### D5. Announcements: static block for builtins, deltas for MCP

- System prompt gains (only when the agent defers anything): the
  `<system-reminder>` tag-convention explainer + a block listing deferred
  **builtin** tool names — computable from config at prompt-build time,
  cache-stable. Coexists with `lean-prompts`: absent by default, dynamic,
  outside base-prompt budgets.
- MCP tools resolve asynchronously, so late-arriving deferred MCP names are
  announced via a user-role delta message (`<system-reminder>`-wrapped
  names) injected in the agent loop only when the announced-set changes —
  never per-turn. Tracked in an announced-set on the agent instance.
- On the native path the announcement matters too: `defer_loading` hides
  schemas from the model, and a names list turns blind keyword search into
  targeted `select`-style queries.

### D6. Hard exclusions and interactions

Never deferrable, regardless of config: `toolsearch` (the ladder can't be
pulled up), `struct_output` (terminal contract — flow wrap-up force-calls it
via `forced-tool-choice`, which cannot force an undeclared tool). The
`task`/`question`/`router_send` tools are deferrable but discouraged in docs
(they anchor core loops). Deferring a disabled tool is a no-op (enable check
runs first).

## Risks / Trade-offs

- [Moonshot/other compat endpoints silently ignore `defer_loading` if
  mis-gated] → gating is per-model (`SupportsToolSearch`), not per-client;
  Kimi defaults to fallback. If a gateway (LiteLLM) fronts real Anthropic,
  the flag passes through untouched — worst case a 400 names the unknown
  field and the fix is config-side model choice.
- [Fallback cache miss on every activation] → inherent to two-turn
  emulation; bounded (once per tool per session), append-at-end keeps the
  miss to the suffix. Documented so users weigh deferral against activation
  frequency.
- [Model calls a deferred tool without searching (fallback: API error)] →
  the announcement block instructs search-first; the API's "unknown tool"
  error is self-correcting; modern models handle this loop reliably.
- [Replay of `tool_search_tool_result` blocks adds a new persisted part
  type] → follows the proven thinking-block-replay pattern; round-trip
  covered by unit tests against recorded fixtures.
- [Viper key case-folding vs uppercase MCP names] → case-insensitive
  matching by design + round-trip test (D1); docs state patterns match
  case-insensitively.
- [Search quality on hundreds of MCP tools with regex-only matching] →
  names are namespaced (`server_tool`), which regex/keyword handles well;
  BM25 variant and `searchHint` metadata are deliberate follow-ups.

## Migration Plan

Pure opt-in: no config ⇒ no wrapper, no `toolsearch`, no prompt block,
byte-identical provider requests (guarded by unit tests). Rollout: enable on
one MCP-heavy agent, compare per-turn input tokens and cache-read rates via
Langfuse. Rollback = removing the config key. The old draft file gets a
superseded banner pointing here.

## Open Questions

- BM25 vs regex server variant default — ship regex, revisit with usage data.
- Default deferral sets for builtin agents (Claude Code defers ~20 builtins)
  — follow-up once token savings are measured.
- `searchHint` on `ToolInfo` for better keyword matching — deferred until
  keyword search proves inadequate.
