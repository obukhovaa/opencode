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

A turn with no inbound peer (a flow step, an auto-resumed task, a cron run) belongs to the `external` peers bound to its session or its parent session. It SHALL `router_send` to an `external` peer only when that peer is one of them, matched exactly on channel, identity and peer id, and SHALL `router_send` to a chat peer only when there are none: an auto-resumed turn of an external conversation carries no inbound peer and must not leave the conversation either. Any other send, and any send whose binding lookup fails, SHALL be refused with the same tool error.

#### Scenario: Its own and another session's conversation

- **WHEN** an auto-resumed turn with no inbound peer calls `router_send` to an external peer bound to its own session, and then to one bound to another session
- **THEN** the first send is dispatched, and the second is refused

#### Scenario: An auto-resumed turn of an external conversation messages a chat channel

- **WHEN** a turn with no inbound peer, whose session is bound to an external peer, calls `router_send` to a Slack channel
- **THEN** nothing is dispatched and the tool returns the same error

#### Scenario: A session bound only to chat peers

- **WHEN** a turn with no inbound peer, whose session is bound only to a Slack channel, calls `router_send` to another Slack channel
- **THEN** the tool dispatches normally

## MODIFIED Requirements

### Requirement: Tool description enumerates configured channels

The `router_send` tool's description SHALL be built at registration time from a snapshot of `cfg.Router`. The description MUST enumerate:

- Each configured channel (`telegram`, `slack`, `mattermost`, `external`).
- For each channel, each enabled identity by ID.
- For each identity, the platform-specific peer-id format (numeric `chat_id` for Telegram; `D<id>` / `U<id>` / `C<id>[|<ts>]` for Slack; `<channelId>[|<rootPostId>]` for Mattermost; `<aid>:<flow_id>:<run_id>` for external, with the statement that each external peer is a separate conversation and a turn can send only to the one it belongs to).
- The list of currently-bound peers on chat channels from `bridge_sessions` (snapshot at registration time) so the agent knows which conversations it can address. Bound `external` peers MUST NOT be listed: each is another conversation, and its id can name another tenant. Each entry MUST carry its provenance: the owning opencode session id (or an explicit orphan marker when the session row was garbage-collected — the FK is `ON DELETE SET NULL`) and a coarse last-active age derived from the row's `updated_at`. The list header MUST warn that entries may belong to a different run — in shared-DB deployments the snapshot spans every run's bindings, including stale leftovers (GENAI-186).

The description format MUST be agent-friendly natural text, not a JSON dump — the agent reads the description as part of its system context.

#### Scenario: Description reflects two channels and two identities

- **WHEN** the tool is registered in a process where Slack has identities `default` and `secondary` (both enabled) and Telegram has identity `default` (enabled)
- **THEN** the tool description lists all three (slack:default, slack:secondary, telegram:default) with their peer-id format

#### Scenario: Description lists currently bound peers

- **WHEN** the tool is registered and `bridge_sessions` contains three rows for the current process's session-bindings
- **THEN** the description includes those bound peers under "currently bound" so the agent knows where it can address messages without learning a new peer ID

#### Scenario: Bound peers on both kinds of channel

- **WHEN** the tool is registered while a Slack peer and an external peer are bound
- **THEN** the description lists the Slack peer and does not contain the external peer's id

#### Scenario: Bound peer entries carry provenance annotations

- **WHEN** the description renders one binding owned by session `1786387649-kickoff-work-clarify-and-prepare` last touched three days ago, and another whose owning session row was garbage-collected
- **THEN** the first entry is annotated `— bound by session 1786387649-kickoff-work-clarify-and-prepare, last active 3d ago` and the second `— orphaned (owning session gone)`, so the agent can recognise a foreign or stale binding before reusing it for a message meant for a specific person

### Requirement: No allowlist enforcement in v1

The tool MUST NOT enforce a peer allowlist on chat channels (`telegram`, `slack`, `mattermost`) in v1. The peer `cfg.Router.AgentPeerAllowlist` field MAY be defined in the config schema as a forward-compat hook but MUST NOT be checked by the tool implementation. The trust boundary for chat channels stays at the orchestrator level (which pods get which `cfg.Router` content), not at tool dispatch. External conversations follow "router_send keeps external conversations apart".

#### Scenario: Agent sends to a non-bound peer

- **WHEN** the agent, in a turn that did not come in on `external`, calls `router_send` with a configured chat channel/identity but a peerId that is NOT in `bridge_sessions` (never been bound)
- **THEN** the tool dispatches normally; the bridge attempts delivery; the result is whatever the platform reports (success if the peer ID is valid; failure if not)
