## Why

An integration that drives a daemon session over the bridge's HTTP API (channel `external`) mints a peer id that names what the conversation is about — for example `<tenant>:<daemon>:<conversation>`. The bridge resolves the session from that peer id, but the model never sees it: a single-peer session gets neither the peer id nor an attribution envelope, and the forwarded text is only what the person typed. So the agent cannot tell which tenant it is serving and has to ask, or guess. Its MCP servers cannot tell either, so a server that should scope its answers to the conversation's tenant has nothing trusted to scope on.

Putting the peer id into the message text is not an option: it would be text the model or the person could forge, it would land in the stored message, and it would break answers to a pending `question`, which must stay the bare value.

## What Changes

- The chat bridge puts the turn's peer — channel, identity, peer id, exactly as the authenticated inbound request carried them — on the run context (`tools.WithPeer`), next to the per-turn requester. Async subagents inherit it.
- For a peer on the `external` channel, the agent writes one synthetic user message ahead of the turn's own message, saying which peer the session serves. It is written once per history: it is not written again while the history holds it, and written again after a peer change or a compaction that dropped it. The person's message is stored unchanged, and the question-reply and interactive-buffer paths, which run before dispatch, still see the bare text.
- New MCP server field `peerHeader`: the name of an HTTP header that carries the calling turn's peer id (sse and http servers). It replaces any static value under that name and is omitted on calls not made from a bridge turn. Schema updated.

## Capabilities

### Modified Capabilities

- `chat-bridge`: two added requirements, peer context for external peers and the per-call MCP peer header.

## Impact

- `internal/llm/tools` (peer context helpers), `internal/bridge/service` (stamps the peer per turn), `internal/llm/agent` (attribution note, async subagent ctx, `peerHeader` layering in `mcp-tool.go`), `internal/config` + `opencode-schema.json`, README.
- No migration: the peer→session binding is already persisted; the turn takes the peer from the inbound request.
- Not covered: turns that are not bridge turns — cron jobs and the turn a background task's completion auto-resumes — carry no peer, so they send no peer header and get no note. Stdio MCP servers cannot receive headers.
- No behaviour change for Slack, Telegram or Mattermost peers beyond the context value, and none for MCP servers without `peerHeader`.
