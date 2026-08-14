# Deferred Tools & ToolSearch

Every tool an agent has enabled ships its full schema (name, description, JSON
parameters) to the model on **every** request. For a handful of builtins that
is cheap, but MCP fleets are unbounded — connect Jira, Slack, GitHub, and a few
others and you can spend thousands of tokens per turn on schemas the model may
never call. That payload also dilutes tool-selection accuracy and, on Anthropic,
sits in the cached prefix.

Deferred tools give a middle ground between "enabled" (full schema, every turn)
and "disabled" (unusable): a **deferred** tool keeps only its *name* in context
until the model discovers it via tool search, at which point its full schema is
loaded and it becomes callable.

## Enabling it

Deferral is opt-in per agent via the `deferredTools` map — same wildcard
semantics as the `tools` map. It works in `.opencode.json` and in markdown agent
frontmatter.

```json
{
  "agents": {
    "coder": {
      "deferredTools": {
        "jira_*": true,
        "mcp_slack_*": true,
        "sourcegraph": true
      }
    }
  }
}
```

A tool is deferred only when **all** of these hold:

- it is enabled (via `tools` / default) — you cannot defer a disabled tool;
- its name matches a truthy `deferredTools` pattern (exact match wins over
  wildcard, most-specific wildcard wins), matched **case-insensitively**;
- it is not a hard exclusion.

**Hard exclusions (never deferrable):** `toolsearch` (the discovery ladder
itself) and `struct_output` (the terminal structured-output tool, which the
flow runner force-calls and cannot force on an undeclared tool).

When an agent declares `deferredTools`, a `toolsearch` tool is auto-registered.
If you disable `toolsearch` while declaring deferrals, the deferral config is
**ignored entirely** (fail-open, with a one-shot warning) — deferring tools with
no way to discover them would strand them.

> Matching is case-insensitive by design: viper lowercases JSON map keys on
> load, while MCP tool names (`<server>_<toolName>`) can contain uppercase.
> A pattern like `{"mcp_Slack_*": true}` still matches `mcp_Slack_send_message`.

## How discovery works

The model sees a `<system-reminder>` block naming the deferred builtin tools and
explaining that their schemas must be loaded via tool search before use.
Deferred **MCP** tools resolve asynchronously, so they are announced through a
follow-up `<system-reminder>` message once they become available (per session,
only when the set changes).

Neither reminder names a specific tool-search tool — they say "the available
tool-search tool". Which one the model actually holds a schema for is decided
per request by the provider (native `tool_search_tool_regex_20251119` vs the
client-side `toolsearch`), while the MCP delta is persisted once and outlives a
mid-session model switch. Naming `toolsearch` there made native-path models
emit a `toolsearch` call carrying the *server* tool's `pattern` argument.

Two activation paths exist, chosen automatically per the resolved model's
`SupportsToolSearch` capability.

### Native path — Claude on Anthropic / VertexAI / Bedrock

Deferred tools are sent with their full schema plus `defer_loading: true`, and
Anthropic's GA server-side tool-search tool (`tool_search_tool_regex_20251119`)
is added to the request. The API strips `defer_loading` tools from the rendered
prompt **before computing the cache key**, so:

- the model does not see a deferred tool's schema until it searches;
- discovery happens **server-side in a single turn** — the model searches,
  receives tool references, and can call the discovered tool in the *same*
  response;
- **activation never invalidates the prompt-cache prefix** (the deferred entry
  was never in the prefix to begin with). The tools-section cache breakpoint is
  placed on the last non-deferred entry so it can't land on a stripped tool.

The `server_tool_use` / `tool_search_tool_result` blocks are persisted as
message parts and replayed on later requests (like thinking-block replay), so
discovered tools stay loaded for the rest of the session.

### Fallback path — OpenAI-compatible, Gemini, Kimi

These endpoints don't run Anthropic server-side tool search (Kimi rides the
Anthropic client but against Moonshot's compat endpoint, so it uses this path
too). Non-activated deferred tools are **omitted** from the request entirely and
a client-side `toolsearch` tool is sent instead. When the model calls it, the
matched tools' full contracts are returned as text and marked active; on the
**next** request they are appended after the stable tool prefix. This is
two-turn activation (search, then call) and costs one cache miss per activation,
kept to the suffix by appending rather than inserting.

## The `toolsearch` tool

Query forms:

