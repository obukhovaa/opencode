# Design — Compact Before a Turn's First Model Call

## Context

- `processGeneration` (`internal/llm/agent/agent.go`) lists the session, filters from `SummaryMessageID`, injects the struct_output schema envelope, persists the user message, resolves the tool set, then enters the agentic loop. The loop's auto-compaction check runs before every model call except `cycles == 1`.
- `TrackUsage` stores `sess.PromptTokens = input + cache_creation` and `sess.CompletionTokens = output + cache_read`. Neither field alone is meaningful, but their SUM is the last call's full prompt + output — i.e. the history up to and including the last assistant message, as the provider counted it.
- After compaction `performSynchronousCompaction` stores `PromptTokens = 0`, `CompletionTokens = summary output tokens`.
- `RunOptions.CompactionThreshold` is set only by the flow runner (`internal/flow/service.go`, `struct_output_retry.go`) from `step.compact.threshold`.

## Goals / Non-goals

- Goal: a session over its threshold compacts before its next turn's first model call, on every entry point.
- Goal: the count cannot silently undercount a session the provider has already told us is large.
- Goal: a daemon agent can compact much earlier than 0.95 without a flow step.
- Goal: a session that already overflowed recovers on its next turn.
- Non-goal: the TUI post-turn check (stays; the gate is additive there).
- Non-goal: changing `provider.Provider` — the provider has no session access, and every stub provider in tests implements the interface.
- Non-goal: Slack `/compact` reachability (tracked separately).

## Key decisions

### 1. Gate before persisting the user message

The count input is the filtered history plus a transient, unpersisted user message built from the content and attachments (an auto-resume turn counts the history only). When compaction runs, the user message is persisted AFTER it, so seq order is `summary → user message` and the user turn is inside the post-summary window on every later reload. Persisting first and compacting second would put the user message before the summary, and `filterMessagesFromSummary` would drop it on the next reload.

`resolveTools()` moves above `createUserMessage` so the gate can count tool schemas; it has no data dependency on the persisted message. The deferred-tool delta injection stays after the gate.

After a successful gate compaction the history is rebuilt exactly as the in-loop path does (reload, filter from summary, filter empty user turns, re-inject the schema envelope, re-apply the task-budget remaining ctx). The task-budget block is factored into `withTaskBudgetRemaining` and shared. A failed compaction only warns: the same contract as the in-loop path.

The in-loop `cycles != 1` guard stays. Cycle 1 is now covered by the gate, and dropping the guard would summarize twice in a row when the summary itself is still over the threshold.

### 2. Floor the estimate by reported usage

`countContextTokens` returns `max(estimate, reported + tail)` where `reported = PromptTokens + CompletionTokens` and `tail` is the local 4 B/token estimate of the messages after the last assistant message. With no assistant message in the history there is nothing the reported usage describes, so it is ignored. The provider's own `hit` flag is discarded and recomputed against the final number. After compaction the reported value is only the summary's size, so the estimate wins — which is correct.

The floor overcounts slightly after a model switch or when messages were filtered out. That is the safe direction.

### 3. Per-agent threshold, flow step wins

`resolveCompactionThreshold(opts)`: `opts.CompactionThreshold > 0` → it; else `agents.<id>.compactionThreshold > 0` → it; else 0.95. Both overrides go through `effectiveCompactionThreshold` for clamping. `validateAgent` warns and zeroes an out-of-range value, so a typo inherits the default instead of disabling compaction or firing it on every turn. The key never enables compaction by itself; `autoCompact` still gates it.

### 4. Bounded summarizer input

`summarizerInput` is shared by `performSynchronousCompaction` and `Summarize`: list → filter from `SummaryMessageID` (prior summary kept as the head) → filter empty user turns. If the local estimate plus the summarizer's system prompt and the compaction prompt reaches 90% of the summarizer's window, the oldest messages are dropped until it fits. A cut never leaves an orphan tool result at the head (the same rule `filterMessagesFromSummary` applies after its boundary). One WARN reports how many were dropped. A window of 0 disables trimming.

### 5. Overflow diagnostic

`likelyContextOverflow(estimated, window)` is a pure helper (`window > 0 && estimated >= 0.9*window`). The two non-cancel error branches after `streamAndHandleEvents` (main call, max-turns final call) log a WARN with session, agent, estimate, window, ratio and the error when it returns true.

## Risks

- Double summarization: prevented by the `cycles != 1` guard.
- A stale outer-loop `session` after a compaction would re-include pre-summary messages on the non-interactive re-entry reload (the in-loop path used to shadow it). Both compaction sites now assign the outer `msgs` and `session`.
- `message.EstimateTokens` counted only text parts, so tool calls, tool results and reasoning were ~100 B each regardless of size — in an agentic session most of the history. It now counts their payloads. This raises the local estimate (compaction fires earlier, the safe direction) and is what makes the summarizer trim and the usage-floor tail meaningful for tool-heavy sessions.
