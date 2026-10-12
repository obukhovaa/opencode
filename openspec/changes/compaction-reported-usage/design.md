# Design: compaction decides on the reported context size

## Context

`countContextTokens` is the one place every auto-compaction decision gets its number: the pre-turn gate, the in-loop check before each model call, its re-count after a compaction, and the check on a non-interactive re-entry. Its result also feeds `AdjustMaxTokens` and the likely-overflow warning on a failed call.

`TrackUsage` stores the last call's usage on the session as `PromptTokens = input + cache_creation` and `CompletionTokens = output + cache_read`. Their sum is that call's whole prompt (system prompt and tools included) plus its output: the exact size of the history up to and including the assistant message the call produced. A compaction stores `PromptTokens = 0` and `CompletionTokens = ` the summary's output.

Measured on a daemon session (1M window, threshold 0.4): reported 756,968 against an estimate of 214,983; also 421K/94K, 545K/161K and 740K/213K on the same session. The estimate is 3.5-4.5x low on tool-heavy histories, so a guard that trusts a report only within 1.5x of it never trusts one.

## Decisions

### The count is the report plus the delta

`count = PromptTokens + CompletionTokens + estimate(msgs[anchor+1:])`. The anchor (`reportAnchor`) is the last message the report covers:

- normally the last non-synthetic assistant message. Synthetic assistant messages are background-task completions written after the reported call, so they belong to the delta;
- after a compaction (`PromptTokens == 0`, `CompletionTokens > 0`), the summary message. The report is the summary's output and everything after it, including an assistant message re-appended past the summary in the loop, is the delta.

The delta keeps the 4 bytes/token local estimate. It is a new user message or a turn's tool results, small next to the history, and the next response replaces it with a reported number. Its undercount is bounded by the delta, not by the history.

After a compaction the count misses the system prompt and tools, which the summary report does not include, until the next call reports. That call follows immediately, and a count right after a compaction only decides whether to log "still over the threshold".

Alternatives: keeping the estimate as a fallback floor (the shape that broke) or calibrating the estimate by the report ratio (still an estimate of a size the provider already measured). Neither adds information the report lacks.

### No report: skip the check

When `reportAnchor` finds nothing (no usage on the session, a report with no assistant message to anchor it, a compaction whose summary reported no output), the check never fires. The other option, the estimate for that one call, has one real case: a forked flow step, whose new session carries the parent's whole message log and no usage. But the estimate is 3.5x low on exactly those histories, so it would not have fired there either, and the first response gives the session a report that the in-loop check before the second call uses. A forked step whose inherited history already overflows the window fails its first call today and still does; copying the parent's usage to a forked session would close that and is left for its own change.

The local estimate of the history is still computed on this path, for `AdjustMaxTokens` and the overflow diagnostic, which need a number. It is the plain 4 bytes/token count of the messages and the serialisable tools, with no `count_tokens` request.

### The doubled cache read is judged against the previous report

The report that motivated the 1.5x guard had `cache_read = 1,383,037 = 690,908 + 692,129`: two calls' cache reads summed. Where the sum happens is not pinned down. The SDK accumulator overwrites `message_start` usage with `message_delta`'s cumulative counts (a test pins that), and a stream retry starts a fresh accumulator, so it is not summed in-process. LiteLLM has had Bedrock streaming bugs that double-count cache tokens (BerriAI/litellm#25517, and the invocation-metrics mapping in #34542), which makes the proxy the likely source, but neither matches this two-addend shape exactly.

The guard therefore uses the reports themselves. A call can read from cache only a prefix that an earlier call of the session sent, and the session's prompt only grows between compactions, so a real cache read is at most the previous call's prompt; the previous prompt plus output is the bound, with the output as margin. `plausibleCacheRead` caps a read above that bound, and a prompt above the context window, and logs a warning. The bound is used only when `PromptTokens > 0`: after a compaction the stored report is the summary alone, and the next call reads the system prompt and tools from cache, which that report does not contain.

Only the last-call fields are capped. The cost and `TotalPromptTokens` keep the reported values: they are what the upstream billed, and telemetry is unchanged.

### Logging

Every check logs one INFO `compaction check` line: `reported_tokens`, `delta_tokens`, `tokens`, `threshold_tokens`, `context_window`, `compact`. With no report it logs `compaction check: no reported usage yet` with the estimate. It is one line per model call, beside the existing INFO `Track usage` line, and makes the next check readable from logs alone.

## Risks / Trade-offs

- A session shared by flow steps of different agents takes the previous step's report, whose system prompt and tools differ from the current agent's. The error is the difference between two system prompts, tens of thousands of tokens at most, and the next call corrects it.
- A call that failed after an earlier success leaves the earlier report while a later assistant message may already be persisted, so the count misses what lies between. The next successful call reports the full size.
- The cap can cut a real cache read only if a different session sent a longer prompt with the same prefix than this session's previous call, which a growing history does not produce.

## Test plan

- `TestCountContextTokens`: the daemon's numbers (757K reported, 400K threshold) fire; under the threshold does not; no report never fires on the estimate; the post-compaction anchor; a synthetic completion counted in the delta; a zero window; Gemini cache counted once; the doubled read capped at the previous context; a plausible read kept; a prompt above the window capped.
- The `processGeneration` tests (pre-turn, in-loop, re-entry, deferred-tool re-announcement, backoff) now drive the size through responses' usage, and the test provider's `CountTokens` panics, so no decision can fall back to the estimate.
