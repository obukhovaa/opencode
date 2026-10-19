## Why

One daemon can serve many `external` conversations at once: each external peer is a separate conversation of the consumer, often a different user and a different tenant. `router_send` lets the model choose the destination freely, and its description lists every bound peer with its id. A persuaded or prompt-injected model can therefore post into another user's conversation, carrying data from its own turn with it, or message any Slack user from inside an external conversation. The consumer's own scoping cannot help: the send arrives as a normal reply in the other conversation.

The bridge already knows which conversation a turn serves. It sets the turn's inbound peer on the context from the authenticated inbound request, and MCP servers declaring a `peerHeader` receive it. The model never chooses it. `router_send` should use the same fact.

## What Changes

- `router_send` refuses a send that would cross between external conversations, with an error that names no peer:
  - A turn the bridge dispatched may send to an `external` peer only if it is the turn's own peer. A turn that came in on `external` may send nowhere else, other chat channels included.
  - A turn with no inbound peer (a flow step, an auto-resumed task, a cron run) belongs to the `external` peers bound to its session or its parent session. It may send to an `external` peer only if the peer is one of them, and to a chat peer only if there are none, so an auto-resumed turn of an external conversation cannot leave it either.
  - Sends between chat channels from chat turns are unchanged.
- The tool description no longer lists bound `external` peers, and says that each external peer is a separate conversation the turn may not leave.
- `BridgeSender` gains `SessionPeers(ctx, sessionID)`, the peers bound to a session; `bridge.Service` implements it over the binding store.

## Capabilities

### Modified Capabilities

- `chat-bridge-agent-tool`: the description omits external bound peers; the no-allowlist rule now covers chat channels only; new requirements keep external conversations apart.

## Impact

- `internal/llm/tools/router_send.go` (the rule, the description), `internal/bridge/service/send.go` (`SessionPeers`), tests, `docs/bridge.md`.
- An agent that relied on sending into other external conversations, or out of one into a chat channel, now gets a tool error. Replying to the current conversation, and chat-to-chat sends, work as before.
