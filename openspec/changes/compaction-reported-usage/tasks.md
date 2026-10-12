## 1. Count

- [x] 1.1 `countContextTokens`: last reported usage plus a local estimate of the messages after `reportAnchor`; no `CountTokens` call, no ratio guard; no report never hits and returns the history estimate for `max_tokens`.
- [x] 1.2 `reportAnchor`: last non-synthetic assistant message; the summary message when the report is a compaction's (`PromptTokens == 0`, `CompletionTokens > 0`).
- [x] 1.3 One INFO line per check with the reported tokens, the delta, the total, the threshold and the outcome.

## 2. Doubled cache read

- [x] 2.1 `plausibleCacheRead` in `TrackUsage`: cap the last-call cache read at the previous call's prompt plus output (only when `PromptTokens > 0`) and the prompt at the window, with a warning; totals and cost unchanged.

## 3. Tests and docs

- [x] 3.1 `TestCountContextTokens` rewritten around reports; the `processGeneration` tests report usage through responses and fail if `CountTokens` is called.
- [x] 3.2 README Auto Compact section (both copies), AGENTS.md `compactionThreshold`.
