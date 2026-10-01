# Relay intermediate assistant text to the chat bridge

## Problem
The chat bridge (`internal/bridge/service`) posts only the run's terminal `AgentEvent` message (`handleTerminalEvent`). Assistant messages that end in `tool_use` are saved but never relayed, so the reviewer sees tool cards and `question` widgets without the text around them.

A detail the ticket got wrong: `handlePartEvent` never sees `TextContent`. The agent publishes only `ToolCall` parts (`agent.go` processEvent: EventToolUseStart, EventToolUseStop, EventComplete republish) and `ToolResult` parts (`streamAndHandleEvents`) on the parts broker. Text deltas go only to `messages.Update`. The fix therefore reads the message from the store.

## Key facts the design relies on
- At `EventComplete`, `processEvent` merges the tool calls, adds the Finish part and calls `messages.Update`, all **before** republishing each ToolCall with its final `Input`. When the bridge sees a ToolCall with `Finished && Input != ""`, `Messages.Get(MessageID)` therefore returns the full text, every tool call in order, and `FinishReason()==tool_use`.
- With streaming providers, the EventToolUseStop publish has `Finished=true` and `Input==""`, and the trailing tool calls have not streamed yet. It must NOT trigger, or the header would be incomplete. That is the same filter the call cards already use.
- `drainParts` forwards parts from subagent sessions too (`isOwnedSession`). Only `ev.Payload.SessionID == d.sessionID` qualifies.
- `emitToolRender` is fire-and-forget (it runs in a goroutine). To get text above the card, send the text synchronously on the runParts goroutine before the card is emitted.
- Compact verbosity uses one progress card (`progress.go`), posted at run start and edited in place. There is no per-call line, so intermediate text appears below the card. Full verbosity posts per-call 🔧/✓/✗ cards.
- The question widget is posted by `QuestionRouter.handleNewRequest`, triggered from the question tool's goroutine. This races the parts goroutine, so an explicit flush is needed. The latest assistant message of the session (`Messages.ListLatest`, oldest first) is the one that owns the pending question call, because the tool-result message is created only after the tool returns.

## Design
### Per-run guard
```go
type textClaim struct{ done chan struct{} }
type runTextGuard struct {
	sinceMs int64    // wall clock before the Run attempt that succeeded
	m       sync.Map // messageID -> *textClaim
}
func (g *runTextGuard) claim(id string) (c *textClaim, won bool)
type partItem struct {
	ev    pubsub.Event[message.PartEvent]
	guard *runTextGuard
}
```
- `textGuard atomic.Pointer[runTextGuard]` on `sessionDispatch`. A fresh guard is created in `handleInbound` once Run has started (next to `progressStart`) and cleared with `CompareAndSwap` in the deferred cleanup after `partsDrainGrace` (next to `progressFinish`). The terminal path and the question flush load it.
- `d.parts` is shared by every run of the session and `runParts` can lag (the intermediate post is a blocking send), so the parts path does not load `textGuard`: `drainParts(ctx, sub, guard)` wraps each forwarded event in a `partItem` with the guard of the run that subscribed it. A late part of run N is then checked against run N's guard, not against nil (text lost) or run N+1's (text posted twice).
- `handleInbound` subscribes to parts before its `ErrSessionBusy` retry loop, so the subscription also buffers the parts of another actor's run (task auto-resume, API run) that held the session meanwhile. `sinceMs` is taken just before each `Run` attempt; the value of the attempt that succeeds goes on the guard, and the parts path drops events whose `PartEvent.Time` (unix millis, set by `PublishPart`) is earlier.
- A nil guard means no bridge-dispatched run is in flight; the intermediate path and the question flush then do nothing. Self-started turns stay out of scope.
- Claims are atomic (`LoadOrStore`) because the terminal path (run goroutine), the parts path (runParts goroutine) and the question flush (router goroutine) can race. The winner posts, then closes `done`.

