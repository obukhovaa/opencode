## 1. Pool

- [x] 1.1 `mcpClientPool` in `internal/llm/agent/mcp_pool.go`: keying (stdio by name, HTTP/SSE by name + header hash), refcounted entries, connect on a registry-owned goroutine with the handshake bounded by `mcpInitTimeout`
- [x] 1.2 Retire/evict semantics: retired entries leave the map at once and close when their last user releases
- [x] 1.3 Idle janitor and `clientIdleTimeoutSeconds` (default 600, negative = no reuse)
- [x] 1.4 `MCPRegistry.Shutdown(ctx)`, called from `app.Shutdown` / `ForceShutdown`; base-context cancellation closes the pool too

## 2. Call path

- [x] 2.1 `mcpTool.Run` calls through the pool; `runTool` no longer handshakes
- [x] 2.2 Retry once on `ErrSessionTerminated` and on a failed stdio write; evict without retry on timeout / other transport errors
- [x] 2.3 Discovery (`getToolsAttempt`) acquires from the pool, so the first tool call reuses the discovery client

## 3. Processes and transports

- [x] 3.1 Stdio spawned via `WithCommandFunc` in its own process group; background close escalates SIGTERM → SIGKILL after a grace period
- [x] 3.2 Drain stdio stderr to the debug log
- [x] 3.3 SSE `Start` runs under the client's lifetime context

## 4. Config, docs

- [x] 4.1 `clientIdleTimeoutSeconds` on `MCPServer`, `cmd/schema`, `opencode-schema.json`, README MCP section

## 5. Tests

- [x] 5.1 Pool unit tests with an injected client factory
- [x] 5.2 Streamable-HTTP end-to-end: reuse, 404 retry, identity separation
- [x] 5.3 Stdio end-to-end against a helper-process MCP server: single spawn, kill of a server ignoring EOF, replacement after a crash
- [x] 5.4 Adapt the deadline tests to the pool (handshake bound at connect, close off the call path)
- [x] 5.5 Re-run `tmp/mcpbench` before/after

## 6. Review round (three parallel reviews)

- [x] 6.1 Discovery: classify against baseCtx so a tools/list timeout evicts; retry tools/list once on a fresh client
- [x] 6.2 HTTP: evict on a JSON-RPC error with a non-standard code (TS-SDK session rejection)
- [x] 6.3 SSE not pooled
- [x] 6.4 Evict a stdio client when its caller abandons a call mid-flight
- [x] 6.5 Enforce callTimeout while mcp-go's stdio write blocks
- [x] 6.6 Shutdown: SIGTERM at once, SIGKILL within the budget; abort in-flight connects; count every close under the lock
- [x] 6.7 Setsid instead of Setpgid; non-interactive runs cancel on SIGINT/SIGTERM
- [x] 6.8 Windows broken-pipe errnos; lastUsed stamped at connect; idle timeout updated on reuse
- [x] 6.9 Tests for each of the above; flaky timing removed; helper exits when orphaned
