# Tasks: bridge-relay-intermediate-text

## 1. Per-run text guard

- [x] 1.1 `intermediate_text.go`: `textClaim{done}` and `runTextGuard` (`sync.Map` keyed by
  message ID) with an atomic `claim` (`LoadOrStore`)
- [x] 1.2 `dispatch.go`: `sessionDispatch.textGuard atomic.Pointer[runTextGuard]`, set in
  `handleInbound` next to `progressStart`, cleared with `CompareAndSwap` after
  `partsDrainGrace` next to `progressFinish`

## 2. Relay

- [x] 2.1 `postIntermediateText`: assistant + `FinishReasonToolUse` + non-empty text after
  `ParseFileTokens`; claim, send `⌛ <names>\n<text>` synchronously via `SendBySessionID`,
  close `done`
- [x] 2.2 `handlePartEvent`: before the tool-update gate, trigger on `Finished && Input != ""`
  for the dispatcher's own session when the message is not yet claimed; read the message
  with `Messages.Get`
- [x] 2.3 `handleTerminalEvent`: claim `ev.Message.ID` after the Summarize return; skip the
  post when the message was already relayed

## 3. Question ordering

- [x] 3.1 `Service.flushIntermediateText`: look up the dispatcher without creating one, read
  `Messages.ListLatest(…, 2)`, post the newest assistant message or wait on its claim
  (`intermediateFlushWait`, 5 s)
- [x] 3.2 `QuestionRouter.handleNewRequest`: call the flush after the buffered auto-answer
  block, before the widget fan-out

## 4. Tests and docs

- [x] 4.1 `dispatch_intermediate_text_test.go`: header and text, call order, no text, final
  reply once, dedup both orders, text before the full 🔧 card, every verbosity, subagent /
  synthetic / streaming parts skipped, nil guard, question flush and its wait
- [x] 4.2 Doc comments on `handlePartEvent` and `handleTerminalEvent`
- [ ] 4.3 Sync the delta into `openspec/specs/chat-bridge/spec.md` and archive after delivery
