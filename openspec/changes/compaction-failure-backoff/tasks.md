## 1. Summarizer transcript

- [x] 1.1 `summarizerTranscript`: text-only entries with ID, role and synthetic flag kept; tool calls, results (name from the call when missing) and tool search (query and findings) as tagged lines; reasoning dropped; payloads over 2,000 estimated tokens cut head and tail on rune boundaries.
- [x] 1.2 `summarizerInput` trims the transcript; the reported-usage calibration still uses the raw history.
- [x] 1.3 `summarizerRequest`: one user message, transcript then prompt; used by `performSynchronousCompaction` and `Summarize`.
- [x] 1.4 `summarizerMaxInputTokens` on `config.Agent` and the registry (frontmatter, config overlay, builtins), negative values warned and zeroed, schema regenerated.
- [x] 1.5 `resolveSummarizerMaxInputTokens`: registry value, else `agents.<id>`, else `agents.summarizer`.

## 2. Failure backoff

- [x] 2.1 Per-session in-memory backoff: no retry in the failing turn, 1 min doubling to 30 min, 10% window growth overrides both, success resets and drops the entry.
- [x] 2.2 Pre-turn and in-loop gates check it before the trigger log; `autoCompact` records the outcome.
- [x] 2.3 `SummarizeSync` and `Summarize` bypass it and reset it on success.

## 3. Usage guard

- [x] 3.1 `countContextTokens` ignores a report over the window or over 1.5x the estimate; WARN when the ignored floor would have fired, DEBUG otherwise.
- [x] 3.2 Test pinning single-counted stream usage in `anthropicClient.usage` (`message_delta` repeats cumulative counts).

## 4. Diagnostics

- [x] 4.1 `provider.WithGenerationMetadata` merges ctx metadata into the generation's metadata; built-in keys win.
- [x] 4.2 The summarizer generation carries the `compaction.*` keys; a likely-overflow warning on a failed summarizer call, automatic and manual.
- [x] 4.3 `performSynchronousCompaction` returns the input stats; the failure WARN names the agent, the estimated input and `likely_context_overflow`.

## 5. Tests and docs

- [x] 5.1 Transcript, request shape, pair-safe trim, budget precedence, backoff (unit and through `processGeneration`, including expiry and same-turn growth), usage guard, config and registry round trips, generation metadata.
- [x] 5.2 README Auto Compact section, AGENTS.md agent keys, `design.md` with the Phase 0 matrix.
