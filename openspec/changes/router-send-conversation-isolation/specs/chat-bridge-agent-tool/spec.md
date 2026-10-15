## ADDED Requirements

### Requirement: router_send keeps external conversations apart

Each `external` peer is a separate conversation. A turn with an inbound peer SHALL `router_send` to an `external` peer only when it is the turn's own peer, and a turn whose inbound peer is `external` SHALL reach no other peer on any channel. Other such sends SHALL be refused before dispatch with a tool error that names no peer.

#### Scenario: A reply to the turn's own conversation

- **WHEN** a turn whose inbound peer is `external:default:a:d:c1` calls `router_send` to that same peer
- **THEN** the tool dispatches normally

#### Scenario: A send into another conversation

- **WHEN** a turn whose inbound peer is `external:default:a:d:c1` calls `router_send` to `external:default:b:d:c9`
- **THEN** nothing is dispatched, and the tool returns an error that names neither peer

#### Scenario: An external turn messages a chat channel

- **WHEN** a turn whose inbound peer is external calls `router_send` to a Slack user
- **THEN** nothing is dispatched and the tool returns the same error

#### Scenario: Chat-to-chat sends are unchanged

- **WHEN** a turn whose inbound peer is a Slack channel calls `router_send` to another Slack channel
- **THEN** the tool dispatches normally

### Requirement: A turn without an inbound peer stays in its own conversations

A turn with no inbound peer (a flow step, an auto-resumed task, a cron run) SHALL `router_send` to an `external` peer only when that peer's binding is owned by the turn's session or its parent session. An unbound peer, an orphaned binding, or a binding lookup that fails SHALL be refused with the same tool error.

#### Scenario: Its own and another session's conversation

- **WHEN** an auto-resumed turn with no inbound peer calls `router_send` to an external peer bound to its own session, and then to one bound to another session
- **THEN** the first send is dispatched, and the second is refused

### Requirement: Tool description omits external conversations

The tool description SHALL NOT list bound `external` peers, and its `external` channel entry SHALL state that each external peer is a separate conversation and that a turn can send only to the one it belongs to. Bound peers on chat channels are listed as before.

#### Scenario: Bound peers on both kinds of channel

- **WHEN** the tool is registered while a Slack peer and an external peer are bound
- **THEN** the description lists the Slack peer and does not contain the external peer's id

## MODIFIED Requirements

### Requirement: No allowlist enforcement in v1

The tool MUST NOT enforce a peer allowlist on chat channels (`telegram`, `slack`, `mattermost`) in v1. The peer `cfg.Router.AgentPeerAllowlist` field MAY be defined in the config schema as a forward-compat hook but MUST NOT be checked by the tool implementation. The trust boundary for chat channels stays at the orchestrator level (which pods get which `cfg.Router` content), not at tool dispatch. External conversations follow "router_send keeps external conversations apart".

#### Scenario: Agent sends to a non-bound peer

- **WHEN** the agent, in a turn that did not come in on `external`, calls `router_send` with a configured chat channel/identity but a peerId that is NOT in `bridge_sessions` (never been bound)
- **THEN** the tool dispatches normally; the bridge attempts delivery; the result is whatever the platform reports (success if the peer ID is valid; failure if not)
