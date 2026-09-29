## ADDED Requirements

### Requirement: Pool-mode inbound is delivered only to an owned interactive session

When `opencode serve` runs with `--pool-mode`, `POST /router/inbound` SHALL accept an inbound only when the peer has a binding whose session the flow engine has marked interactive in the current process. Otherwise it SHALL respond `409` with a JSON body carrying `sessionNotOwned: true` and SHALL NOT enqueue the inbound. In pool mode, the inbound dispatcher SHALL NOT hand an inbound to the workspace default agent. The flow engine SHALL set the interactive marker before it binds a step's peers and clear it only after they are unbound. Without `--pool-mode`, behaviour is unchanged.

#### Scenario: Container restarted mid-step

- **WHEN** a pool pod's process restarts while an interactive step was waiting, the peer's binding survives, and the orchestrator forwards a reply
- **THEN** the endpoint responds `409` with `sessionNotOwned: true` and no agent runs

#### Scenario: Owned interactive session

- **WHEN** an interactive flow step in this process has bound the peer
- **THEN** the endpoint responds `202`, including for a reply forwarded the instant the binding is registered

#### Scenario: Step ends between accept and dispatch

- **WHEN** an inbound is accepted but the step completes before the inbound is dispatched
- **THEN** the inbound is dropped and the default agent does not run

#### Scenario: Non-pool pod

- **WHEN** `opencode serve` runs without `--pool-mode` and the peer has no binding
- **THEN** the endpoint responds `202` and a session is allocated as before
