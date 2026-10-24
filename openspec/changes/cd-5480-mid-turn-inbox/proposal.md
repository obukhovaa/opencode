# Proposal

## Why

On an interactive flow step, a reviewer's message that arrives while the agent works waits in the bridge's per-session buffer until the agent's next `question`, and only the head of that buffer answers it. The rest is cleared when the step ends, without a word. A chat UI that lets people keep typing while the agent works (as Claude Code does) needs those messages to reach the agent at its next tool or model boundary, all of them, and needs to know which ones arrived.

## What Changes

- **Mid-turn inbox.** On a session marked interactive, the agent loop drains the session's pending reviewer input after a turn's tool results are stored and before the next model call. Each item becomes its own user message, so a batch reads as consecutive user messages with no assistant message or tool call between them.
- **No turn end while input waits.** A run does not end, either on a natural end turn or on an accepted `struct_output`, while the inbox holds input. It drains the inbox and takes one more cycle instead.
- **Question answers keep the rest.** When a question is pending, or is asked while input is buffered, the first item answers it and every other item stays in the inbox. The loop drains them right after the question's tool result. The buffer is no longer consumed one head at a time.
- **Batched inbound with ids.** `POST /router/inbound` accepts an optional `messages: [{id, text}]` array as an alternative to `text`. The batch enters the inbox atomically, so a pending question cannot split it.
- **Delivery acknowledgement.** When items with an id become part of the session, either as a question answer or as drained user messages, the bridge reports them to the bound peers. The `external` adapter sends a new relay frame, `kind: "delivered"` with `delivered.messageIds`. Chat adapters ignore it.
- **Turn budget.** A drain at a tool boundary rides the next model call and costs nothing extra. A cycle that only exists because the inbox held input counts against `max_turns` like any other cycle. The forced max-turns wrap-up turn never drains, so input still waiting at that point stays unacknowledged.

## Capabilities

### New Capabilities
- `interactive-step-inbox`: how reviewer input that arrives during an interactive flow step reaches the running agent mid-turn: drain points, batching, question split, turn-end hold, turn budget and the delivery acknowledgement.

### Modified Capabilities
- `bridge-http-api`: `POST /router/inbound` accepts a batch of messages with ids.

## Impact

- `internal/bridge/service` (question router buffer, `/router/inbound` handler, unbind cleanup), `internal/bridge/external` (relay frame), `internal/bridge` (`Inbound` gains `Messages`).
- `internal/llm/agent/agent.go` inner and outer loop: one drain seam after tool results, plus a hold at the two run-end points.
- New leaf package for the per-session inbox, shared by the bridge and the agent loop without either importing the other.
- No config field, so the `.opencode.json` schema is unchanged. Non-interactive sessions, daemons and TUI sessions are unaffected because their inbox is always empty.
- Wire: additive. An orchestrator that never sends `messages` keeps today's behaviour, apart from mid-turn delivery of buffered input (see design).
