## 1. Per-agent threshold

- [x] 1.1 Add `CompactionThreshold float64 json:"compactionThreshold,omitempty"` to `config.Agent`; `validateAgent` warns and zeroes a value outside (0, 1].
- [x] 1.2 `internal/agent/registry.go`: yaml frontmatter `compactionThreshold`, merged at the same sites as `taskBudget`.
- [x] 1.3 `cmd/schema/main.go`: `compactionThreshold` on both agent definitions; regenerate `opencode-schema.json`.
- [x] 1.4 `resolveCompactionThreshold(opts)`: flow step > agent > default; replace the `effectiveCompactionThreshold(opts.CompactionThreshold)` call sites.
- [x] 1.5 Document in `AGENTS.md`, `README.md` and the `RunOptions.CompactionThreshold` comment.

## 2. Token floor

- [x] 2.1 `countContextTokens(ctx, sessionID, threshold, msgs, toolSet)`: `max(estimate, PromptTokens+CompletionTokens+tail)`; hit recomputed against the window; Info log when the floor beats the estimate by >10%.
- [x] 2.2 Use it at the pre-turn gate, the in-loop check and the post-compaction recount.

## 3. Pre-turn gate

- [x] 3.1 Move `resolveTools()` above `createUserMessage`.
- [x] 3.2 Count history + transient user message; compact when over threshold and `autoCompact`; rebuild history; persist the user message after the summary.
- [x] 3.3 Factor the task-budget-remaining block into a helper (`withTaskBudgetRemaining`); used by the in-loop path only — the gate leaves the turn's budget full.
- [x] 3.4 Auto-resume turn: carry the trailing synthetic completion pair past the summary (`syntheticTail`).
- [x] 3.5 Wait (bounded, ctx-aware) for an in-flight `Summarize` of the session, then reload and count.
- [x] 3.6 In-loop guard `cycles != 1 || outerCycles > 1`: a non-interactive re-entry's first call is checked; its drained pairs survive a compaction there too.

## 4. Summarizer input

- [x] 4.1 `summarizerInput(ctx, sessionID, prompt)`: start after `SummaryMessageID`, trim oldest to 90% of the summarizer window without orphaning tool results, WARN on trim.
- [x] 4.2 Use it from `performSynchronousCompaction` and `Summarize`.

- [x] 4.3 `message.EstimateTokens` counts tool-call input, tool-result content and reasoning text, not only text parts.
- [x] 4.4 Calibrate the trim budget by the session's reported usage when it exceeds the local estimate.
- [x] 4.5 Clear the forced-tool signal on the summarizer ctx.
- [x] 4.6 After a compaction, forget the session's announced deferred tools; the delta dedup scans only the history from `SummaryMessageID`.
- [x] 4.7 `geminiClient.usage`: `InputTokens = max(PromptTokenCount - CachedContentTokenCount, 0)`.
- [x] 4.8 The summarizer trim keeps the turn's prompt (`turnPromptIndex`: latest non-synthetic user message with text) after the head, unless it alone exceeds half of the space left after the head and fixed parts, or keeping it would leave no recent history or the input over budget (then the trim is redone unprotected).
- [x] 4.9 The in-loop compaction rebuild re-announces deferred MCP tools (`injectDeferredDelta` after the schema envelope); document it in `docs/deferred-tools.md`.

## 5. Overflow diagnostic

- [x] 5.1 `likelyContextOverflow(estimated, window)` and the WARN in the two non-cancel error branches after `streamAndHandleEvents`.

## 6. Tests

- [x] 6.1 Gate compacts before the first `StreamResponse`; summary precedes the persisted user message; no compaction under the threshold.
- [x] 6.2 `countContextTokens` floor / no-assistant / post-compaction / zero window.
- [x] 6.3 `resolveCompactionThreshold` precedence.
- [x] 6.4 `viper.Unmarshal` round-trip of `agents.<name>.compactionThreshold`; validation zeroes invalid values.
- [x] 6.5 Registry frontmatter merge.
- [x] 6.6 `summarizerInput` starts at the summary, trims oldest, never orphans tool results.
- [x] 6.7 `likelyContextOverflow` boundaries.
- [x] 6.8 `make schema-check`, `go test ./cmd/schema/...`, `make test`.
- [x] 6.9 Auto-resume compaction keeps the pair; re-entry after a drain is gated; in-flight `Summarize` is awaited; the summarizer sees no forced tool; the gate leaves the task budget unset; deferred tools are re-announced once.
- [x] 6.10 Gemini usage parts add up to the real prompt + output; a Gemini-shaped usage at 52% does not hit; reported usage calibrates the summarizer trim.
- [x] 6.11 The turn's prompt survives a summarizer trim, with and without a summary head; an in-loop compaction leaves one deferred-tools delta in the next request and none on later turns.
