# Design — Compact Before a Turn's First Model Call

## Context

- `processGeneration` (`internal/llm/agent/agent.go`) lists the session, filters from `SummaryMessageID`, injects the struct_output schema envelope, persists the user message, resolves the tool set, then enters the agentic loop. The loop's auto-compaction check runs before every model call except `cycles == 1`.
- `TrackUsage` stores `sess.PromptTokens = input + cache_creation` and `sess.CompletionTokens = output + cache_read`. Neither field alone is meaningful, but their SUM is the last call's full prompt + output — i.e. the history up to and including the last assistant message, as the provider counted it.
- After compaction `performSynchronousCompaction` stores `PromptTokens = 0`, `CompletionTokens = summary output tokens`.
- `RunOptions.CompactionThreshold` is set only by the flow runner (`internal/flow/service.go`, `struct_output_retry.go`) from `step.compact.threshold`.
- An auto-resume turn (`Run` with no content, from `taskDeps.ResumeSession`) is driven by the synthetic `Assistant(ToolCall)` + `Tool(ToolResult)` pair `task.EnqueueTaskCompletion` wrote at the end of the history. A non-interactive run that drains background tasks re-enters the loop with `cycles` reset to 0 and the drained pairs at the end of the reloaded history.
- The TUI starts an async `Summarize` after a turn that ended at >= 95% of the window. It does not take the session slot; its cancel func sits in `activeRequests` under `sessionID + "-summarize"`.

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

After a successful gate compaction the history is rebuilt as the in-loop path does (reload, filter from summary, filter empty user turns; the schema envelope is injected afterwards anyway). The task-budget remaining ctx is NOT applied at the gate: the turn has not started, so it gets the full budget, as a turn that does not compact does. `withTaskBudgetRemaining` is used by the in-loop path only. A failed compaction only warns: the same contract as the in-loop path.

On an auto-resume turn the gate captures the trailing synthetic messages before compacting (`syntheticTail`: walk back over `Synthetic` messages, start on an assistant so no tool result is orphaned) and re-appends them in memory after the reload, as `preserveTail` does in the loop. The summary lands after the pair, so without this the model would see only the summary instead of the completion it has to react to.

If a `Summarize` of the session is in flight on this agent, the gate waits for it (polling `activeRequests` every 100 ms, up to 2 minutes, ending early on ctx), reloads the history from the summary it wrote, and only then counts. Otherwise the gate would compact the same history concurrently and write a second summary.

The in-loop guard becomes `cycles != 1 || outerCycles > 1`. The first call of the first outer cycle is covered by the gate, and checking it again would summarize twice in a row when the summary itself is still over the threshold. The first call of a non-interactive re-entry has no gate in front of it, and the drained completions can push the history over, so it is checked. When it compacts there is no preserved tail or user turn to re-append; the drained pairs are carried over with `syntheticTail`, as at the gate.

`performSynchronousCompaction` clears the forced-tool signal (`provider.ForceStructOutputToolKey` set to `""`) on the summarizer ctx. The flow runner's struct_output rescue run forces `tool_choice` on the turn's ctx, and the summarizer request carries no tools, so a forced choice would be rejected.

A compaction moves the deferred-tools delta message (the announcement of deferred MCP tools) before the summary, out of the model's view. `performSynchronousCompaction` (pre-turn gate, in-loop check, `SummarizeSync`) and `Summarize` clear the session's in-memory announced set after writing the summary, and the delta's history dedup scans only the messages from `SummaryMessageID` on, so the next turn announces the tools again, once. A compaction at the gate is followed by the turn's own announcement. The in-loop rebuild calls `injectDeferredDelta` itself, right after it re-injects the schema envelope and before the preserved tail is appended: a flow step, a task subagent or a non-interactive ACP run is usually one turn, and without it the deferred MCP tools would stay out of view for the rest of the run.

### 2. Floor the estimate by reported usage

`countContextTokens` returns `max(estimate, reported + tail)` where `reported = PromptTokens + CompletionTokens` and `tail` is the local 4 B/token estimate of the messages after the last assistant message. With no assistant message in the history there is nothing the reported usage describes, so it is ignored. The provider's own `hit` flag is discarded and recomputed against the final number. After compaction the reported value is only the summary's size, so the estimate wins — which is correct.

