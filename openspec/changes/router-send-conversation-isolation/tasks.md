## 1. Rule

- [x] 1.1 `BridgeSender.BindingSessionID`, implemented by `bridge.Service` over `GetBinding` (unbound, orphaned or a store error answers false)
- [x] 1.2 `router_send` refuses a send that crosses external conversations, before dispatch, with an error naming no peer

## 2. Description

- [x] 2.1 Bound `external` peers are left out of the description
- [x] 2.2 The `external` channel line states that each external peer is a separate conversation

## 3. Tests and docs

- [x] 3.1 Tool: external turn to own / other conversation / a chat channel; chat turn to chat / external; no-peer turn to its session's, its parent's, another session's and an unbound conversation, and to a chat channel
- [x] 3.2 Description omits external bound peers and states the rule
- [x] 3.3 Service: `BindingSessionID` for a bound and an unbound peer
- [x] 3.4 `docs/bridge.md` router_send section
