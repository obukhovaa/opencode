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

### D2. Per-turn decision in providers via wrapper with per-session state

`NewToolSet` wraps matching tools in an exported `tools.DeferredWrapper`;
providers and `toolsearch` recognize deferral by **type assertion** on the
wrapper rather than a new `BaseTool` method (implementation refinement: an
interface change would touch every concrete tool and mock for zero
semantic gain). The wrapper carries **per-session** activation state: a
map `sessionID → activation sequence number` (sequence from a per-toolset
atomic counter). Providers extract the sessionID from the request context
(the same `tools.GetContextValues` plumbing every tool already uses) and
ask the wrapper `ActivatedAt(sessionID) (seq int64, ok bool)`;
`toolsearch` and the native discovery path call `Activate(sessionID)`.

Per-session state is mandatory, not a nicety: primary agent Service
instances are constructed once per process and serve every session
(app.Agents / bridge dispatch), so a process-global bool would leak
activations and suppress announcements across sessions. The toolset slice
itself never mutates (respects the `sync.Once` architecture); the
providers' `convertTools` — which run fresh per request — make the
include/flag/skip decision. In-memory state survives compaction (process
alive); after a process restart, native sessions recover from replayed
blocks (D3) and fallback sessions re-discover via `toolsearch`
(self-correcting, one turn).

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
   BM25 is a config follow-up if regex proves weak) to the tools array and
   **omits the client-side `toolsearch` from the serialized payload** — the
   server tool owns discovery, giving single-turn discovery+invocation.
   Note the registration/serialization split: `toolsearch` is still
   REGISTERED in the (frozen) toolset whenever deferrals exist, so a
   mid-session switch to a fallback model can serialize it; only the
   per-request emission is path-dependent.
3. Places the tools-section `cache_control` breakpoint on the last entry
   without `defer_loading` — on this path that is the appended server
   tool-search tool. Deferred entries don't exist in the rendered prefix,
   so a breakpoint on one would silently vanish.
4. Persists `server_tool_use` / `tool_search_tool_result` (with
   `tool_reference` blocks) message parts and replays them on subsequent
   requests, mirroring the `thinking-block-replay` mechanism (including its
   provider-family replay gating, à la `shouldReplayReasoning`) — the API
   re-expands referenced schemas from the replayed blocks.
5. **Activates at discovery time**: when the response stream's accumulated
   message carries `tool_search_tool_result` references, the named wrappers
   are marked activated for the session in the same event-handling pass —
   before the next request is built. Replay-time scanning is only the
   backfill for resumed sessions after a restart. Without this, a model
   switch right after discovery would strand just-discovered tools.
6. Maps the server tool entry and `defer_loading` flags into the
   count_tokens tool union as well — the count-tokens path reuses
   convertTools output, and an unmapped union member would 400 every
   count_tokens call (see D7).

Alternative considered — client-side `toolsearch` + `tool_reference` result
blocks on Anthropic (the 2026-04 draft's design, pre-GA): rejected; the GA
server tool does the same with less code, no result-format branching, and
server-maintained search quality.

### D4. Fallback path (everything else)

`convertTools` in openai.go/gemini.go — and anthropic.go when the model
lacks `SupportsToolSearch` (Kimi) — skips deferred tools not activated for
the request's session. The client-side `toolsearch` is serialized on this
path; its result returns each match's full contract (name, description,
parameters, required) as `<system-reminder>`-wrapped text and calls
`Activate(sessionID)`. Next request: activated tools are **appended after
the stable prefix** (baseline order + sorted externals, then
activated-deferred ordered by their session activation sequence — the
wrapper's per-session seq from D2 exists precisely so this order is
recoverable from a frozen slice) so all previously sent tool bytes keep
their positions; one cache miss per activation is the accepted cost
(two-turn activation is inherent — these APIs reject calls to undeclared
tools). Search algorithm: exact name > `select:a,b` > `+term`
required-keyword > scored keyword match over name+description, with a
minimum-score floor; no-match responses list the available deferred names,
and a query hitting an already-activated tool answers "already loaded —
call it directly" so the model doesn't loop.

