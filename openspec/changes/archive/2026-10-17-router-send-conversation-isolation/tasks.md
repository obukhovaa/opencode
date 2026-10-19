## 1. Rule

- [x] 1.1 `BridgeSender.SessionPeers`, implemented by `bridge.Service` over `ListBindingsBySession`
- [x] 1.2 `router_send` refuses a send that crosses external conversations, before dispatch and before reading attachments, with an error naming no peer; a turn with no inbound peer is held to its session's (and parent's) external peers, matched exactly, and a failed lookup refuses

## 2. Description

- [x] 2.1 Bound `external` peers are left out of the description
- [x] 2.2 The `external` channel line states that each external peer is a separate conversation

## 3. Tests and docs

- [x] 3.1 Tool: external turn to own / other conversation / a chat channel; chat turn to chat / external; no-peer turn to its session's, its parent's, another session's, an unbound and a case-variant conversation; no-peer turn of an external and of a chat-only session to a chat channel; a failed lookup; no attachment read on refusal
- [x] 3.2 Description omits external bound peers and states the rule
- [x] 3.3 Service: `SessionPeers` for a session with bindings and one without
- [x] 3.4 `docs/bridge.md` router_send section
