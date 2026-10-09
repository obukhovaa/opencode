# Design: mcp-client-pool

## Measured cost (GENAI-433)

| server | transport | per-call today (bench p50) | warm client (bench p50) | Langfuse p50, 60 days |
|---|---|---|---|---|
| gitlab | stdio | 22.5 s (close waits ~22 s) | 2 ms | 12.3 s (17 calls) |
| obsidian | stdio | 10.5 s (close waits ~10 s) | 3 ms | 10.6 s (60 calls) |
| atlassian | stdio | 1.2 s (boot) | 7 ms | 3.0 s (24 calls) |
| datadog | http | 0.78 s (initialize + DELETE) | 0.25 s | 0.9 s |

## Decisions

### One pool per registry, keyed by connection identity

The registry is process-wide and already shares the tools/list cache across sessions, so the pool lives there too. The key is the server name plus a SHA-256 of its launch configuration (type, command, args, env, URL — so an edited config gets a new client) and, for HTTP, of the resolved request headers — static headers, the `mcpauthctx` Authorization override and the `peerHeader` value.

A stdio server receives nothing per call, so one process serves every session, agent, bridge peer and flow run. Keying stdio by identity would spawn a process per peer or per flow run — defeating the pool exactly in `opencode serve` and on pool pods, where it matters most — for no isolation the server could observe. The cost is that in-process server state is shared and a serial server queues concurrent calls (queue time counts against each call's timeout); `clientIdleTimeoutSeconds: -1` restores per-call processes for such a server.

Keying on headers rather than sharing one HTTP client and injecting headers per request (`transport.WithHTTPHeaderFunc`, Codex's approach) is deliberate: a server may bind identity to the session at `initialize`, and then a shared session would act under the first caller's credentials. Distinct identities therefore never share a session. The cost is one client per identity — one per flow-run token on a pool pod, one per bridge peer for a `peerHeader` server — bounded by the idle timeout. The hash keeps tokens out of the map key.

### Entry lifecycle

An entry is created by the first acquirer and connected (construct, `Start`, `Initialize`) on its own goroutine under a context derived from the registry's base context, never a caller's: a caller that gives up must not poison the client for concurrent and later callers (the same rule the tools/list cache follows). `Initialize` is bounded by `mcpInitTimeout`; acquirers wait on the entry's ready channel or their own context. A failed connect removes the entry, so the next acquirer retries.

Entries are reference counted. `release` records the last-use time; an entry marked retired leaves the map at once (new acquirers get a fresh client) and is closed when its last user releases it, so in-flight calls on a retired client finish normally.

### SSE is not pooled

mcp-go's SSE transport ends its event stream silently on EOF — a proxy idle timeout or a server restart — and exposes neither a liveness signal nor a `Ping`. A pooled SSE client would be handed out with no stream: a POST the server rejects fails the call, and one it accepts (202) runs the tool while the response is lost, so it cannot be retried either. Each SSE call therefore gets its own client, whose stream now lives for the whole call (it used to be cancelled by `StartClient`'s 20 s start context as soon as it returned, which broke SSE outright). SSE is the transport the MCP spec deprecated in favour of streamable HTTP.

### Retry and eviction

| outcome of a call | evict | retry once |
|---|---|---|
| server answered (including a tool-level error) | no | no |
| HTTP 404 → `ErrSessionTerminated` | yes | yes — the server rejected the session, nothing ran |
| stdio write failed (`EPIPE`, closed pipe) | yes | yes — the request never left the process |
| per-call timeout (enforced even while mcp-go's stdio write blocks) | yes | no — the request may have run |
| any other transport error | yes | no |
| HTTP: JSON-RPC error with a non-standard code | yes | no |
| stdio: the caller abandoned the call mid-flight | yes | no |

Retrying only when the request provably did not execute keeps side-effecting tools at-most-once.

mcp-go returns a non-2xx response that carries a JSON-RPC body as an ordinary error answer, which is how TypeScript-SDK servers reject a session they no longer know (400, -32000). Its standard codes map to sentinel errors (method not found, invalid params, internal error — how mcp-go servers report a failing tool, request interrupted, resource not found); anything else on an HTTP client evicts it. mcp-go sends no `notifications/cancelled`, so an abandoned stdio call would keep the server busy and later calls would queue behind it; evicting the client stops the process once its other calls finish, as closing the per-call client used to.

Discovery uses the same rules, classified against the registry's context rather than the fetch's 30 s budget, so a `tools/list` that times out evicts the client; and since `tools/list` has no side effects, a fetch whose client was evicted is retried once on a fresh client while the budget lasts. Without that, a client that went stale between calls would freeze an empty toolset into the fetching agent.

### Closing

Closes run on their own goroutine, never on the tool-call path. Stdio servers are spawned through mcp-go's `WithCommandFunc` with `Setsid` (POSIX), which gives the process handle mcp-go does not expose and makes the server a process-group leader with no controlling terminal: a server that tries to prompt on `/dev/tty` fails at once instead of being stopped by SIGTTIN and hanging its call. `Close` closes stdin and waits in `cmd.Wait()`; if it has not returned after `mcpCloseGrace` (2 s) the process group gets SIGTERM, after another grace SIGKILL. A signal is sent only while `Close` is still blocked in `cmd.Wait()` (rechecked just before the kill), so it cannot reach a reused PID except in the few instructions between the reap and the kill(2). Windows has no process groups; the server process alone is killed, and a wrapper's children (`npx.cmd`) survive it.

Because the servers no longer share the terminal's process group, Ctrl-C does not reach them. Non-interactive runs therefore turn SIGINT/SIGTERM into cancelling the run, so `ForceShutdown` runs and stops them; a second signal kills as before.

### Stderr

mcp-go connects stderr to a pipe and never reads it. A per-call process rarely filled the 64 KiB pipe buffer; a long-lived one will, and then blocks on its next log line. The pool drains it line by line to the debug log.

### Idle timeout and opt-out

A janitor goroutine closes entries with no users that have been idle longer than their server's timeout (`clientIdleTimeoutSeconds`, default 600 s). A negative value disables reuse for that server: every call gets a fresh, unpooled client that is closed in the background after the call — today's behaviour minus the close wait — for servers that misbehave when a process outlives one request.

### Shutdown

`MCPRegistry.Shutdown(ctx)` marks the pool closed (acquire fails fast), aborts connects still in progress, retires every entry, and waits — bounded by ctx — for the closes to finish, so `app.Shutdown` (5 s) and `ForceShutdown` (1 s) do not leave stdio servers behind. At shutdown a stdio server gets SIGTERM at once rather than after the EOF grace, and SIGKILL within half the remaining budget: the process is exiting, and the 1 s ForceShutdown budget is shorter than the 2 s grace. Every close is counted in the same critical section that decides it, so the wait cannot miss one. Cancelling the registry's base context runs the same shutdown from the janitor.

## Testing

- Pool unit tests with an injected client factory (fake `MCPClient`): reuse, concurrent first use connects once, a caller giving up does not poison the entry, handshake bound and its message, retire-while-in-use, idle eviction, opt-out, shutdown.
- End-to-end against an in-process streamable-HTTP server: a second call makes one HTTP request and no `initialize`; a 404 on `tools/call` re-initializes and retries once; distinct Authorization / peer values get distinct sessions.
- Stdio against a helper process (the test binary re-executed as a tiny MCP server): one spawn across many calls; a server that ignores stdin EOF is killed within the grace period; a server that died between calls is replaced transparently.
- Re-run `tmp/mcpbench` (local only) before/after.
