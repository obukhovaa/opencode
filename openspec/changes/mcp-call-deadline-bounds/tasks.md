# Tasks: mcp-call-deadline-bounds

## 1. Bound the per-call MCP handshake

- [ ] 1.1 In `runTool` (`internal/llm/agent/mcp-tool.go`), wrap the `c.Initialize`
  call in `context.WithTimeout(ctx, mcpInitTimeout)`; call `cancelInit()` explicitly
  rather than deferring it, so the timer is not held across the up-to-5-minute
  `CallTool` that follows

- [ ] 1.2 Add the deadline-attribution branch on handshake failure, copying the
  `ctx.Err() == nil && initCtx.Err() == context.DeadlineExceeded` guard from the
  existing `CallTool` branch verbatim; return `tools.NewTextErrorResponse` with a
  message naming the tool, the elapsed budget, and the "try a different approach or
  skip this step" advice, and a `nil` Go error so the turn survives

- [ ] 1.3 Extend the doc comment on `mcpInitTimeout` to state that it now bounds the
  per-call handshake in addition to the registry fetch path, and why it is not
  per-server overridable (a handshake has no work behind it; see design.md)

## 2. Bound the shared client-cache wait

- [ ] 2.1 Replace the bare `<-entry.done` receive in `getToolsAttempt`
  (`internal/llm/agent/mcp-tool.go`) with a `select` over `entry.done`,
  `r.discoveryCtx().Done()`, and a `time.After(mcpInitTimeout)` backstop

- [ ] 2.2 On backstop expiry: `logging.Warn` naming the server, return the
  tools accumulated so far, and do NOT delete the shared cache entry — the fetcher
  keeps ownership, mirroring the existing `entry.err != nil` path. On
  `discoveryCtx` cancellation return without logging a warning

- [ ] 2.3 Replace the now-inaccurate comment ("Bounded: the fetcher closes entry.done
  within mcpInitTimeout") with one describing the enforced bound and noting that the
  waiter backstop must stay >= the fetcher's own `fetchCtx` budget

## 3. Close every client on every path

- [ ] 3.1 Audit each `return` between `b.mcpReg.StartClient(...)` and normal
  completion in `mcpTool.Run` plus every early return in `runTool`; ensure each closes
  the client so no MCP server child process outlives its tool call

- [ ] 3.2 Confirm the `defer c.Close()` in `mcpTool.Run` is now actually reachable for
  the wedged-server case (it was not before: `runTool` never returned)

## 4. Drain progress log

- [ ] 4.1 Add a 60s ticker inside the wait in `drainSessionTasks`
  (`internal/llm/agent/agent.go`) logging still-pending `task_id`, `Kind` and age;
  emit nothing when the drain returns without waiting

- [ ] 4.2 Leave the wait condition untouched — no deadline, no early return. Add a
  comment stating that the ticker is observability only, per the `background-tasks`
  requirement that the drain impose no timeout of its own

## 5. Tests

- [ ] 5.1 Extend `fakeMCPClient` in `internal/llm/agent/mcp-tool_test.go` with
  configurable per-method blocking (a `blockInitialize` / `blockCallTool` channel or
  duration), preserving its current zero-value behavior so existing tests are
  unaffected

- [ ] 5.2 `TestRunTool_HandshakeTimeout`: a client that never returns from
  `Initialize` produces a non-error tool response naming the tool and the budget,
  within a bound comfortably under `mcpInitTimeout` (use a short injected timeout or a
  package-level variable rather than sleeping 30s in CI)

- [ ] 5.3 `TestRunTool_HandshakeTimeoutAttribution`: an already-cancelled parent ctx
  yields the upstream error, not the handshake-budget message

- [ ] 5.4 `TestRunTool_SlowToolNotCurtailedByHandshakeBound`: prompt handshake plus a
  tool call slower than `mcpInitTimeout` but within its tool-call budget still succeeds

- [ ] 5.5 `TestMCPRegistry_WaiterBoundedByWedgedFetcher`: with a fetcher that never
  closes `entry.done`, a concurrent waiter returns within the backstop, contributes no
  tools for that server, and leaves the cache entry present; extend the existing
  `TestMCPRegistry_ConcurrentLoadSharesSingleFetch` / `..._ShutdownBoundsFetch`
  neighbours rather than duplicating their scaffolding

- [ ] 5.6 `TestRunTool_ClosesClientOnEveryPath`: assert `Close()` was called for the
  handshake-timeout and argument-parse-failure paths

- [ ] 5.7 In `internal/llm/agent/drain_test.go`: assert the progress log fires while a
  task is pending, does not fire for an empty drain, and that a never-terminating task
  with a deadline-free ctx leaves the drain still waiting (no timeout introduced)

- [ ] 5.8 `go test ./internal/llm/agent/... ./internal/task/...` green; `go build ./...`
  clean

## 6. Docs

- [ ] 6.1 Document the four MCP wait points and their bounds (the design.md table) in
  the MCP docs, so the next person does not have to re-derive which waits are bounded

- [ ] 6.2 `docs/background-tasks.md`: note the drain progress log and restate that it
  is not a timeout

## 7. Verification against the original incident

- [ ] 7.1 Reproduce the production signature locally with a stub stdio MCP server that
  completes `Start` but never answers `Initialize`; confirm pre-fix the tool part
  persists as `status: running` with `started: null` and the call never returns, and
  post-fix the call fails within the budget with the tool part reaching a terminal
  state

- [ ] 7.2 Confirm no MCP server child process and no zombie remains after the
  reproduction run

- [ ] 7.3 Note on GENAI-270 that the drain-timeout fix originally proposed there is
  deliberately NOT part of this change, and why (contradicts the standing
  `background-tasks` requirement; the real gap is the unset deadline source on the
  Piano deployment side)
