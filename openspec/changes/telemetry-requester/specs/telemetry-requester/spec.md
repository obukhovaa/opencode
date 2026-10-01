## ADDED Requirements

### Requirement: Trace requester precedence

Every Langfuse trace SHALL carry a `requester` metadata field (namespaced like every other custom key) when a requester is known, chosen in this order: a `requester` flow arg extracted through `telemetry.flowArgs`; the requester carried by the run context; the configured `telemetry.requester`. The field SHALL be omitted when none is known.

#### Scenario: Flow arg wins

- **WHEN** a flow step runs with a `requester` flow arg and the run context also carries a requester
- **THEN** the trace's `requester` is the flow arg value

#### Scenario: Per-turn requester beats static config

- **WHEN** the run context carries a requester and `telemetry.requester` is set
- **THEN** the trace's `requester` is the run context's value

#### Scenario: Static fallback

- **WHEN** neither a flow arg nor a run-context requester is present and `telemetry.requester` is set
- **THEN** the trace's `requester` is `telemetry.requester`

#### Scenario: Nothing known

- **WHEN** no source yields a requester
- **THEN** the trace carries no `requester` field

### Requirement: Chat turns are attributed to the message author

The chat bridge SHALL run each inbound turn with the message author as the run context's requester, resolved to an email address when the adapter implements `bridge.UserEmailResolver` and the platform returns one, else the raw author id. Resolved and email-less results SHALL be cached per channel, identity and author for one hour; a failed lookup SHALL fall back to the author id, SHALL NOT be cached, and SHALL NOT delay the turn beyond a bounded timeout.

#### Scenario: Shared thread with two authors

- **WHEN** two people post in the same bound session
- **THEN** each turn's traces carry that turn's author

#### Scenario: Lookup fails

- **WHEN** the platform lookup returns an error
- **THEN** the turn runs with the raw author id as requester and the next message from that author retries the lookup

### Requirement: Scheduled jobs carry their creator's requester

A cron job SHALL store a requester when created: the explicit `CreateParams.Requester`, else the requester on the creating context. Every run of the job SHALL carry the stored requester on its run context. Jobs created without one SHALL run without a run-context requester.

#### Scenario: Job created during a chat turn

- **WHEN** an agent creates a cron job during a turn attributed to a person
- **THEN** every later run of that job is attributed to the same person

### Requirement: Async subagents inherit the requester

A detached async subagent SHALL run with the requester of the turn that spawned it.

#### Scenario: Background task

- **WHEN** a turn attributed to a person spawns an async subagent
- **THEN** the subagent's traces carry that person as requester
