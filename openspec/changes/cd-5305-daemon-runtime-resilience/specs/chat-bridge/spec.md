## ADDED Requirements

### Requirement: Stop commands cancel the session's run and its queued messages

The bridge SHALL accept `/stop` and `/cancel` as aliases of `/abort`. `/abort` SHALL cancel the bound session's in-flight run on whichever agent instance holds it, stop the session's drain worker, empty its queue, drop the inbound messages waiting in the session's dispatcher, and make a message that is waiting out the busy session give up instead of running. The reply SHALL say whether a run was cancelled and how many queued messages were dropped.

#### Scenario: /stop on a busy session

- **WHEN** a run is in flight on the bound session and two messages are queued behind it
- **AND** the user sends `/stop`
- **THEN** the run is cancelled, both queued messages are dropped, and the next message starts exactly one run

### Requirement: /reset cancels before it unbinds

`/reset` (and `/new`) SHALL cancel the old session's run and queued messages as `/abort` does before deleting the binding, and its reply SHALL say when a run was cancelled.

#### Scenario: /reset during a run

- **WHEN** a run is in flight on the bound session and the user sends `/reset`
- **THEN** the run is cancelled, the binding is deleted, and no second run coexists with it

### Requirement: /agent is refused while the bound session is busy

`/agent <name>` SHALL NOT switch the agent while a run is in flight on the peer's bound session; it SHALL reply with a hint to `/stop` first.

#### Scenario: Switch attempt mid-run

- **WHEN** a run is in flight on the bound session and the user sends `/agent other`
- **THEN** the active agent is unchanged and the reply points at `/stop`

### Requirement: The agent is persisted per session

The system SHALL store the agent a session runs under in `sessions.agent_name`: `/agent <name>` on a bound session writes it, and every bridge-dispatched run stamps the agent it ran with. The dispatcher and the heartbeat paths SHALL run a session under its persisted agent when that name is a registered primary agent, and otherwise under the active agent, logging a WARN for an unknown name.

#### Scenario: Choice survives a restart

- **WHEN** the user switched a bound session to agent `x` and the process restarts with another default agent
- **THEN** the next turn on that session runs as `x`

#### Scenario: Persisted agent no longer registered

- **WHEN** a session's persisted agent is not a registered primary agent
- **THEN** the turn runs under the active agent and a WARN names the unknown agent
