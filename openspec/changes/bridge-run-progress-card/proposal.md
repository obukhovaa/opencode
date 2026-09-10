# Proposal: bridge-run-progress-card

## Why

In daemon mode bound to Slack, a reviewer watching a run sees one chat message per
tool call even at the default `compact` verbosity: a `🔧 bash#a1b2c3` card when the
call starts, edited to `✓ bash#a1b2c3 · 1.4s` when it finishes. A task that makes
forty tool calls narrates itself forty times before its one real answer arrives.
The intent behind `compact` was a progress indicator, not a transcript, but "one
line per call" is still a transcript once the call count is high.

The first draft of this change proposed making `compact` silent and adding a
`verbose` tier for the per-call lines. That trades noise for anxiety: a silent
thread for ten minutes reads as a hung daemon, which is exactly the failure the
per-call cards were added to make visible. What a reviewer needs from the default
level is a single, live answer to "is it still working, and how far along is it".

## What Changes

- **`compact` posts one progress card per agent run and edits it in place.** The card
  is posted when the run starts, reading `⏳ Thinking...`. As tool calls complete it
  is updated to the real running count (`⏳ 5 tool calls done · 1m12s`,
  `⏳ 8 tool calls done · 2m03s`), naming the tool currently in flight when one is
  known. When the run ends the card is edited one final time to a terminal state
  (`✓ Done · 12 tool calls · 3m40s`). No per-tool-call message is posted at
  `compact`, ever.
- **Failures fold into the card instead of posting separately.** A failed call adds a
  failure count and the most recent failure's one-line, rune-capped reason to the
  card. Adapters that cannot edit a message in place still receive that one line as
  plain text, so a broken call surfaces everywhere; they receive nothing else from
  the card.
- **Updates are paced and coalesced.** A single per-run worker serialises the edits,
  so counts never arrive out of order, and successive completions inside the pacing
  interval collapse into one edit carrying the latest numbers. This keeps a
  fast-firing tool loop under Slack's `chat.update` rate tier.
- **`full` is unchanged**: one card per call with the argument summary and a
  rune-capped result body. `verbose` and `debug` are accepted as aliases of `full`
  so the vocabulary the reporter used keeps working; unknown values still resolve to
  `compact` with one WARN.
- **One small adapter interface, already half-built.** `bridge.MessageEditor`
  (`SendEditable` / `EditMessage`) is the generic form of what the queued-ack
  feature added per adapter last release; Slack, Telegram and Mattermost implement
  it and their `QueuedAcknowledger` methods become thin wrappers over it. The relay
  (`external`) channel has no message to edit, so it receives nothing for the card
  except a failed call's one-line reason, which it still gets as plain text.

## Capabilities

### Modified Capabilities

- `chat-bridge`: "Tool updates are compact by default, one line per call" is replaced
  by "Compact tool updates are one progress card per run, updated in place"; the
  runtime `/verbosity` requirement gains the aliases; the typing/reporting
  indicators requirement's "tool updates enabled" scenario now points at the card.
  A new requirement covers the optional `MessageEditor` adapter capability.

## Impact

`internal/bridge/bridge.go` (`MessageEditor`), `internal/bridge/config.go` (aliases,
field doc), `internal/bridge/service/{progress.go,dispatch.go,service.go,commands.go}`,
`internal/bridge/{slack,telegram,mattermost}/adapter.go` (`SendEditable` /
`EditMessage`, ack methods rebased on them), tests alongside each, `docs/bridge.md`.
No schema change: the `router` block is not declared in `cmd/schema/main.go` today,
so there is nothing there to extend.

Out of scope: removing or merging `toolUpdatesEnabled`; persisting `/verbosity`;
the queue-acknowledgement cards themselves; the orchestrator's own job
notifications; any change on the deployment that embeds the runtime (config line, tag bump) — those
follow once a runtime release carries this.
