## 1. Count

- [x] 1.1 `countContextTokens`: last reported usage plus a local estimate of the messages after `reportAnchor`; no ratio guard. No report: the provider's `CountTokens` decides, for that check only.
- [x] 1.2 `reportAnchor`: last non-synthetic assistant message that is finished and not finished as an error; the summary message when the report is a compaction's (`PromptTokens == 0`, `CompletionTokens > 0`).
- [x] 1.3 `estimateDeltaTokens`: images (attachments and image tool results) at 1,600 tokens, other attachments by size.
- [x] 1.4 One `compaction check` line per check with the reported tokens, the delta, the total, the threshold and `over_threshold`; DEBUG with auto-compaction off; the backoff skip at INFO.

## 2. Summed sampling passes

- [x] 2.1 `TokenUsage.LastPass`, set by the Anthropic client from `usage.iterations` or by dividing the input-side counts by the response's sampling passes.
- [x] 2.2 `TrackUsage` records the last pass as the last-call usage, and a prompt above the window at the window, with a warning; totals and cost keep the billed counts. No bound against the previous report.
- [x] 2.3 Probe of the LiteLLM Bedrock route (one tool search, cold and warm) recorded in design.md.

## 3. max_tokens

- [x] 3.1 `AdjustMaxTokens` halves from the configured value on every call and sets it only when it changes.

## 4. Tests and docs

- [x] 4.1 Provider: the probe's usage shapes, iterations, parallel searches, `pause_turn`, a single pass; `AdjustMaxTokens` does not stick.
- [x] 4.2 `TestCountContextTokens` rewritten around reports; `TestProcessGeneration_FailedCallDoesNotHideItsHistory`; the in-flight summarize test seeds an over-threshold report.
- [x] 4.3 README Auto Compact section (both copies), AGENTS.md `compactionThreshold`, docs/deferred-tools.md token accounting, the compaction e2e driver's description.
