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
