# Design: bridge-run-progress-card

## What the reviewer sees

One message per run, edited in place. Its life on Slack:

```
⏳ Thinking...                                   run started, before the first tool call
⏳ 5 tool calls done · running bash · 1m12s      updated as calls complete
⏳ 8 tool calls done · 1 failed · 2m03s
   ✗ bash#a1b2c3 · permission denied exit 1      most recent failure, rune-capped
✓ Done · 12 tool calls · 1 failed · 3m40s        final edit when the run ends
   ✗ bash#a1b2c3 · permission denied exit 1
```

A run whose agent errored ends as `✗ Run failed · N tool calls · elapsed`. Then the
final assistant text arrives as its own message, exactly as today.

The count is the number of `ToolResult` parts observed for the run, including
failures. "Running" names the most recently started call that has no result yet;
with more than one in flight it reads `N running`. Elapsed time is measured from the
card's creation.

## Where the state lives

`runProgress` (`internal/bridge/service/progress.go`) is one struct per run, owned by
the session dispatcher and reachable through `sessionDispatch.progress`
(`atomic.Pointer`). It holds the counters under a mutex, a `wake` channel of
capacity one, and a `done` channel closed after the final edit is flushed.

Three hooks drive it, all in `dispatch.go`:

| Hook | Where | Effect |
| --- | --- | --- |
| `progressStart` | `handleInbound`, after `agent.Run` accepted the message | create the struct if `ToolUpdatesEnabled` and the live mode is `compact`; wake once so `Thinking...` posts immediately |
| `toolStarted` / `toolDone` | `handlePartEvent`, on the parts goroutine | record an in-flight call; count a completion, fold a failure, wake |
| `progressFinish` | `handleInbound`'s deferred tail, after the parts grace window | mark terminal (`ok` / `error`), wake, wait briefly for `done` |

The card is created at run start if and only if the live mode is `compact` at that
moment. After that it counts every completion for its run regardless of later
`/verbosity` switches, so the numbers stay true even if a reviewer flips to `full`
mid-run and per-call cards start appearing beside it. A run that started at `full`
has no card; switching to `compact` mid-run therefore shows nothing for the rest
of that run except failures, which post as fresh lines as they always did without a
card to fold into.

## Why a single worker, and why it is paced

`emitToolRender` fires one goroutine per event. That is fine for per-call cards,
where each event is its own message, but for a card that carries a monotonically
growing count it reorders edits: "8 tool calls done" can land before "7", and the
message ends on the wrong number. The progress card is therefore flushed by one
goroutine per run that reads a snapshot and sends synchronously. Ordering follows.

The same worker paces: after each flush it sleeps `progressMinInterval` (2s) before
looking at `wake` again. Because `wake` has capacity one, every completion that
lands during the sleep collapses into one signal, and the next flush carries the
latest counts. Slack's `chat.update` sits in rate tier 3 (roughly 50 per minute per
channel); an agent finishing ten `read` calls in a second would otherwise be
throttled, and the throttled edits are the ones that carry the newest numbers. The
first flush (`Thinking...`) and the terminal flush are still subject to the same
pacing, which bounds the worst case at one 2s delay before the final state shows.

## Adapter contract: `MessageEditor`

The queued-ack feature already taught every production adapter to post a plain-text
message and edit it later (`QueuedAcknowledger`), but with the ack's texts baked in.
`bridge.MessageEditor` is the same two calls with the text as a parameter:

```go
SendEditable(ctx, peer, text) (EditableMessageToken, error)
EditMessage(ctx, peer, token, text) error
```

Slack encodes `channel\x00ts`, Telegram the message id, Mattermost the post id, all
unchanged from the ack tokens; `SendQueuedAck` / `UpdateQueuedAck` become one-line
wrappers so there is a single edit path per adapter. The progress worker holds one
token per bound peer. If an edit fails (message deleted, token stale) it posts fresh
and keeps the new token, so the chain continues rather than posting fresh on every
subsequent update. Plain text is deliberate: the card is one or two lines, and a
plain message can be edited on every platform without Block Kit or attachment
plumbing.

## Peers that cannot edit

The `external` relay adapter has no message to edit and does not implement
`MessageEditor`. Sending it the card's text on every update would produce one relay
frame per edit, which is the noise this change removes, so it receives nothing for
the card itself. The one exception keeps an existing invariant: a flush that carries a
not-yet-delivered tool failure sends that failure's one-line reason to such peers as
plain text, so a failed call reaches a text-only peer exactly as it did before.

The worker resolves the session's bindings on every flush rather than once at run
start, so a peer bound mid-run picks up the card on the next edit as a fresh post.

## Alternatives considered

- **Silent `compact` plus a new `verbose` tier** (the first spec draft). Rejected: a
  silent thread is indistinguishable from a hung daemon, and the reason the per-call
  cards existed was to make hangs visible. The card keeps that signal in one message.
- **Delete the card when the run ends.** Rejected: a deleted message leaves no trace
  of how long the run took or that anything failed, and a reviewer scrolling back
  later loses the context. A final edit costs one call and keeps the record.
- **Create the card lazily on the first tool call.** Rejected: the reporter asked for
  `Thinking...` at run start, and the gap between message and first call is exactly
  where a reviewer wonders whether the bot heard them.
- **A new `RenderHint` kind routed through the fan-out.** The first cut of this change
  did that, with a per-adapter card cache and a "render-only" flag so text-only
  adapters would drop the fallback. Rejected once the queued-ack code landed on
  `main`: it already had the post-then-edit shape as an adapter interface with
  service-held tokens, and a second mechanism for the same thing is one more thing to
  keep consistent.
- **Reuse the tool-card cache with a synthetic call id.** Rejected: that cache
  consumes on read and evicts at 5 minutes, both wrong for a card edited many times
  over a long run; bending it would break the tool-card contract it exists for.
