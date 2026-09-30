## 1. Per-agent threshold

- [ ] 1.1 Add `CompactionThreshold float64 json:"compactionThreshold,omitempty"` to `config.Agent`; `validateAgent` warns and zeroes a value outside (0, 1].
- [ ] 1.2 `internal/agent/registry.go`: yaml frontmatter `compactionThreshold`, merged at the same sites as `taskBudget`.
- [ ] 1.3 `cmd/schema/main.go`: `compactionThreshold` on both agent definitions; regenerate `opencode-schema.json`.
- [ ] 1.4 `resolveCompactionThreshold(opts)`: flow step > agent > default; replace the `effectiveCompactionThreshold(opts.CompactionThreshold)` call sites.
- [ ] 1.5 Document in `AGENTS.md`, `README.md` and the `RunOptions.CompactionThreshold` comment.

## 2. Token floor

- [ ] 2.1 `countContextTokens(ctx, sessionID, threshold, msgs, toolSet)`: `max(estimate, PromptTokens+CompletionTokens+tail)`; hit recomputed against the window; Info log when the floor beats the estimate by >10%.
- [ ] 2.2 Use it at the pre-turn gate, the in-loop check and the post-compaction recount.

## 3. Pre-turn gate

- [ ] 3.1 Move `resolveTools()` above `createUserMessage`.
- [ ] 3.2 Count history + transient user message; compact when over threshold and `autoCompact`; rebuild history; persist the user message after the summary.
- [ ] 3.3 Factor the task-budget-remaining block into a helper shared by the gate and the in-loop path.

## 4. Summarizer input

- [ ] 4.1 `summarizerInput(ctx, sessionID, prompt)`: start after `SummaryMessageID`, trim oldest to 90% of the summarizer window without orphaning tool results, WARN on trim.
- [ ] 4.2 Use it from `performSynchronousCompaction` and `Summarize`.

## 5. Overflow diagnostic

- [ ] 5.1 `likelyContextOverflow(estimated, window)` and the WARN in the two non-cancel error branches after `streamAndHandleEvents`.

## 6. Tests

- [ ] 6.1 Gate compacts before the first `StreamResponse`; summary precedes the persisted user message; no compaction under the threshold.
- [ ] 6.2 `countContextTokens` floor / no-assistant / post-compaction / zero window.
- [ ] 6.3 `resolveCompactionThreshold` precedence.
- [ ] 6.4 `viper.Unmarshal` round-trip of `agents.<name>.compactionThreshold`; validation zeroes invalid values.
- [ ] 6.5 Registry frontmatter merge.
- [ ] 6.6 `summarizerInput` starts at the summary, trims oldest, never orphans tool results.
- [ ] 6.7 `likelyContextOverflow` boundaries.
- [ ] 6.8 `make schema-check`, `go test ./cmd/schema/...`, `make test`.
