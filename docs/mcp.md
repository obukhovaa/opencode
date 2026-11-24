# MCP Servers

OpenCode extends its tool set with [Model Context Protocol](https://modelcontextprotocol.io) servers declared under `mcpServers` in `.opencode.json`. To keep a large MCP fleet out of the context until it is needed, see [deferred tools](deferred-tools.md); to scope which agents get which MCP tools, see [tool permissions](tool-permissions.md).

## Configuration

```json
{
  "mcpServers": {
    "stdio-example": {
      "type": "stdio",
      "command": "path/to/server",
      "env": [],
      "args": []
    },
    "sse-example": {
      "type": "sse",
      "url": "https://example.org/mcp",
      "headers": { "Authorization": "Bearer token" }
    },
    "http-example": {
      "type": "http",
      "url": "https://example.com/mcp",
      "headers": { "Authorization": "Bearer token" }
    }
  }
}
```

A stdio server inherits OpenCode's environment minus the [tool environment deny list](tools.md#tool-environment); give it a denied value through its own `env` instead.

## Per-server tuning

- `callToolTimeoutSeconds` — override the per-tool-call timeout (default 5 minutes). Raise it for slow servers.
- `peerHeader` — name of an HTTP header that carries the chat-bridge peer id of the turn making the call (sse and http servers), e.g. `"peerHeader": "X-Peer-Id"`. The value comes from the bridge's authenticated inbound request, never from the model, so a server can scope its answers to the conversation the peer id names. It replaces any static header of that name and is omitted on calls not made from a bridge turn (cron jobs, a task's auto-resumed turn).
- `flowArgHeaders` — map of HTTP header name to the name of a top-level job arg (sse and http servers), e.g. `"flowArgHeaders": {"X-Tenant-Token": "tenant_token"}`. The arg's value comes from the args the orchestrator supplied for the job (`POST /flow` `args`, or `--flow-args`), never from the model, and is sent on tool calls made by the job's steps, its subagents, its auto-resumed turns, and the chat-bridge turns of the peers and sessions the job itself bound (never a daemon's turns, nor a turn of another job); tool discovery (`tools/list`) runs outside any job and carries none, as does a daemon turn. A string is sent as is and a JSON number as digits; an absent, empty, object/array/bool/null or control-character value omits the header (the warning logs the header name only). A static `headers` entry of the same name, in any letter case, is always dropped. Each job gets its own client. `Authorization` and the server's `peerHeader` are refused at load. Every arg named here is **redacted**: it is not persisted in `flow_states.args` (a resumed step takes it from the args the orchestrator supplies again), never enters Langfuse metadata or logs regardless of `telemetry.flowArgs`, and renders as `[redacted]` in `${args.<name>}` and `${args}` in step prompts, repo or Langfuse-hosted.
- `callToolMaxOutputBytes` — cap a single tool call's output kept in the model context (default `51200`, i.e. 50KB). Output beyond the cap is spilled to a temp file and replaced with a head+tail preview that points the agent at the file, which it can then `grep`/`read`/`sed`. This protects the context window from tools that return very large payloads (e.g. multi-MB CI build logs). Set a higher value to keep more inline, or a negative value to disable the cap entirely (unbounded — a single result can then overflow the context).
- `clientIdleTimeoutSeconds` — how long a connected client is kept for reuse after its last call (default `600`). See below; a negative value disables reuse for that server.

## Client reuse

opencode keeps one connected, initialized client per MCP server and reuses it for every tool call and for tool discovery. For a stdio server, that means the server process stays up between calls instead of being spawned, initialized and stopped for each call.

- **HTTP servers** get one client per distinct set of request headers. A per-flow Authorization override or a `peerHeader` value therefore gets its own session and never shares one with another identity.
- **One stdio process serves every session, agent, bridge peer and flow run**, because a stdio server receives nothing per call. A server that keeps state in memory (logins, browser contexts, caches) therefore shares it across all of them. Calls to it are multiplexed on one process, so a server that handles requests one at a time queues them, and the time spent queued counts against each call's timeout. For a server where that matters (multi-tenant `opencode serve`, a strictly serial server), set `clientIdleTimeoutSeconds: -1`.
- **SSE servers are never pooled.** mcp-go cannot detect a dropped event stream, so each call gets its own client.

A client idle longer than `clientIdleTimeoutSeconds` is closed, and all clients close on shutdown. A client is replaced when it turns out to be stale:

- **Retried once on a fresh client** — the request provably never ran: an HTTP 404 on a session the server dropped, or a write to a stdio server that has exited. Tool discovery (`tools/list`, which has no side effects) is also retried once after any transport failure.
- **Replaced without retrying** — other transport failures, call timeouts, a remote server rejecting the session with a non-standard JSON-RPC error, and a stdio call the agent abandons mid-call. The abandoned case stops the server's work on it, as closing the per-call client used to.

## Timeouts

Every wait on an MCP server is bounded, so a server that starts but never answers cannot park an agent turn:

| Wait | Budget | Tunable |
|---|---|---|
| Connect (`Start` + `initialize`), on first use and after a client is replaced | 30s | no |
| Tool call (`tools/call`) | 5 min | `callToolTimeoutSeconds` |
| Any tool call in the agent loop (`task`, `question` and `bash` exempt) | 20 min, and for an MCP tool at least its server's `callToolTimeoutSeconds` + 60s; past it the call is cancelled and abandoned and the model gets an error result | `toolCallTimeoutSeconds` (top level or per agent; 0 = off) |
| Client close | off the call path; a stdio server still running 2s after stdin closes gets SIGTERM, then SIGKILL 2s later (at shutdown: SIGTERM at once, SIGKILL within the shutdown budget); any other close is abandoned after 30s | no |
| Shared client-cache entry | 30s backstop | no |

The connect and cache budgets are deliberately not per-server tunable: `initialize` is one request/response with no work behind it, so a server that misses the budget is broken rather than slow. Only tool latency is genuinely server-specific. A blown budget surfaces as a normal tool error, so the agent can try another approach instead of hanging.

No tool result waits for a client to close. A stdio transport's `Close` blocks in `cmd.Wait()` honouring no context, and some servers take 10–20s to exit after stdin EOF. opencode starts each stdio server in a session of its own (so its process group can be signalled as a whole, wrappers such as `npx` included) and signals the group if it outlives the grace period, so closes are bounded and do not leak processes. Having no controlling terminal, a server that tries to prompt on the terminal (ssh, git credentials) fails at once instead of hanging; Ctrl-C does not reach it directly, and opencode stops it on exit instead. On Windows only the server process itself can be stopped. Stdio servers' stderr goes to the debug log.

## TUI controls

Open `/mcp` to inspect servers and their tools. Use `r` or Enter to reconnect,
`e` to enable, `d` to disable, and `f` to toggle deferred discovery for the
selected session's agent. Settings are persisted to the active configuration
file and its toolset is refreshed without restarting. Changes require all agent
runs to be idle. Reconnecting retires pooled connections without cancelling
calls already using them. Disabling affects that agent's tool access; shared
connections remain available to other agents.

An agent using `allowTools` is converted to an equivalent `tools` map with a
`"*": false` default when its server policy is edited. Other allowed patterns
remain enabled. This keeps the configuration's tools/allowTools exclusivity.

Tool controls report “still loading” while initial discovery is pending, rather
than waiting behind startup. They also require active flows to pause or finish,
including the gaps between their individual steps.

### Prompt-cache behavior of live controls

An unchanged reconnect keeps the same tool contracts, descriptions and existing
per-session deferred discoveries. Refreshing one server does not rebuild other
servers' tools or unrelated dynamic descriptions such as `router_send`'s peer
snapshot. Newly discovered deferred tools append after the session's already
activated tools on providers using client-side tool search.

Enable/disable, switching eager/deferred delivery, or a server changing its tool
catalog can change the serialized tools prefix. Those are real configuration
changes and may invalidate cached tools and subsequent system/message prefixes;
the application does not advertise disabled schemas to conceal the change.
Disabled MCP calls are also denied at execution time, including stale cached
flow-step tool instances. Once the configuration settles, subsequent requests
again share a stable prefix. Native server-side discovery of an already-deferred
tool is different: it keeps `defer_loading` stable and does not require changing
the tool declarations. See [deferred tools](deferred-tools.md).

## Bounded read composition (opt-in)

`mcp_compose` executes a fixed sequence of MCP reads and returns selected JSON facts. It is disabled unless both conditions hold: the agent grants the literal `mcp_compose` name, and the root configuration supplies a nonempty exact endpoint list:

```json
{
  "mcpComposition": {
    "readTools": ["catalog_find", "catalog_get"]
  }
}
```

Add the composition tool and its permitted child tools to the agent's existing `allowTools`; retain its existing model settings. A wildcard alone does not enable composition. An explicit legacy `tools.mcp_compose: true` also qualifies. The endpoint list is an operator-reviewed read-only allowlist, not an automatic classifier: do not put write operations or general-purpose SQL, HTTP or code runners in it. An explicit `readOnlyHint: false` refuses a child; missing annotations still require operator review. The `steps[].tool` schema lists the configured candidate names as an enum, in stable sorted order. This list does not attest current availability and does not grant access. Tools outside it must be called directly. Each child must also be enabled for the agent. A configured deferred read is resolved internally without requiring prior schema discovery or changing its activation state or serialization order. Composition therefore does not load child schemas into the model context. The outer tool and each child retain their own permission checks. Hook approval can bypass an ask prompt, never an explicit MCP policy deny.

Example plan:

```json
{
  "steps": [
    {"id":"lookup","tool":"catalog_find","arguments":{"key":"example"},
     "select":{"fields":{"id":"/id"}}},
    {"id":"detail","tool":"catalog_get","arguments":{"id":null},
     "bindings":[{"target":"/id","step":"lookup","pointer":"/id"}],
     "select":{"fields":{"id":"/id","state":"/state"}}}
  ],
  "return": ["detail"]
}
```

Bindings copy values from earlier **projected** results into existing argument locations. `select.fields` maps output names to RFC6901 pointers. For arrays, use `{"rows":"/items","fields":{"id":"/id"},"limit":20}`; field pointers are relative to each row. Missing pointers fail; JSON null is a value. Use paths from the actual MCP response, which may differ from the upstream REST API. If the shape is unknown, inspect an ordinary tool response first. For example, GitLab MCP v2.1.48 strips the REST root `sha` field but retains `/diff_refs/head_sha`; selecting `/sha` fails even when the merge request exists. IDs retain exact numeric digits. Plans reject duplicate keys, extra fields, forward references, overlapping binding targets and noncanonical array indices. There is no script execution, automatic pagination, dynamic fanout, recursion or persistent intermediate state.

Limits are 8 calls, 32 KiB plan input, depth 64, 256 KiB accepted child JSON text, 1 MiB cumulative accepted text, 16 KiB projected values, and 120 seconds of active execution, excluding human permission waits. Parent deadlines and smaller applicable tool limits still win. IDs are at most 64 characters, tool names 256 bytes, field names 64 bytes and pointers 1024 bytes. Row limits are 1–100. A deliberately limited row selection reports `limited:true` and `complete:false`; other failures stop later calls and mark them skipped. The result retains requested completed projections and explicit per-step source/status, never a raw-output fallback.

A `projection_failed` error includes `error.selection`, identifying the failed selection source (`fields` or `rows`), caller-supplied field name and JSON pointer, a zero-based row index for a failing row, and a static reason (`path_not_found` or `expected_array`). Object projection reports all failed fields in field-name order; row projection reports all failed fields of the first failing row within the selected limit. Diagnostics contain no response keys or values. Correct the selectors using the ordinary response; do not keep retrying the same child arguments with guessed paths, since repeated-call protection still applies.

An `output_limit` error includes `error.limit` with `scope` (`child_response`,
`total_response` or `projected_values`), `maxBytes` and `actualBytes`. The 16 KiB
projected-value bound is cumulative across all steps, including intermediate
steps omitted from `return`. Select small fields and bounded rows instead of
entire changed-file arrays, diffs or long descriptions. For a root array use
`"rows":""` and row-relative fields such as `"path":"/new_path"`; `/0/new_path`
would incorrectly index inside each row. Split the plan when needed. Diagnostics
include byte counts, never unprojected content; exceeding a bound still fails the
plan.

Only complete, text-only, post-hook JSON is supported. MCP error results, media/resources, structured-only responses, capped results, post-hook blocks and invalid JSON fail explicitly. Composition never reopens spill files or substitutes raw structured content after redaction. Trusted hook context remains visible separately, including on failure. The 16 KiB projection limit does not cap hook context: the existing hook runner allows up to 1 MiB stdout per hook and can concatenate several contexts.

Child values stay outside conversation history, raw child SSE results and agent-authored telemetry payloads; normal permission prompts may show the actual proposed child arguments. Shared MCP connections cannot attribute stderr or connection diagnostics to an individual call. Clients for servers reachable by the configured composition endpoint names suppress raw connection/close causes and drain stderr without logging its payload for **all sharing calls**, until that client is replaced. Ambiguous configured server-name prefixes are conservatively restricted for diagnostics only; authorization still uses exact tool identities. Operator-written hooks retain their own trusted logging behavior.

If the builtin name conflicts with another tool, the conflicting declarations are omitted when composition is enabled. With composition disabled, an existing MCP tool named `mcp_compose` keeps ordinary tool behavior. Remove the literal grant or empty `readTools` to disable composition. No workspace or daemon is opted in by default.

These are retained-data limits, not a hard transport-memory guarantee: the MCP client decodes server responses before the composition layer checks them. The acceptance fixtures measure result and serialized request bytes, not live model cost or production answer quality.