### Intermediate post
`func (d *sessionDispatch) postIntermediateText(ctx context.Context, g *runTextGuard, msg message.Message)`
1. Return unless `g != nil && msg.Role == message.Assistant && msg.FinishReason() == message.FinishReasonToolUse`.
2. Claim `msg.ID`; return if not won, otherwise `defer close(c.done)`.
3. `sendIntermediateText`: `ParseFileTokens(agentMessageText(msg), mediaRoot)`; return if there is no text and no attachment. The claim stays, so the message's later ToolCall parts skip the store read.
4. Body: `"⌛ " + strings.Join(toolCallNames, ", ") + "\n" + clean`, names in call order, duplicates kept.
5. `SendBySessionID` synchronously under `intermediateSendTimeout` (10 s; `intermediateUploadTimeout`, 60 s, with attachments): slack-go's default HTTP client has no timeout, and a hung send would stall the session's tool updates and overflow `d.parts`. Log per-peer failures like the terminal path.

Glyph: Unicode `⌛` (U+231B), constant `intermediateTextGlyph`. Slack shows it as `:hourglass:`; Telegram and Mattermost would print the literal shortcode. Distinct from the `⏳` queued-ack glyph.

### Trigger: `handlePartEvent`
In `case message.ToolCall:`, before the tool-update gate, `relayIntermediateTextForPart(ev, part, guard)` runs when `part.Finished && part.Input != "" && ev.Payload.SessionID == d.sessionID`, the item's guard is set, `ev.Time >= guard.sinceMs` and the guard has no claim for the message. It reads `Messages.Get(MessageID)` and calls `postIntermediateText`. The rest of the branch is unchanged and runs after the synchronous post. The existing `Synthetic` early return drops synthetic parts. The store is read at most once per ToolUse message with text.

### Terminal dedup: `handleTerminalEvent`
After the `Summarize` return, when a guard exists: wait for every claim taken so far to be released, for at most `intermediateFlushWait` in total (`runTextGuard.waitInFlight`), so the final reply does not land above a text still being posted; then claim `ev.Message.ID`; if not won, return. A message the parts goroutine has not reached yet is not waited for. The final reply keeps having no header.

### Question ordering
`func (s *Service) flushIntermediateText(ctx, sessionID)` looks the dispatcher up under `dispatchMu` without creating one, returns if it or its guard is nil, reads `ListLatest(ctx, sessionID, 2)` and takes the newest assistant message. Unclaimed (and ending in `tool_use`): claim it on the router goroutine and run `sendIntermediateText` on a `launchSupervised` goroutine under the service context, which closes `done`. Either way, wait on `done` for at most `intermediateFlushWait` (5 s), because the question router is one goroutine for every session. Only the wait is bounded: cancelling the send at 5 s would leave the claim taken and the text lost, since the parts path skips a claimed message. Called in `QuestionRouter.handleNewRequest` after the buffered auto-answer block and before the widget fan-out.

## Out of scope
Streaming deltas and in-place edits; tool-call and tool-result rendering, the progress card and `router.toolUpdateVerbosity`; self-started turns and `router_send`; flow-step sessions not run by the bridge dispatcher, including interactive flow steps bound to chat (follow-up).

## Tests
`internal/bridge/service/dispatch_intermediate_text_test.go`: header + text, call order and duplicates, no text, final reply once without header, dedup in both orders, text before the full 🔧 card, every verbosity, subagent / synthetic / streaming parts, nil guard, question flush before the widget, flush waits on an in-flight post, flush never creates a dispatcher. Review fixes: `handleInbound` end to end with a stub agent (own run relayed; another actor's part buffered during the busy retry not relayed), a late part of a finished run checked against its own run's guard, a flush send slower than the wait delivered once, the parts-path send bounded, the final reply waiting for an in-flight text.

## Acceptance (after delivery)
In a bridged DM, the turn "text, bash, text, question, summary" shows `⌛ bash` + text 1, then `⌛ question` + text 2 above the question widget, then the summary exactly once with no header.
