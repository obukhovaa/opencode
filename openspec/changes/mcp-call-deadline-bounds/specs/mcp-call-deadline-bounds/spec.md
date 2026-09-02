# mcp-call-deadline-bounds

Guarantees that no wait on an MCP server can park an agent turn indefinitely. Every
point at which opencode blocks on an MCP server — the per-call protocol handshake, the
shared client-cache entry, and the tool invocation itself — carries an explicit
deadline, and no MCP server process outlives the tool call that spawned it. Protects
against a server that starts successfully but never answers, which previously wedged
the calling goroutine for the life of the process and, through an async `task`
subagent, parked an entire non-interactive flow step.

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

### Requirement: No MCP server process outlives its tool call

Every MCP client created for a tool call SHALL be closed on every exit path from that
call, including handshake timeout, invocation timeout, argument-parsing failure, and
any early error return between client creation and normal completion. A tool call that
has returned MUST NOT leave a live MCP server child process behind.

#### Scenario: Timed-out handshake closes its client

- **WHEN** an MCP tool call fails because the handshake deadline elapsed
- **THEN** the client is closed
- **AND** no MCP server child process spawned for that call remains alive

#### Scenario: Early error return closes its client

- **WHEN** an MCP tool call returns early because its arguments could not be parsed
- **THEN** the client created for that call is closed before returning
