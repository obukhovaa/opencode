# Deferred tools & toolsearch: opt-in on-demand tool loading

## Why

Every enabled tool ships its full schema (name, description, JSON parameters)
to the provider on every request. Builtins are now lean (see `lean-prompts`),
but MCP is unbounded: the toolset channel streams one tool per MCP server
entry, and real configs run to hundreds of tools — each with schemas we don't
control. A session that never touches Jira still pays for every Jira tool on
every turn, and the payload dilutes tool-selection accuracy. There is no
middle ground today between "enabled" (full schema, every turn) and
"disabled" (unusable).

Since the original draft (`spec/20260405T120000-deferred-tools-and-toolsearch.md`,
2026-04-05), the platform caught up: Anthropic's tool search tool is **GA**
(server-side `tool_search_tool_regex_20251119` / `tool_search_tool_bm25_20251119`),
`defer_loading` is a standard GA property on every tool definition, deferred
tools are documented as **stripped before the prompt-cache key is computed**
(activation cannot invalidate an existing cache entry), and our pinned SDK
(`anthropic-sdk-go v1.37.0`) ships every needed type in the stable API. The
mechanism Claude Code uses is now buildable here with no beta headers and no
SDK bump.

## What Changes

- **Opt-in per agent**: new `deferredTools` map (same wildcard semantics as
  `tools`) on `.opencode.json` agents and markdown-agent frontmatter. Absent
  config ⇒ zero behavior change for every existing agent.
- **`toolsearch` tool**: auto-registered only when the agent has ≥1 deferred
  tool. Searches deferred tools by exact name, `select:a,b` multi-select, or
  keyword; activates matches. Never deferrable itself; `struct_output` is
  never deferrable.
- **Two activation paths, gated per model** (new `SupportsToolSearch` model
  capability, following the `SupportsTaskBudget` pattern):
  - **Native** (Claude models on anthropic/bedrock/vertexai): deferred tools
    sent with full schema + `defer_loading: true`, the GA server-side tool
    search tool included in the tools array; single-turn discovery+call;
    prompt-cache prefix provably unaffected. `tool_search_tool_result` /
    `tool_reference` blocks are persisted and replayed like thinking blocks.
  - **Fallback** (OpenAI-compatible, Gemini, Kimi — which rides the anthropic
    client against Moonshot's compat endpoint where Anthropic server tools
    don't exist): deferred tools omitted from the payload; our client-side
    `toolsearch` returns schemas as text and flips a wrapper activation flag;
    the tool appears in the next request, **appended after the stable tool
    prefix**; two-turn activation, one accepted cache miss per activation.
- **Announcements**: deferred builtin tool names listed in a cache-stable
  `<system-reminder>` block in the system prompt; late-arriving deferred MCP
  tools announced via delta user messages only when the pool changes.
- **Cache correctness**: the Anthropic last-tool `cache_control` breakpoint
  moves to the last **non-deferred** tool (deferred tools are stripped from
  the prefix, so a breakpoint on one would be lost).
- Old draft `spec/20260405T120000-deferred-tools-and-toolsearch.md` gets a
  "superseded by this change" banner; content is carried here, actualized.

## Capabilities

### New Capabilities

- `deferred-tools`: the deferral config surface, the `toolsearch` tool
  contract, native and fallback activation semantics, announcement blocks,
  cache-stability guarantees, and the never-defer exclusions.

### Modified Capabilities

None. `forced-tool-choice` is untouched (no forcing of `toolsearch` in v1);
`structured-output` is untouched (the never-defer rule lives in the new
capability); `kimi-provider` requirements are unchanged (Kimi simply doesn't
set the new model capability flag).

## Impact

**`github.com/obukhovaa/opencode`**

- `internal/config/config.go` (+`cmd/schema/main.go`, regenerated
  `opencode-schema.json`): `Agent.DeferredTools` — with a viper round-trip
  test (map keyed on user-supplied names; viper case-folds keys and MCP tool
  names can contain uppercase, so matching is defined case-insensitive).
- `internal/agent/registry.go`: `AgentInfo.DeferredTools` (yaml frontmatter
  parses automatically) + the two `maps.Copy` merge sites.
- `internal/permission/evaluate.go`: `IsToolDeferred` (wildcards via
  `MatchWildcard`, case-insensitive, hardcoded exclusions).
- `internal/llm/tools/tools.go`: `IsDeferred()` on `BaseTool`, `deferredWrapper`;
  `internal/llm/tools/toolsearch.go`: the new tool.
- `internal/llm/agent/tools.go`: deferred wrapping in `NewToolSet` (builtin +
  MCP + LSP paths), `toolsearch` auto-inclusion; `agent.go`: MCP delta
  announcements after `resolveTools`.
- `internal/llm/models/*`: `SupportsToolSearch` capability flag on Claude
  models (anthropic/bedrock/vertexai); false for kimi/openai/gemini families.
- `internal/llm/provider/anthropic.go`: `DeferLoading` in `convertTools`,
  server tool-search tool injection, breakpoint on last non-deferred tool,
  `tool_search_tool_result`/`tool_reference` block persistence + replay
  (mirrors `thinking-block-replay`); `openai.go`/`gemini.go`: skip
  non-activated deferred tools, append activated ones after the prefix.
- `internal/llm/prompt/prompt.go`: conditional `<system-reminder>` appendix
  (coexists with `lean-prompts` prompt-surface budgets — it is dynamic,
  config-gated, and absent by default).
- Docs: CLAUDE.md agent-fields table, `docs/` agent configuration section.
