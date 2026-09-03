# mcp-call-deadline-bounds

Guarantees that no wait on an MCP server can park an agent turn indefinitely. Each
point at which opencode blocks on an MCP server — the per-call protocol handshake, the
tool invocation, the shared client-cache entry, and closing the client — carries an
explicit deadline. Protects against a server that starts successfully but never
answers, which previously wedged the calling goroutine for the life of the process
and, through an async `task` subagent, parked an entire non-interactive flow step.

## ADDED Requirements

### Requirement: The per-call MCP handshake is bounded

When executing an MCP tool call, the system SHALL bound the protocol handshake
(`Initialize`) with an explicit deadline of `mcpInitTimeout`, independent of and
in addition to the per-call tool-invocation budget (`callToolTimeoutSeconds` /
`mcpCallToolTimeout`). The handshake deadline SHALL NOT be configurable per server.

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

- **GIVEN** an MCP tool call is in its handshake
- **WHEN** the surrounding context is cancelled before the handshake budget elapses
- **THEN** the returned tool error reflects the upstream cancellation
- **AND** it does not claim the handshake budget was exceeded

#### Scenario: Handshake budget does not consume the tool-call budget

- **GIVEN** an MCP server that completes its handshake promptly and then runs a tool
  for longer than `mcpInitTimeout` but less than its resolved tool-call timeout
- **WHEN** the tool is invoked
- **THEN** the call succeeds
- **AND** the handshake deadline does not curtail the tool invocation

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

Every MCP client created for a tool call or a cache fetch SHALL be closed on every exit
path. Because a stdio transport's close blocks until the child process exits, honouring
no context, the close itself SHALL be bounded: on expiry the caller SHALL proceed rather
than continue waiting, and the abandonment SHALL be logged with the server name.

Abandoning the close leaks the close goroutine and the child process for the life of the
process. That is the accepted trade: the alternative is parking the agent turn, which is
the failure this capability exists to remove, and the transport exposes no handle with
which to signal the child.

A cache fetch SHALL make its cached entry available to waiters BEFORE closing its
client, so a close that overruns cannot hold waiters on that server — and cannot leave
the shared entry unreadable for the life of the process.

#### Scenario: A close that never completes does not park the caller

- **GIVEN** an MCP server whose close blocks indefinitely
- **WHEN** a tool call using it returns
- **THEN** the caller proceeds within the close budget
- **AND** the abandonment is logged with the server name

#### Scenario: A cooperative close completes normally

- **WHEN** an MCP server exits on stdin EOF as expected
- **THEN** its client is closed and nothing is logged as abandoned

#### Scenario: A wedged close does not block cache waiters

- **GIVEN** a cache fetch whose client close overruns its budget
- **WHEN** another caller waits on that server's cache entry
- **THEN** the waiter is released as soon as the fetch result is available, without
  waiting for the close
