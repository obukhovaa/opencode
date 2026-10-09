## ADDED Requirements

### Requirement: Turns carry the inbound peer

The chat bridge SHALL run every inbound turn with the peer the inbound request named — channel, identity and peer id, as decoded from the authenticated request — on the run context, and SHALL NOT derive it from message text or model output. Async subagents spawned by the turn SHALL inherit it. The text handed to the agent SHALL be the inbound text unchanged.

#### Scenario: The turn carries the inbound peer

- **WHEN** an inbound message arrives for peer `{external, default, t1:d1:c1}`
- **THEN** the agent run's context carries that peer, and the text handed to the agent is the inbound text unchanged

#### Scenario: Question answers stay bare

- **WHEN** an inbound message answers a pending `question` from an external peer
- **THEN** the answer delivered to the question is the bare inbound text

### Requirement: External peer attribution

When a turn's peer is on the `external` channel, the agent SHALL tell the model which peer the session serves with a synthetic user message ahead of the turn's own message. It SHALL be written at most once per history: not again while the history holds the same note, and again after the peer changes or a compaction drops it.

#### Scenario: Note written once

- **WHEN** two consecutive turns in a session serve the same external peer
- **THEN** exactly one synthetic attribution message exists in the history, written before the first turn's message

#### Scenario: Other channels are not announced

- **WHEN** an inbound message arrives from a Slack peer
- **THEN** no attribution message is written

### Requirement: MCP peer header

An MCP server MAY declare `peerHeader`, the name of an HTTP header. A client started for a call made under a context carrying a bridge peer SHALL send that peer's id in that header, replacing any static header of the same name in any letter case, and SHALL NOT mutate the server's configured headers. A call whose context carries no peer SHALL send no value under that name beyond the static configuration. A peer id that is not a valid HTTP header value SHALL NOT fail the call: the call SHALL send no value under that name, static configuration included. Stdio servers receive no headers.

#### Scenario: Header sent from a bridge turn

- **WHEN** a tool of a server with `peerHeader: "X-Peer-Id"` is called from a turn serving peer `t1:d1:c1`
- **THEN** the server's requests carry `X-Peer-Id: t1:d1:c1`

#### Scenario: Configured value cannot pin a peer

- **WHEN** the server's static `headers` also set `x-peer-id`
- **THEN** only the bridge peer's id is sent under that name

#### Scenario: A peer id that cannot be a header value is omitted

- **WHEN** a tool of a server with `peerHeader: "X-Peer-Id"` is called from a turn whose peer id contains a control character
- **THEN** the call proceeds and its requests carry no `X-Peer-Id` header

#### Scenario: Servers without the field are unchanged

- **WHEN** a server declares no `peerHeader`
- **THEN** its headers are exactly its static configuration and any Authorization override
