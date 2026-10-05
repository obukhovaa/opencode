# Proposal: bridge-relay-intermediate-text

## Why

In daemon mode bound to a chat surface, the bridge posts only the run's terminal
assistant message (`handleTerminalEvent`). Every earlier assistant message of the
run ends in `tool_use`: it is saved in the session store but never relayed. A
reviewer watching a turn such as "text, bash, text, question, summary" sees the
tool cards and the `question` widget without the text the agent wrote around them,
so the question arrives with no context and the reasoning between calls is lost.

The ticket assumed `handlePartEvent` already sees `TextContent` parts. It does not:
the agent publishes only `ToolCall` and `ToolResult` parts on the parts broker, and
text deltas go only to `messages.Update`. The relay therefore reads the finished
message from the store.

## What Changes

- **Intermediate assistant text is relayed.** When the `ToolCall` part that
  completes an assistant message arrives (the `EventComplete` republish, with
  `Finished` set and `Input` merged), the bridge reads the message from the store.
  If it ended in `tool_use` and has text (or `FILE:` attachments), the bridge posts
  `⌛ <tool names>` on the first line, followed by the text. Names are in call order,
  duplicates kept.
- **The text lands above the call's card.** The post is synchronous on the parts
  goroutine and happens before the call is fed to the progress card or posted as a
  `full` 🔧 card.
- **Each message is posted once per run.** A per-run text guard (claim by message ID)
  is shared by the terminal path, the intermediate path and the question flush. A
  run that ends on a `tool_use` message (turn limit) posts it through whichever path
  claims first. Each part is checked against the guard of the run that received it,
  and parts published before the run started (another actor's run that held the
  session while the bridge retried `ErrSessionBusy`) are not relayed.
- **The question widget waits for its text.** `QuestionRouter.handleNewRequest`
  flushes the session's latest assistant message before fanning out the widget: it
  claims the message, sends it on a background goroutine and waits up to 5 s. A slower
  send is not cancelled, so the text still arrives, once.
- **The final reply waits for in-flight text.** Before posting the terminal message the
  bridge waits up to 5 s for intermediate posts of the run that are still being sent.
- **Every intermediate post is bounded**: 10 s, or 60 s with attachments.
- **Unchanged**: the final reply (no header), tool-call and tool-result rendering,
  the progress card, `router.toolUpdateVerbosity`. The relay runs at every verbosity
  and with tool updates off.

## Out of scope

Streaming deltas and in-place edits of the relayed text; self-started turns and
`router_send`; flow-step sessions not run by the bridge dispatcher, including
interactive flow steps bound to chat (a follow-up); subagent text (parts from
descendant sessions are skipped).

## Impact

- `internal/bridge/service/intermediate_text.go` (new): guard, header, relay and
  question flush.
- `internal/bridge/service/dispatch.go`: guard lifecycle in `handleInbound`, the
  guard bound to each `d.parts` item (`partItem`), the trigger in `handlePartEvent`,
  the wait and claim in `handleTerminalEvent`.
- `internal/bridge/service/question.go`: flush before the widget fan-out.
- No config or schema change.
