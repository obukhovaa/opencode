## Why

Every MCP tool call builds a new client: `mcpTool.Run` → `StartClient` → `Initialize` → `CallTool` → `closeMCPClient` (inherited from upstream opencode). A stdio server is spawned, handshaken and stopped per call, an HTTP server gets `initialize` + `notifications/initialized` + a session `DELETE` per call, and the tool result waits for the close: mcp-go's `Stdio.Close` is `cmd.Wait()` with no kill, and `closeMCPClient` waits up to 30 s for it.

Measured against the servers in a real `~/.opencode.json` and confirmed by 60 days of Langfuse tool spans (GENAI-433): obsidian calls take p50 10.6 s and gitlab p50 12.3 s — almost all of it waiting for a server process to exit after the result is already in — atlassian pays ~3 s of server boot per call, and HTTP servers pay 170–530 ms of extra round trips. Codex CLI, Claude Code and sst/opencode all keep one client per server for the session instead.

## What Changes

- **Client pool.** The MCP registry keeps one connected, initialized client per server and connection identity and reuses it across tool calls and tool discovery. Stdio clients are keyed by server name; HTTP/SSE clients by server name plus the resolved request headers, so a per-flow Authorization override or a bridge `peerHeader` value gets its own session and never shares one with another identity. A client idle longer than its server's idle timeout (default 10 minutes) is closed; all clients close on shutdown.
- **Self-healing.** A call whose request provably never reached the server — an HTTP 404 on a terminated session, a write to a stdio server that already exited — evicts the client and is retried once on a fresh one. A transport failure or a call timeout evicts the client without retrying (the request may have run).
- **Closing never delays a result.** Clients are closed off the tool-call path. Stdio servers are started in their own process group; a server that has not exited a grace period after stdin is closed gets SIGTERM, then SIGKILL, so closes are bounded and no longer leak processes.
- **Stdio stderr is drained** (to the debug log): a long-lived server that logs to stderr would otherwise block once the pipe buffer fills.
- **SSE keeps its event stream.** The stream now lives as long as the client; it used to be tied to a 20 s start context that was cancelled as soon as `StartClient` returned.
- **New MCP server field `clientIdleTimeoutSeconds`**: idle time before a reused client is closed. `0`/unset = 600; negative = no reuse (a fresh client per call, as before, but closed in the background).

## Capabilities

### New Capabilities

- `mcp-client-pool`: client reuse, keying, eviction, retries, idle timeout and shutdown.

### Modified Capabilities

- `mcp-call-deadline-bounds`: the handshake bound applies when a client is connected (first use or after eviction) rather than on every call; closing is moved off the call path and escalates to signals for stdio servers.

## Impact

- `internal/llm/agent/mcp-tool.go` (+ new `mcp_pool.go`, `mcp_proc_{posix,windows}.go`), `internal/app/app.go` (shutdown), `internal/config/config.go`, `cmd/schema/main.go`, `opencode-schema.json`, README.
- Behaviour: a stdio server process now stays up between calls (up to the idle timeout), so a server that keeps state across requests will see it kept; `clientIdleTimeoutSeconds: -1` restores per-call processes for such a server.
- No change to tool names, discovery caching (30 min), permissions, per-call timeouts or output caps.
