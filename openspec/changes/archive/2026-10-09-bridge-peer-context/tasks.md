## 1. Peer context

- [x] 1.1 `tools.Peer`, `WithPeer`, `PeerFromContext`
- [x] 1.2 Bridge stamps the inbound peer on the run context
- [x] 1.3 Async subagents inherit the peer

## 2. Attribution

- [x] 2.1 Synthetic attribution note for external peers, once per history
- [x] 2.2 Tests: written once, re-written for another peer, other channels skipped, reminder tags defused

## 3. MCP peer header

- [x] 3.1 `peerHeader` config field and schema
- [x] 3.2 Layer the peer id in `StartClient` (case-insensitive replace, no mutation)
- [x] 3.3 Tests: unit layering and a stub server receiving the header

## 4. Docs

- [x] 4.1 README MCP section
