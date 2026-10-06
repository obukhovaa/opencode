## 1. Summarizer transcript

- [x] 1.1 `summarizerTranscript`: text-only entries with ID, role and synthetic flag kept; tool calls, results and tool search as tagged lines; reasoning dropped; payloads over 2,000 estimated tokens cut head and tail on rune boundaries.
- [x] 1.2 `summarizerInput` trims the transcript; the reported-usage calibration still uses the raw history.
- [x] 1.3 `summarizerRequest`: one user message, transcript then prompt; used by `performSynchronousCompaction` and `Summarize`.
- [x] 1.4 `summarizerMaxInputTokens` on `config.Agent` and the registry (frontmatter, config overlay, builtins), negative values warned and zeroed, schema regenerated.

## 2. Failure backoff

- [x] 2.1 Per-session in-memory backoff: no retry in the failing turn, 1 min doubling to 30 min, 10% window growth overrides, success resets.
- [x] 2.2 Pre-turn and in-loop gates check it before the trigger log; `autoCompact` records the outcome.
- [x] 2.3 `SummarizeSync` and `Summarize` bypass it and reset it on success.

## 3. Usage guard

- [x] 3.1 `countContextTokens` ignores a report over the window or over 1.5x the estimate, with a warning.

## 4. Diagnostics

- [x] 4.1 `provider.WithGenerationMetadata` merges ctx metadata into the generation's metadata.
- [x] 4.2 The summarizer generation carries the `compaction.*` keys; a likely-overflow warning on a failed summarizer call.

## 5. Tests and docs

- [x] 5.1 Transcript, request shape, budget cap, backoff (unit and through `processGeneration`), usage guard, config and registry round trips, generation metadata.
- [x] 5.2 README Auto Compact section.
