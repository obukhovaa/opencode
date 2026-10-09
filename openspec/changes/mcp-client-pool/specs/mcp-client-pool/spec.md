## ADDED Requirements

### Requirement: MCP clients are reused across calls

The MCP registry SHALL keep a connected, initialized client per MCP server and connection identity and SHALL use it for every tool call and tool discovery on that server, rather than starting, initializing and closing a client per call. The connection identity SHALL be the server name and its launch configuration (type, command, arguments, environment, URL), and for HTTP servers also the full set of resolved request headers — static headers, the context-scoped Authorization override and the `peerHeader` value. Calls with different resolved headers SHALL NOT share an HTTP client. A stdio server receives no per-call input, so one stdio client SHALL serve every session, agent, bridge peer and flow run; calls to it are multiplexed on its one process.

SSE servers SHALL NOT be pooled: each call SHALL get its own client, whose event stream lives for the whole call, because a dropped SSE event stream cannot be detected on a pooled client.

Connecting SHALL run under the registry's own lifetime, not a caller's context: a caller that stops waiting SHALL NOT fail the connect for other callers, and concurrent first callers of one identity SHALL share a single connect.

#### Scenario: A stdio server is spawned once

- **WHEN** an agent calls tools of a stdio MCP server three times in a session
- **THEN** the server process is spawned once and `initialize` is sent once

#### Scenario: A warm HTTP client sends only the call

- **GIVEN** a streamable-HTTP MCP server whose client is already connected
- **WHEN** a tool of that server is called
- **THEN** the server receives exactly one request, the `tools/call`

#### Scenario: Discovery warms the client for the first call

- **WHEN** tool discovery has listed a server's tools and an agent then calls one of them under the same headers
- **THEN** the call reuses the discovery client without a new `initialize`

#### Scenario: Different identities do not share a session

- **WHEN** two calls to the same HTTP server carry different Authorization overrides or different bridge peers
- **THEN** each is sent on its own client and session, with its own headers

#### Scenario: SSE calls get their own client

- **WHEN** a tool of an SSE server is called twice
- **THEN** each call connects its own client, keeps its event stream until it completes, and succeeds

### Requirement: Clients whose session or process is gone are replaced

A call that fails because the server terminated its session (HTTP 404, `ErrSessionTerminated`) or because the request could not be written to a stdio server that has exited SHALL evict that client and SHALL be retried once on a freshly connected client, because the request provably did not execute. The client SHALL be evicted without retrying when a call fails with any other transport error, with its per-call timeout (enforced even while the transport is blocked writing the request), or — for an HTTP client — with a JSON-RPC error whose code is not one of the standard method-not-found, invalid-params, internal-error, request-interrupted or resource-not-found codes, which is how a server rejects a session it no longer knows. A stdio client SHALL also be evicted when its caller abandons a call mid-flight, so the server stops working on the abandoned request once its other calls finish. An evicted client SHALL NOT be handed to new callers; calls already running on it SHALL complete, and it SHALL be closed when the last of them finishes.

Tool discovery (`tools/list`) on a pooled client SHALL follow the same eviction rules, judged against the registry's lifetime rather than the fetch's own budget, so a fetch that times out evicts the client. Because `tools/list` has no side effects, a discovery fetch whose client is evicted SHALL be retried once on a fresh client while its budget lasts.

#### Scenario: Session terminated by the server

- **GIVEN** a warm HTTP client whose session the server has dropped
- **WHEN** a tool is called and the server answers 404
- **THEN** a new client is initialized, the call is sent once more, and its result is returned

#### Scenario: Stdio server died between calls

- **GIVEN** a pooled stdio server whose process has exited
- **WHEN** one of its tools is called
- **THEN** the call is sent to a newly spawned process and succeeds

#### Scenario: A timed-out call is not retried

- **WHEN** a tool call exceeds its per-call timeout
- **THEN** the agent receives the timeout error, the client is evicted, and the call is not sent again

#### Scenario: An unknown session reported as a JSON-RPC error

- **GIVEN** an HTTP server that answers requests on a session it no longer knows with HTTP 400 and a JSON-RPC error with code -32000
- **WHEN** a tool is called on the stale client
- **THEN** the agent receives the error, the client is evicted, and the next call connects a new session

#### Scenario: An abandoned stdio call stops the server's work

- **WHEN** the agent abandons a call to a stdio server mid-flight
- **THEN** the client is evicted and the server process is stopped once no other call is using it

#### Scenario: Discovery on a stale client

- **GIVEN** a pooled client whose stdio process has exited or whose HTTP session the server has dropped
- **WHEN** an agent's tool-set resolution lists that server's tools
- **THEN** the listing is retried once on a fresh client and the server's tools are returned

### Requirement: Idle clients are closed and reuse can be disabled per server

An MCP server MAY declare `clientIdleTimeoutSeconds`. A pooled client with no call in progress that has been idle longer than that many seconds (600 when unset or zero) SHALL be closed. A negative value SHALL disable reuse for that server: each call gets its own client, closed after the call without delaying the result.

#### Scenario: Idle client closed

- **WHEN** a pooled client has had no call for longer than its server's idle timeout
- **THEN** it is closed, and the next call connects a new one

#### Scenario: Reuse disabled

- **GIVEN** a server with `clientIdleTimeoutSeconds: -1`
- **WHEN** two of its tools are called
- **THEN** each call starts its own client and neither waits for the other client to close

### Requirement: Stdio server processes are contained

Stdio MCP servers SHALL be started in a session of their own where the platform supports it, which makes each a process-group leader and leaves it without a controlling terminal. Their stderr SHALL be drained continuously so a long-lived server cannot block on a full pipe. When a stdio client is closed and its process has not exited within a grace period after stdin is closed, the process group SHALL be sent SIGTERM and, after a further grace period, SIGKILL. Signals SHALL only be sent while Close is still waiting on the process, i.e. before it is reaped.

#### Scenario: A server that ignores stdin EOF is stopped

- **GIVEN** a stdio MCP server that keeps running after its stdin closes
- **WHEN** its client is closed
- **THEN** the process is terminated within two grace periods and no process is left running

#### Scenario: A chatty server keeps working

- **GIVEN** a pooled stdio server that writes more than 64 KiB to stderr over its lifetime
- **WHEN** its tools keep being called
- **THEN** the calls keep completing

### Requirement: MCP clients close on shutdown

On application shutdown the registry SHALL stop handing out clients, abort connects still in progress, close every pooled client and wait, bounded by the shutdown context, for the closes to finish. At shutdown a stdio server SHALL be sent SIGTERM at once and SIGKILL within the shutdown budget, so even the shortest shutdown (the non-interactive ForceShutdown) leaves no server process behind. Cancelling the registry's base context SHALL also close the pool.

In non-interactive mode an interrupt or termination signal SHALL cancel the run rather than kill the process, so the shutdown above still runs; a second signal SHALL terminate as before.

#### Scenario: Shutdown leaves no server processes

- **GIVEN** pooled stdio MCP clients, one of them a server that ignores stdin EOF and SIGTERM
- **WHEN** the application shuts down with a one-second budget
- **THEN** every client is closed and every server process has exited or been killed within the budget
