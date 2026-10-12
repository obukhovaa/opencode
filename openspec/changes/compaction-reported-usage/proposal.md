# Compaction Decides on the Reported Context Size

## Why

Auto-compaction has not run on a long-lived daemon session since the usage guard of `compaction-failure-backoff` shipped. A daemon at a 0.4 threshold (400K of a 1M window) ran for hours at 434K-452K input and reached 672K without one summarizer call, and the sessions were reset by hand instead. Its log names the reason on every call: `implausible reported usage ignored reported=756968 estimated=214983`.

Every compaction decision starts from the provider's token estimate (a `count_tokens` request, or 4 bytes/token locally when the endpoint is unavailable). The usage the provider reported is only a floor, and the guard drops that floor when it is more than 1.5x the estimate. On agentic sessions the estimate is about 3.5x low, so the guard throws away the one exact number there is, and the estimate alone never reaches the threshold. The guard was added for a report that is now and then about twice the real prompt (two calls' cache reads summed upstream); judged against an estimate this far off, it rejects the real reports too.

Every response already carries the exact size of the context the model just saw: input, cache creation, cache read and output, which `TrackUsage` records. Nothing before the last call needs estimating.

## What Changes

- **The reported size decides.** The context size of every compaction decision (the pre-turn gate, the in-loop checks, the re-entry check) is the size the provider reported for the session's last model call plus a local estimate of only the messages appended since: the new user message, tool results, background-task completions. Images and attachments in that delta are priced per item, not as base64 text or a fixed overhead. After a compaction the summary's own reported output is the report and the messages after the summary are the delta.
- **The report's anchor is a call that completed.** A call that failed (for instance because the context was too large) or never finished reports no usage, so the messages before it stay in the delta. Anchored on it, a session whose call failed for its size would never compact again.
- **No estimate of a history a report covers.** No `count_tokens` request and no local estimate of that history; the 1.5x guard goes. A history no report covers (a forked flow step that inherited its parent's log, a compaction whose summary reported no usage, a provider that reports none) is counted by the provider, for that check only, and decides on it.
- **A doubled report is recorded as its last sampling pass.** The doubling is a server-side tool search: it samples twice inside one response, and the response's usage sums both passes (probed on the LiteLLM Bedrock route: one search over a 9.6K prompt reports cache creation 9,612 plus cache read 9,612 cold, cache read 19,224 warm). The provider names the last pass, from `usage.iterations` when sent, otherwise by dividing the input-side counts by the passes, and that is the session's last-call usage. A prompt above the window is recorded at the window. The cost, the session totals and telemetry keep the billed counts. No bound against the previous report, which misfires on retried and cancelled calls and carries its error into later calls.
- **max_tokens is lowered per call.** `AdjustMaxTokens` halves from the configured value on every call instead of the provider's last value, which every session of the agent shares; the real count would otherwise leave a whole process at a halved budget after one near-full session.
- **Each decision is logged.** One `compaction check` line per check names the reported tokens, the delta, the total, the threshold and whether the count is over it; a check with auto-compaction off logs at DEBUG, and a compaction the backoff skips at INFO.

## Capabilities

### Modified Capabilities

- `auto-compaction`: the context count is the last reported usage plus the unsent delta instead of an estimate floored by the report; a failed call is no anchor; a history without a report is counted by the provider; a response's summed sampling passes are recorded as the last pass; `max_tokens` is lowered per call.

## Impact

- `internal/llm/provider`: `TokenUsage.LastPass`; the Anthropic client sets it (`lastPassUsage`, `samplingPasses`); `AdjustMaxTokens` starts from the configured `max_tokens`.
- `internal/llm/agent/compaction.go`: `countContextTokens`, `reportAnchor`/`reportedCall`, `estimateDeltaTokens`; `usageFloorMaxRatio` removed. `compaction_backoff.go`: the skip logs at INFO.
- `internal/llm/agent/agent.go`: `TrackUsage` records `lastCallUsage` (the last pass, at most the window).
- README Auto Compact section, AGENTS.md `compactionThreshold`, docs/deferred-tools.md, the compaction e2e driver's description.
