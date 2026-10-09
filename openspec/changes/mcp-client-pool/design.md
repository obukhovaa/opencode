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

The registry is process-wide and already shares the tools/list cache across sessions, so the pool lives there too. The key is the server name for stdio (no headers reach a stdio server) and the server name plus a SHA-256 of the resolved request headers for HTTP/SSE — static headers, the `mcpauthctx` Authorization override and the `peerHeader` value.

Keying on headers rather than sharing one HTTP client and injecting headers per request (`transport.WithHTTPHeaderFunc`, Codex's approach) is deliberate: a server may bind identity to the session at `initialize`, and then a shared session would act under the first caller's credentials. Distinct identities therefore never share a session. The cost is one client per identity — one per flow-run token on a pool pod, one per bridge peer for a `peerHeader` server — bounded by the idle timeout. The hash keeps tokens out of the map key.

### Entry lifecycle

An entry is created by the first acquirer and connected (construct, `Start`, `Initialize`) on its own goroutine under a context derived from the registry's base context, never a caller's: a caller that gives up must not poison the client for concurrent and later callers (the same rule the tools/list cache follows). `Initialize` is bounded by `mcpInitTimeout`; acquirers wait on the entry's ready channel or their own context. A failed connect removes the entry, so the next acquirer retries.

Entries are reference counted. `release` records the last-use time; an entry marked retired leaves the map at once (new acquirers get a fresh client) and is closed when its last user releases it, so in-flight calls on a retired client finish normally.

### Retry and eviction

| outcome of a call | evict | retry once |
|---|---|---|
| server answered (including a tool-level error) | no | no |
| HTTP 404 → `ErrSessionTerminated` | yes | yes — the server rejected the session, nothing ran |
| stdio write failed (`EPIPE`, closed pipe) | yes | yes — the request never left the process |
| per-call timeout | yes | no — the request may have run |
| any other transport error | yes | no |

Retrying only when the request provably did not execute keeps side-effecting tools at-most-once.

### Closing

Closes run on their own goroutine, never on the tool-call path. Stdio servers are spawned through mcp-go's `WithCommandFunc` with `Setpgid` (POSIX), which gives the process handle mcp-go does not expose. `Close` closes stdin and waits in `cmd.Wait()`; if it has not returned after `mcpCloseGrace` (2 s) the process group gets SIGTERM, after another grace SIGKILL. Signals are sent only while `Close` is still blocked in `cmd.Wait()`, i.e. before the PID is reaped, so a reused PID is never signalled. Windows has no process groups; the server process alone is killed.

### Stderr

mcp-go connects stderr to a pipe and never reads it. A per-call process rarely filled the 64 KiB pipe buffer; a long-lived one will, and then blocks on its next log line. The pool drains it line by line to the debug log.

### Idle timeout and opt-out

A janitor goroutine closes entries with no users that have been idle longer than their server's timeout (`clientIdleTimeoutSeconds`, default 600 s). A negative value disables reuse for that server: every call gets a fresh, unpooled client that is closed in the background after the call — today's behaviour minus the close wait — for servers that misbehave when a process outlives one request.

### Shutdown

`MCPRegistry.Shutdown(ctx)` marks the pool closed (acquire fails fast), retires every entry, and waits — bounded by ctx — for the closes to finish, so `app.Shutdown` / `ForceShutdown` do not leave stdio servers behind. Cancelling the registry's base context has the same effect without the wait.

## Testing

- Pool unit tests with an injected client factory (fake `MCPClient`): reuse, concurrent first use connects once, a caller giving up does not poison the entry, handshake bound and its message, retire-while-in-use, idle eviction, opt-out, shutdown.
- End-to-end against an in-process streamable-HTTP server: a second call makes one HTTP request and no `initialize`; a 404 on `tools/call` re-initializes and retries once; distinct Authorization / peer values get distinct sessions.
- Stdio against a helper process (the test binary re-executed as a tiny MCP server): one spawn across many calls; a server that ignores stdin EOF is killed within the grace period; a server that died between calls is replaced transparently.
- Re-run `tmp/mcpbench` (local only) before/after.