### D5. Announcements: static block for builtins, deltas for MCP

- System prompt gains a `<system-reminder>` block whenever the agent's
  `deferredTools` config is **non-empty** — not "when a known builtin
  matches": an MCP-only pattern set (`{"jira_*": true}`) matches nothing at
  prompt-build time (MCP resolves asynchronously) yet still needs the
  explainer. The block carries the tag-convention explainer, the deferred
  builtin names when any exist, and a sentence that deferred MCP tools are
  announced as they become available. Config-computable, byte-stable per
  request; coexists with `lean-prompts` budgets (absent by default,
  dynamic).
- Late-arriving deferred MCP names are announced via a user-role delta
  message (`<system-reminder>`-wrapped names) injected in the agent loop
  only when the session's announced-set changes — never per-turn. The
  announced-set is **per session** (map on the agent keyed by sessionID),
  for the same reason as D2's activation state: agent instances outlive
  sessions. After a process restart a resumed session may receive one
  duplicate delta — harmless, accepted over history-scanning complexity.
- On the native path the announcement matters too: `defer_loading` hides
  schemas from the model, and a names list turns blind keyword search into
  targeted `select`-style queries.

### D6. Hard exclusions and interactions

Never deferrable, regardless of config: `toolsearch` (the ladder can't be
pulled up), `struct_output` (terminal contract — flow wrap-up force-calls it
via `forced-tool-choice`, which cannot force an undeclared tool). The
`task`/`question`/`router_send` tools are deferrable but discouraged in docs
(they anchor core loops). Deferring a disabled tool is a no-op (enable check
runs first). If the agent disables `toolsearch` itself while declaring
deferrals, the deferral config is ignored wholesale (fail-open, one WARN):
honoring it on the fallback path would make the deferred tools permanently
unreachable, and a path-dependent honor-sometimes rule would make the same
config mean different things per model.

### D7. Token accounting mirrors serialization

Auto-compaction is driven by token counting, so counting must see the same
payload the provider serializes — otherwise deferring 200 MCP tools still
"costs" their full schemas in the estimator and compaction fires tens of
thousands of tokens early, negating the feature. Local estimation
(`localTokenEstimate`) counts only the tools convertTools would emit for
the session's current state; the anthropic count_tokens request maps the
server tool-search entry and `defer_loading` flags into
`MessageCountTokensToolUnionParam` (an unmapped entry becomes an all-nil
union member and 400s every call). Covered by unit tests on both paths.

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
- [Cross-session state leaks on long-lived agent instances] → activation
  and announced-set are keyed by sessionID by design (D2/D5), with a
  session-isolation unit test; the alternative (per-instance state) was
  reviewed and rejected — it breaks every session after the first in
  serve/bridge mode.
- [Fallback-path activation lost on process restart] → accepted:
  re-discovery via `toolsearch` costs one turn and is self-correcting;
  native sessions recover from replayed `tool_search_tool_result` blocks.

## Migration Plan

Pure opt-in: no config ⇒ no wrapper, no `toolsearch`, no prompt block,
byte-identical provider requests (guarded by unit tests). Rollout: enable on
one MCP-heavy agent, compare per-turn input tokens and cache-read rates via
Langfuse. Rollback = removing the config key. The old draft file gets a
superseded banner pointing here.

Verification is two-tier: the fallback path gets a fully offline e2e (an
in-process mock OpenAI-compatible server via the `cmd/compaction-e2e`
driver pattern) asserting the wire contract, while the native path gets a
live, credential-gated e2e against a real Anthropic model with server-side
tool search that SKIPs when no key is available — CI stays hermetic, and
the real integration is exercised wherever credentials exist.

## Open Questions

- BM25 vs regex server variant default — ship regex, revisit with usage data.
- Default deferral sets for builtin agents (Claude Code defers ~20 builtins)
  — follow-up once token savings are measured.
- `searchHint` on `ToolInfo` for better keyword matching — deferred until
  keyword search proves inadequate.
