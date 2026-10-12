# Compaction Decides on the Reported Context Size

## Why

Auto-compaction has not run on a long-lived daemon session since the usage guard of `compaction-failure-backoff` shipped. A daemon at a 0.4 threshold (400K of a 1M window) ran for hours at 434K-452K input and reached 672K without one summarizer call, and the sessions were reset by hand instead. Its log names the reason on every call: `implausible reported usage ignored reported=756968 estimated=214983`.

Every compaction decision starts from the provider's token estimate (a `count_tokens` request, or 4 bytes/token locally when the endpoint is unavailable). The usage the provider reported is only a floor, and the guard drops that floor when it is more than 1.5x the estimate. On agentic sessions the estimate is about 3.5x low, so the guard throws away the one exact number there is, and the estimate alone never reaches the threshold. The guard was added for a report that is now and then about twice the real prompt (two calls' cache reads summed upstream); judged against an estimate this far off, it rejects the real reports too.

Every response already carries the exact size of the context the model just saw: input, cache creation, cache read and output, which `TrackUsage` records. Nothing before the last call needs estimating.

## What Changes

- **The reported size decides.** The context size of every compaction decision (the pre-turn gate, the in-loop checks, the re-entry check) is the size the provider reported for the session's last model call plus a local estimate of only the messages appended since: the new user message, tool results, background-task completions. After a compaction the summary's own reported output is the report and the messages after the summary are the delta.
- **No estimate of the whole history.** The decision sends no `count_tokens` request and runs no local estimate of the history. The 1.5x guard goes with it.
- **No report, no compaction.** A history no report covers (a session's first call, a forked flow step's first call, the first call after a compaction whose summary reported no usage) is not compacted. The first response gives it a report; the in-loop check before the next call uses it. The local estimate of the history is still computed then, for `max_tokens` and the overflow diagnostic only.
- **The doubled cache read is judged against the previous report.** A call can read from cache only a prefix an earlier call of the session sent. When a reported cache read exceeds the previous call's prompt plus output, or the reported prompt exceeds the window, the last-call size is capped there and a warning names the report. The cost, the session totals and telemetry keep the reported values.
- **Each decision is logged.** One INFO line per check names the reported tokens, the delta, the total, the threshold and the outcome.

## Capabilities

### Modified Capabilities

- `auto-compaction`: the context count is the last reported usage plus the unsent delta instead of an estimate floored by the report; the plausibility guard compares a report with the previous one, not with an estimate; a history without a report is not compacted.

## Impact

- `internal/llm/agent/compaction.go`: `countContextTokens` and `reportAnchor`; `usageFloorMaxRatio` removed.
- `internal/llm/agent/agent.go`: `TrackUsage` stores a capped cache read in the last-call usage (`plausibleCacheRead`).
- `provider.CountTokens` keeps its other callers (the compaction e2e driver); no agent code calls it any more.
- README Auto Compact section and AGENTS.md `compactionThreshold`.
