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