| Query | Meaning |
|-------|---------|
| `read` | exact tool name |
| `select:jira_add_comment,mcp_slack_send_message` | load these tools by name |
| `+slack send message` | require `slack`, rank by the rest |
| `send a message to slack` | keyword search over names + descriptions |

The result contains each matched tool's full contract (description, parameters,
required fields). A query that matches nothing returns the list of still-deferred
tool names; a query for an already-loaded tool says so and tells the model to
call it directly.

The declared argument is `query`. An undeclared `pattern` argument is also
accepted (`query` wins when both are set): models that know the server-side
`tool_search_tool_regex` schema sometimes aim a call at this tool's name while
filling in that tool's argument, and honouring it beats spending a turn on
`query is required`. The client tool stays in the toolset on the native path —
it is filtered out of the request, not out of dispatch — so such a call lands
here rather than failing as `Tool not found`.

`pattern` is a **regex** while matching here is literal, so it is normalized
before searching: anchors, wildcards, quantifiers, character classes, inline
flags and escape classes are replaced by term separators, and `|` becomes
separate keyword terms (the matcher already ORs and ranks them). So `^jira`,
`jira_.*`, `(?i)jira` and `jira[a-z_]+comment` all reach `jira_add_comment`,
and `a|b` reaches both. Terms left with no letters or digits are dropped:
matching is substring-based, so the `_` that `[a-z]+_[a-z]+` reduces to would
score against every tool name and activate the whole deferred set. A pattern
that reduces to no usable term (`.*`, `_`) takes the no-match branch, which
lists the still-deferred names — it is never retried raw. `query` is never
normalized — its `+term` syntax means "require this term", not a quantifier.

Keyword search loads at most 12 tools per call, best-ranked first, and says how
many further matches it withheld. Terms are ORed over substrings, so one broad
query (`mcp gitlab`, or a regex whose namespace prefix survives normalization)
can otherwise score most of a large MCP fleet and dump every schema it was
deferring. Exact-name and `select:` hits are exempt — the model named those.

## Scope & lifecycle

- **Per session.** A long-lived agent instance serves many sessions; activation
  and MCP-announcement state are keyed by session, so one session's activations
  never leak into another.
- **Survives compaction** within a running process. After a process restart,
  native sessions restore activation from the replayed tool-search blocks;
  fallback sessions re-discover via `toolsearch` (one self-correcting turn).
- **Model switches mid-session** are safe: `toolsearch` stays registered
  regardless of model, so switching from a native to a fallback model never
  strands deferred tools.

## Token accounting

Auto-compaction is driven by token counting, so counting mirrors serialization:
only the tools that would actually be sent for the session's current state are
counted, not the withheld schemas of non-activated deferred tools. Deferring a
large MCP fleet therefore lowers the per-turn token estimate instead of firing
compaction early.

## When to use it

Reach for `deferredTools` when an agent has a large, mostly-idle tool surface —
typically several MCP servers. The savings scale with the deferred schema bytes
times the number of turns. For a handful of always-used tools the extra
discovery turn (on the fallback path) isn't worth it; leave them loaded.

Start with the noisiest MCP namespaces (`{"jira_*": true, "slack_*": true}`) and
measure per-turn input tokens. There is no default deferral set — every agent
keeps its current behavior until you opt in.

## Known limitations

- **Bedrock / Vertex without server tool search.** The native path requires the
  serving endpoint to execute Anthropic's server-side tool search. This is
  verified working through the LiteLLM proxy for both `bedrock.*` and
  `vertexai.*` Claude models. A *direct* AWS Bedrock / Google Vertex endpoint
  that does not support the GA tool-search tool (or requires the
  `tool-search-tool` beta) may reject a deferred-tools request; set that beta
  via `providers.<name>.headers`, or don't declare `deferredTools` for such
  agents.
- **Cross-path model switch mid-session.** Activation *state* carries across a
  model switch, but switching between a native (server tool search) model and a
  fallback model within one session can leave provider-specific tool-search
  history blocks that the new model's request doesn't declare. Native→fallback
  is handled (those blocks are dropped on replay); the reverse, after the model
  has called the client `toolsearch`, may require a compaction to clear. Prefer
  keeping an agent on one path for a session.
- **Fallback restart re-discovery.** Fallback-path activations live only in
  memory. After a process restart a resumed fallback session re-discovers tools
  via `toolsearch` (one self-correcting turn); native sessions restore from the
  replayed tool-search blocks and need no re-discovery.
