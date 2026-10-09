# mcp-call-deadline-bounds Specification

## Purpose
TBD - created by archiving change mcp-call-deadline-bounds. Update Purpose after archive.
## Requirements
### Requirement: The shared MCP client-cache wait is bounded

A caller waiting on an in-flight MCP client-cache fetch SHALL NOT wait unboundedly. The
wait SHALL terminate on the earliest of: the fetch completing, the registry's own
context being cancelled, or a backstop of `mcpInitTimeout` elapsing. The backstop
duration MUST be no shorter than the budget the fetcher itself runs under, so that a
healthy but slow server is not skipped by waiters while its fetch is still legitimately
in progress.

On backstop expiry the waiting caller SHALL contribute no tools for that server, SHALL
log a warning naming the server, and MUST NOT delete or otherwise poison the shared
cache entry — the fetcher retains ownership of the entry's lifecycle, matching the
existing behavior for a fetch that completed with an error.

#### Scenario: Wedged fetcher does not block unrelated waiters

- **GIVEN** one agent's tool-set resolution has become the fetcher for MCP server `X`
  and is blocked in a call that ignores its context
- **WHEN** a second, unrelated agent resolves its own tool set and waits on the same
  cache entry for `X`
- **THEN** the second agent's wait terminates within `mcpInitTimeout`
- **AND** the second agent resolves its remaining tools and proceeds with no tools from
  `X`
- **AND** the shared cache entry is left intact for the fetcher to finish or fail

#### Scenario: Registry shutdown releases waiters immediately

- **GIVEN** a caller is waiting on an in-flight cache fetch
- **WHEN** the registry's context is cancelled
- **THEN** the wait returns without waiting out the backstop

#### Scenario: Normal cache hit is unaffected

- **GIVEN** an MCP server whose cache entry is already populated and unexpired
- **WHEN** an agent resolves its tool set
- **THEN** the tools are returned from cache with no additional latency and no warning

### Requirement: Closing an MCP client is bounded

Every MCP client SHALL be closed when it is evicted, idle past its timeout, unpooled and its call has finished, or the registry shuts down. Closing SHALL run off the tool-call path: no tool result and no cache waiter SHALL wait for a client to close.

A stdio transport's close blocks until the child process exits, honouring no context. A stdio server that has not exited a grace period after its stdin closed SHALL be signalled — SIGTERM to its process group, then SIGKILL after a further grace period — so the close completes and no process is leaked. Any other close that exceeds `mcpInitTimeout` SHALL be abandoned and the abandonment logged with the server name.

#### Scenario: A close that never completes does not park the caller

- **GIVEN** an MCP server whose close blocks indefinitely
- **WHEN** a tool call using it returns
- **THEN** the caller receives the result without waiting for the close
- **AND** a stdio server is signalled until it exits; any other wedged close is abandoned and logged with the server name

#### Scenario: A cooperative close completes normally

- **WHEN** an MCP server exits on stdin EOF as expected
- **THEN** its client is closed, no signal is sent and nothing is logged as abandoned

#### Scenario: A wedged close does not block cache waiters

- **GIVEN** a cache fetch whose client close overruns its budget
- **WHEN** another caller waits on that server's cache entry
- **THEN** the waiter is released as soon as the fetch result is available, without
  waiting for the close

### Requirement: The MCP handshake is bounded

When connecting an MCP client — on its first use for a connection identity, or after the previous client was evicted — the system SHALL bound the protocol handshake
(`Initialize`) with an explicit deadline of `mcpInitTimeout`, independent of and
in addition to the per-call tool-invocation budget (`callToolTimeoutSeconds` /
`mcpCallToolTimeout`). The handshake deadline SHALL NOT be configurable per server. A call on an already-connected client performs no handshake.

On handshake deadline expiry the system SHALL return a tool error — not a Go error —
whose text names the tool, states that the MCP server did not complete its handshake,
names the elapsed budget, and advises trying a different approach or skipping the step.
The response MUST NOT terminate the agent turn.

When the surrounding context is already cancelled, the returned error SHALL reflect
that upstream cause rather than attributing the failure to the handshake budget.

#### Scenario: Handshake that never returns fails within the budget

- **GIVEN** an MCP server whose transport starts successfully but which never responds
  to `Initialize`
- **WHEN** an agent invokes one of that server's tools
- **THEN** the tool call fails within `mcpInitTimeout` rather than blocking
- **AND** the agent receives a tool error naming the tool and the elapsed budget
- **AND** the agent turn continues so the model can choose another approach

#### Scenario: Handshake budget is not attributed on upstream cancellation

- **GIVEN** an MCP tool call is waiting for its client's handshake
- **WHEN** the surrounding context is cancelled before the handshake budget elapses
- **THEN** the returned tool error reflects the upstream cancellation
- **AND** it does not claim the handshake budget was exceeded

#### Scenario: Handshake budget does not consume the tool-call budget

- **GIVEN** an MCP server that completes its handshake promptly and then runs a tool
  for longer than `mcpInitTimeout` but less than its resolved tool-call timeout
- **WHEN** the tool is invoked
- **THEN** the call succeeds
- **AND** the handshake deadline does not curtail the tool invocation

