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
type runTextGuard struct{ m sync.Map } // messageID -> *textClaim
func (g *runTextGuard) claim(id string) (c *textClaim, won bool)
```
- `textGuard atomic.Pointer[runTextGuard]` on `sessionDispatch`. A fresh guard is created in `handleInbound` once Run has started (next to `progressStart`) and cleared with `CompareAndSwap` in the deferred cleanup after `partsDrainGrace` (next to `progressFinish`).
- A nil guard means no bridge-dispatched run is in flight; the intermediate path and the question flush then do nothing. Self-started turns stay out of scope.
- Claims are atomic (`LoadOrStore`) because the terminal path (run goroutine), the parts path (runParts goroutine) and the question flush (router goroutine) can race. The winner posts, then closes `done`.

### Intermediate post
`func (d *sessionDispatch) postIntermediateText(ctx context.Context, msg message.Message)`
1. Return unless `msg.Role == message.Assistant && msg.FinishReason() == message.FinishReasonToolUse`.
2. `ParseFileTokens(agentMessageText(msg), mediaRoot)`; return if there is no text and no attachment. Such a message is not claimed.
3. Claim `msg.ID`; return if not won, otherwise `defer close(c.done)`.
4. Body: `"⌛ " + strings.Join(toolCallNames, ", ") + "\n" + clean`, names in call order, duplicates kept.
5. `SendBySessionID` synchronously; log per-peer failures like the terminal path.

Glyph: Unicode `⌛` (U+231B), constant `intermediateTextGlyph`. Slack shows it as `:hourglass:`; Telegram and Mattermost would print the literal shortcode. Distinct from the `⏳` queued-ack glyph.

### Trigger: `handlePartEvent`
In `case message.ToolCall:`, before the tool-update gate, `relayIntermediateTextForPart` runs when `part.Finished && part.Input != "" && ev.Payload.SessionID == d.sessionID` and the guard has no claim for the message. It reads `Messages.Get(MessageID)` and calls `postIntermediateText`. The rest of the branch is unchanged and runs after the synchronous post. The existing `Synthetic` early return drops synthetic parts. The store is read at most once per ToolUse message with text.

### Terminal dedup: `handleTerminalEvent`
After the `Summarize` return, claim `ev.Message.ID` when a guard exists; if not won, return. The final reply keeps having no header.

### Question ordering
`func (s *Service) flushIntermediateText(ctx, sessionID)` looks the dispatcher up under `dispatchMu` without creating one, returns if it or its guard is nil, reads `ListLatest(ctx, sessionID, 2)` and takes the newest assistant message. Claimed: wait on `done` for at most `intermediateFlushWait` (5 s). Unclaimed: `postIntermediateText`. Called in `QuestionRouter.handleNewRequest` after the buffered auto-answer block and before the widget fan-out.

## Out of scope
Streaming deltas and in-place edits; tool-call and tool-result rendering, the progress card and `router.toolUpdateVerbosity`; self-started turns and `router_send`; flow-step sessions not run by the bridge dispatcher.

## Tests
`internal/bridge/service/dispatch_intermediate_text_test.go`: header + text, call order and duplicates, no text, final reply once without header, dedup in both orders, text before the full 🔧 card, every verbosity, subagent / synthetic / streaming parts, nil guard, question flush before the widget, flush waits on an in-flight post, flush never creates a dispatcher.

## Acceptance (after delivery)
In a bridged DM, the turn "text, bash, text, question, summary" shows `⌛ bash` + text 1, then `⌛ question` + text 2 above the question widget, then the summary exactly once with no header.
