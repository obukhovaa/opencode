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
- Agent prompt changes. Guidance on how to treat a mid-turn user message is added only once a real run shows an agent mishandling one, and not before.
- A cap on post-`struct_output` reopen cycles. `max_turns` is the only bound; a cap is added only if a real run hits a problem.
- Batching and delivery acknowledgements for chat platforms (phase 2).

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

### D3. Queued input closes a pending question as unanswered; plain text answers it
- **Id-carrying input** (a `messages` batch) is never an answer. In `TryHandleQuestionReply`, a batch with at least one id not yet delivered (D8) is pushed into the inbox. The pending question is then closed with a new `Questions.Unanswered(requestID, note)`, a sibling of `Reject` whose tool result reads: "The user has not answered this question yet; they sent the messages that follow. Ask again if you still need the answer." This happens under `r.mu`, so the D2 drain delivers the batch right after that result. `handleNewRequest` does the same when a question is asked while id-carrying input waits, and the question is never shown to the peers.
- **Plain-text input** (typed message, button click, suggestion chip, no id) answers a pending question exactly as today. When a question is asked with only id-less input buffered, `PopFirst` auto-answers it as today and the remainder drains after the result.
- **Why close rather than keep the question pending:** a UI that holds its Approve or answer controls until the queue empties (as C3 does) would otherwise deadlock. The agent waits for an answer, the queue cannot drain until the question is answered, and the controls stay disabled until the queue drains. Closing as unanswered lets the queue reach the agent as context, after which the agent asks again.

### D4. Batch on the wire, not repeated singles
`bridge.Inbound` gains `Messages []InboundMessage{ID, Text}`. A batch is one HTTP request and one push. Two singles 5 ms apart can straddle the instant a pending question is closed, and the second would then land a model call later. That would break "several messages in one step".

### D5. Acknowledge on entry into the session
`OnDelivered` fires right after a drain stores id-carrying items, and at once for repeats skipped by D8. The bridge fans the ids out to the session's bindings through an optional `DeliveryAcker` adapter capability, the same pattern as `ProgressRelayer`. The `external` adapter implements it as a `kind: "delivered"` relay frame. Items with no id produce no acknowledgement.

### D6. Turn budget
Tool-boundary drains add no model call. A run-end hold adds exactly one cycle, counted by the existing `cycles` / `outerCycles` budget, so a reviewer cannot keep a run alive past `max_turns`.

### D7. All channels, no flag (decided)
The drain applies to every interactive session, Slack and Mattermost reviewers included. Their mid-turn messages now reach the agent at the next boundary instead of waiting for the next question, and they are no longer dropped after the first. No config toggle is added. This is a product decision, not an open point. The PR description states it plainly, so a flow owner who depends on the old wait-for-question behaviour can see it.

### D8. Per-run delivered-id memory
The bridge keeps an in-memory set of delivered message ids per bound peer (the external peer is per run, so this is per run and spans its steps). The set is cleared when the job ends. A push skips ids already in the set and calls `OnDelivered` for them at once, so the sender can settle. A pod restart, or a run whose steps land on different pool pods, loses the set, and a re-send can then repeat. A durable set would need a session-store table and migration, which is deferred until a duplicate shows up.

### D9. Chat-platform leftovers get a "not delivered" reply
`Unbind` (step end) first takes the inbox's remaining items. For each id-less item it sends `replyToPeer` with a short "Your message didn't reach the agent before this step ended — please resend it" (with a quoted excerpt), fire-and-forget like the overflow notice, and only then deletes the bindings. Id-carrying items get no reply, because their sender re-sends.

## Risks / Trade-offs

- [The model treats a mid-turn user message as noise] → The message arrives as a plain user turn after a tool result, which current models handle. Prompt guidance is deferred until a run shows a problem.
- [An acknowledgement frame is lost and the sender re-sends] → D8 skips the repeat and acknowledges it again. A repeat only gets through after a pod restart, which is a visible duplicate, not a loss.
- [The agent re-asks a question after an unanswered close and the user sees it twice] → That is intended: the second ask reflects the new messages.
- [A hold after `struct_output` lets the model change an already accepted output] → This is intended: the reviewer spoke after it. The run still ends within `max_turns`.
- [Overflow past 16 queued items] → The existing audible drop-oldest stays. A dropped item is never acknowledged, so the sender re-sends it.

## Migration Plan

Additive. Ship in a release, and the orchestrator pins it before any sender uses `messages`. Rollback is a pin revert: senders that got no acknowledgement keep their input.
