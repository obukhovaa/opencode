## ADDED Requirements

### Requirement: A stdio client is retired when its call runs out its own timeout

When a `tools/call` on a pooled stdio client runs out its per-call timeout while the caller's context is still alive, the pool SHALL retire that client, as it does when the caller abandons the call, so the next call starts a fresh process instead of queueing behind the request the server is still working on.

#### Scenario: Server never answers

- **WHEN** a stdio server never answers a call and the call's 1s timeout expires
- **THEN** the call returns a timeout error and the next call on that server runs in a new process
