## Why

A daemon session driven through the `external` relay shows its consumer nothing while a turn runs. The run progress card (one message per run, edited in place) only reaches adapters that implement `MessageEditor`, and the relay cannot edit a message, so it gets no frame for the card at all — only the occasional failure line. A consumer that draws its own chat UI is left with a bare "waiting" indicator. A turn that spends two minutes in a few slow tool calls then looks hung, even though the bridge knows exactly which tool is running and for how long.

The card's text is the wrong thing to relay: a consumer would have to parse "⏳ 3 tool calls done · running grep · 1m12s" back into numbers to render it in its own words. The state behind the text is what it needs.

## What Changes

- New optional adapter interface `bridge.ProgressRelayer` with `SendProgress(ctx, peer, bridge.RunProgress)`. `RunProgress` is the card's state, structured: status (`running`, `ok`, `error`), tool calls done, failed, in flight, the most recently started unfinished tool, the most recent failure's tool and one-line reason, and elapsed time.
- The progress worker hands every paced card state to a peer whose adapter does not implement `MessageEditor` but does implement `ProgressRelayer`, stamped with the session id — from the first `Thinking...` state to exactly one terminal state. Pacing, coalescing and ordering are the card's own.
- The `external` adapter implements it by POSTing a relay frame with the new kind `progress` and a `progress` object carrying those fields. Every key is always present.
- A relay that accepts a progress update no longer gets that update's failures as plain-text lines too: the state carries the failure count and the last failure, and a separate `✗ <tool>#<id> · <reason>` message read to the consumer as another agent reply. If the relay rejects the update, its failures fall back to text lines, so a failure still surfaces. Other text-only peers are unchanged.

## Capabilities

### Modified Capabilities

- `chat-bridge`: the optional-capability requirement gains `ProgressRelayer`, the relay receives structured progress instead of nothing, and a relay that accepts progress gets failures in the state instead of as text lines.

## Impact

- `internal/bridge` (`RunProgress`, `ProgressRelayer`), `internal/bridge/service/progress.go` (structured state per flush, relayed to `ProgressRelayer` peers), `internal/bridge/external` (`progress` frame kind and `SendProgress`).
- **Relay contract:** the orchestrator endpoint must accept `kind: "progress"` before a daemon on this version sends it (c2-agent 0.4.6, GENAI-448). Until it does, each progress POST is rejected, logged at WARN and recorded as the adapter's last error on `/router/health`; messages, questions and failure lines are relayed as before. Once it accepts, an accepted frame replaces the `✗` text line, so a consumer that wants to show failures reads them from the frame.
- No change for Slack, Telegram or Mattermost peers, and none when tool updates are off or at `full` verbosity, where no card exists.