The floor overcounts slightly after a model switch or when messages were filtered out. That is the safe direction.

### 3. Per-agent threshold, flow step wins

`resolveCompactionThreshold(opts)`: `opts.CompactionThreshold > 0` → it; else `agents.<id>.compactionThreshold > 0` → it; else 0.95. Both overrides go through `effectiveCompactionThreshold` for clamping. `validateAgent` warns and zeroes an out-of-range value, so a typo inherits the default instead of disabling compaction or firing it on every turn. The key never enables compaction by itself; `autoCompact` still gates it.

For a markdown-defined agent the key belongs in the frontmatter. A JSON `agents.<id>` entry without `model` is given the default model and its `maxTokens` by `validateAgent`, and `applyConfigOverrides` copies them over the frontmatter values, so a JSON entry added only for the threshold silently switches the agent's model. Such an entry must repeat the agent's `model` (and `maxTokens`, if set). The docs say so.

### 4. Bounded summarizer input

`summarizerInput` is shared by `performSynchronousCompaction` and `Summarize`: list → filter from `SummaryMessageID` (prior summary kept as the head) → filter empty user turns. If the local estimate plus the summarizer's system prompt and the compaction prompt reaches 90% of the summarizer's window, the oldest messages are dropped until it fits. A cut never leaves an orphan tool result at the head (the same rule `filterMessagesFromSummary` applies after its boundary). One WARN reports how many were dropped. A window of 0 disables trimming.

The 4 B/token estimate can undercount a session by a wide margin, and trimming on it alone would leave an overflowed session's summarizer input over the window. So the budget is calibrated: when the session's reported usage (`PromptTokens + CompletionTokens`) exceeds the local estimate of the history up to the last assistant message, the budget is scaled by `local / reported` before trimming. The reported value also includes the main agent's system prompt and tools, so this errs toward trimming more. Recovery is still estimate-based: a summarizer whose tokenizer counts far more than the main model's can still reject the input.

The cut keeps the turn's prompt. A flow step's task is the turn's user message, the oldest message of the turn, so an oldest-first cut drops it before anything else: with no prior summary it is `msgs[0]`, and after one it goes as soon as the turn's own history passes the budget. The in-loop rebuild re-appends only the last tool call and its result (as before this change), so a summary written without the task would lose it for the rest of the step. Before this change the summarizer got the full log, so the task was always in it; the calibrated budget makes the trim fire far more often (with a 1M-window main model and a 200K-window summarizer it drops most of every flow-step compaction). `turnPromptIndex` picks the latest user message that is not synthetic and has text (the schema envelope and the deferred-tools delta are synthetic). When the cut passes it, `trimSummarizerInput` keeps it between the head and the omission note, and it stays counted in the budget. A prompt that alone takes more than half the budget is dropped like any other message, so it cannot crowd out the recent history.

### 5. Overflow diagnostic

`likelyContextOverflow(estimated, window)` is a pure helper (`window > 0 && estimated >= 0.9*window`). The two non-cancel error branches after `streamAndHandleEvents` (main call, max-turns final call) log a WARN with session, agent, estimate, window, ratio and the error when it returns true.

## Risks

- Double summarization: prevented by the `cycles != 1 || outerCycles > 1` guard, and against the TUI's async `Summarize` by the gate's wait.
- A stale outer-loop `session` after a compaction would re-include pre-summary messages on the non-interactive re-entry reload (the in-loop path used to shadow it). Both compaction sites now assign the outer `msgs` and `session`.
- Gemini usage double-counted cached tokens: `PromptTokenCount` includes `CachedContentTokenCount`, yet both went into the `TokenUsage` (`InputTokens` and `CacheReadTokens`). The reported-usage floor would have read a mostly-cached Gemini session at nearly twice its size, and the cost counted the cached prefix at both rates. `geminiClient.usage` now sets `InputTokens = max(PromptTokenCount - CachedContentTokenCount, 0)`, as `openai.go` does.
- `message.EstimateTokens` counted only text parts, so tool calls, tool results and reasoning were ~100 B each regardless of size — in an agentic session most of the history. It now counts their payloads. This raises the local estimate (compaction fires earlier, the safe direction) and is what makes the summarizer trim and the usage-floor tail meaningful for tool-heavy sessions.
