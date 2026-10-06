# Compaction That Recovers From a Failing Summarizer

## Why

A long-lived daemon session crossed its compaction threshold, and every summarizer call failed after 5-13 s with `stream error: stream ID N; INTERNAL_ERROR; received from peer` and zero usage. More than 200 attempts over four days, none succeeded, so the session kept growing past 650K tokens and every turn re-read all of it.

Three defects combine:

1. **The summarizer request has a shape the upstream can reject.** It declares no tools, yet replays the history as native `tool_use` / `tool_result` blocks, server tool-search blocks and the main model's signed thinking blocks. A model API rejects tool blocks a request does not declare, and an Anthropic-dialect proxy in front of Bedrock answers some rejections with a reset stream instead of a 4xx. The first failure came at about a quarter of the window, so shape is the leading cause and size the secondary one. Tool output also dominates the size of an agentic history.
2. **A failed compaction is retried before every model call.** Only a successful compaction lowers the session's token count, so the next check is over the threshold again. Each step pays a failing summarizer call first: 27 of 27 attempts in one hour.
3. **An implausible usage report can fire compaction.** The trigger floors its estimate on the usage the provider reported for the last call. Now and then that report is about twice the real prompt (two upstream attempts of one call summed into the cache read). Taken at face value, it fires compaction at half the real size.

## What Changes

- **Text transcript for the summarizer.** The summarizer gets one user message: the history since the previous summary as a text transcript, then the compaction prompt. Tool calls and results render as tagged lines, a server tool search renders as one line naming the tools it found, reasoning is dropped, and each tool input or result over 2,000 estimated tokens keeps its head and tail around an omission marker. The trim, its pairing rules and the kept turn prompt work as before, on the transcript entries. Both compaction paths (synchronous and async) use it.
- **Per-agent `summarizerMaxInputTokens`.** An optional cap, in estimated tokens, on what an agent's compaction sends the summarizer (frontmatter and `.opencode.json`, in the schema). The budget is the smaller of the cap and 90% of the summarizer's window. Unset keeps today's behaviour.
- **Compaction failure backoff.** After a failed auto-compaction, a session does not retry in the same turn, and in later turns waits 1 minute, doubling per consecutive failure up to 30 minutes. Growth of 10% of the context window since the failure overrides the wait. Success resets it. Manual compaction ignores it and its success also resets it. A skip logs at debug; a failure logs a warning with the failure count and the next attempt time.
- **Usage plausibility guard.** The reported usage floors the estimate only when it is within the context window and within 1.5x the estimate of the same history; otherwise a warning names the ignored report and the estimate is used. What is stored and sent to telemetry is unchanged.
- **Diagnostics on the summarizer generation.** Its telemetry metadata carries the trigger (`pre_turn`, `in_loop`, `manual`), the estimated input tokens, the message count, the trimmed messages, the truncated tool payloads and the failure count, without logging the input. A summarizer failure with its input near its window logs a likely-overflow warning.

## Capabilities

### Modified Capabilities

- `auto-compaction`: the usage floor gains a plausibility guard, the summarizer input becomes a capped text transcript with an optional per-agent budget, and failed auto-compactions back off.
