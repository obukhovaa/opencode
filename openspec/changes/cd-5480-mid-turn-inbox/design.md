# Design

## Context

- `dispatchInbound` (`internal/bridge/service/inbound.go`) routes an inbound for an interactive session to `QuestionRouter.TryHandleQuestionReply` when a question is pending, and otherwise to `QuestionRouter.BufferInbound`, a per-session FIFO (cap 16, drop-oldest with a notice).
- `handleNewRequest` (`question.go`) answers a newly asked question from `popBufferedLocked`, which takes ONE item. `Unbind` → `ClearSession` drops the rest at step end. Between questions the agent never sees buffered input.
- Flow steps run `RunWith(..., RunOptions{NonInteractive: true})` (`internal/flow/service.go`). Interactive steps do too. In `agent.go` the inner loop appends `agentMessage, *toolResults` to `msgHistory` and `continue`s. It ends on a natural end turn or an accepted `struct_output`, and the outer loop re-enters only to drain background tasks.
- The Anthropic converter emits one API message per stored message, and consecutive user messages already work. `Message.Content()` returns only the first text part, so each input item has to be its own stored message.

## Goals / Non-Goals

**Goals:** mid-turn delivery at tool and model boundaries on interactive sessions; whole-buffer drains as consecutive user messages; atomic batches; an id-based delivery acknowledgement; and no input lost inside a step.

**Non-Goals:**
- Interrupting a model stream or a tool call.
- Surviving step end. Input still waiting when the step ends is cleared, as today. The sender keeps it, because it was never acknowledged, and re-sends it to the next interactive step.
- Persisting the inbox, or delivering into non-interactive sessions, daemons or the TUI. The TUI already has its own `chat-message-queue`.
- Agent prompt changes.

## Decisions

### D1. One leaf package owns the per-session inbox
The `QuestionRouter.buffered` map, its cap and its overflow notice move into a small leaf package (`internal/inbox`). It offers `Push(session, items...)` (atomic), `PopFirst(session)`, `DrainAll(session)`, `Clear(session)` and an `OnDelivered(session, ids)` hook. Both the bridge and the agent loop use it, and neither imports the other.
*Alternative:* an interface on the agent that the bridge implements, registered like `task.GlobalRegistry()`. That gives the same seam with a callback in the opposite direction. The leaf package is simpler to test.

### D2. Drain at the tool boundary, hold at the two run-end points
- **After tool results.** Right after `msgHistory = append(msgHistory, agentMessage, *toolResults)` and before `continue`, the loop drains the inbox. Each item becomes a `message.User` text message (author attribution as today's envelope) and is appended to `msgHistory`.
- **Natural end turn.** Before `finalResult` is set and the inner loop breaks, a non-empty inbox is drained and the loop `continue`s instead of ending.
- **`struct_output` accepted.** Before `break OuterLoop`, a non-empty inbox is drained and the loop continues. `structOutput` stays captured.
- **Max-turns wrap-up.** The forced wrap-up turn does not drain. Input that reaches it is left for the sender to re-send.
*Why here:* these are the only points where nothing is streaming or executing, so no turn gets cancelled. Draining at the tool boundary keeps the provider's tool_use → tool_result pairing intact, because user messages only ever follow a complete tool_result message.

### D3. A question takes the first item and leaves the rest
`TryHandleQuestionReply` with a batched inbound pushes items 2..n into the inbox *before* calling `Questions.Reply` with item 1, all under `r.mu`. The question's tool result therefore always reaches the agent loop with the rest already waiting, and the D2 drain picks them up on the same boundary. `handleNewRequest` keeps using `PopFirst`, which now leaves the remainder for the loop instead of for the next question. This fixes the head-only consumption.

### D4. Batch on the wire, not repeated singles
`bridge.Inbound` gains `Messages []InboundMessage{ID, Text}`. A batch is one HTTP request and one push. Two singles 5 ms apart can straddle the instant a question is answered, and the second would then land a model call later. That would break "several messages in one step".

### D5. Acknowledge on entry into the session
`OnDelivered` fires right after a question `Reply` succeeds (for item 1) and right after a drain stores its messages (for the rest). The bridge fans the ids out to the session's bindings through an optional `DeliveryAcker` adapter capability, the same pattern as `ProgressRelayer`. The `external` adapter implements it as a `kind: "delivered"` relay frame. Items with no id, such as typed chat-adapter messages, produce no acknowledgement.

### D6. Turn budget
Tool-boundary drains add no model call. A run-end hold adds exactly one cycle, counted by the existing `cycles` / `outerCycles` budget, so a reviewer cannot keep a run alive past `max_turns`.

### D7. All channels, no flag
The drain applies to every interactive session, Slack and Mattermost reviewers included. Their mid-turn messages now reach the agent at the next boundary instead of waiting for the next question, and they are no longer dropped after the first. No config toggle is added. Reviewers flag it in the PR if a flow depends on the old wait-for-question behaviour.

## Risks / Trade-offs

- [The model treats a mid-turn user message as noise] → The message arrives as a plain user turn after a tool result, which current models handle. Prompt guidance is deferred until a run shows a problem.
- [An acknowledgement frame is lost, the sender re-sends at the next step, and the agent sees the input twice] → The relay retries like other frames. A visible duplicate is recoverable, while a silent loss is not.
- [A hold after `struct_output` lets the model change an already accepted output] → This is intended: the reviewer spoke after it. The run still ends within `max_turns`.
- [Overflow past 16 queued items] → The existing audible drop-oldest stays. A dropped item is never acknowledged, so the sender re-sends it.

## Migration Plan

Additive. Ship in a release, and the orchestrator pins it before any sender uses `messages`. Rollback is a pin revert: senders that got no acknowledgement keep their input.
