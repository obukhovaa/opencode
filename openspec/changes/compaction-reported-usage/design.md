# Design: compaction decides on the reported context size

## Context

`countContextTokens` is the one place every auto-compaction decision gets its number: the pre-turn gate, the in-loop check before each model call, its re-count after a compaction, and the check on a non-interactive re-entry. Its result also feeds `AdjustMaxTokens` and the likely-overflow warning on a failed call.

`TrackUsage` stores the last call's usage on the session as `PromptTokens = input + cache_creation` and `CompletionTokens = output + cache_read`. Their sum is that call's whole prompt (system prompt and tools included) plus its output: the exact size of the history up to and including the assistant message the call produced. A compaction stores `PromptTokens = 0` and `CompletionTokens = ` the summary's output.

Measured on a daemon session (1M window, threshold 0.4): reported 756,968 against an estimate of 214,983; also 421K/94K, 545K/161K and 740K/213K on the same session. The estimate is 3.5-4.5x low on tool-heavy histories, so a guard that trusts a report only within 1.5x of it never trusts one.

## Decisions

### The count is the report plus the delta

`count = PromptTokens + CompletionTokens + estimate(msgs[anchor+1:])`. The anchor (`reportAnchor`) is the last message the report covers:

- normally the last assistant message whose call completed. Synthetic assistant messages are background-task completions written after the reported call, so they belong to the delta. So does the message of a call that failed (finished `error`) or never finished (a crash, a failed max-turns wrap-up): it reports nothing. Anchored on it, the tool results before it would drop out of every later count, and a call that failed *because of* its size never reports, so the session would fail every turn without compacting. A cancelled call stays an anchor: one cancelled during its tools was reported, and one cancelled while streaming was not, but a cancel is no sign of size and the next call reports. Recording the reported message's id would be exact, but it needs either a column or per-process state that a restart and every other agent on the session lose; the finish reason is in the history already.
- after a compaction (`PromptTokens == 0`, `CompletionTokens > 0`), the summary message. The report is the summary's output, and everything after it is the delta, including an assistant message re-appended past the summary in the loop.

The delta keeps the 4 bytes/token local estimate for text, tool calls and tool results. It is a new user message or a turn's tool results, small next to the history, and the next response replaces it with a reported number; its undercount is bounded by the delta, not by the history. Media is priced the way a provider bills it, not by `EstimateTokens`, which counts an attachment as 100 bytes of overhead and a `view_image` result's base64 as text: an image is 1,600 tokens (Anthropic bills width×height/750, ~1.6K at its 1,568px cap), any other attachment 100 bytes per token with 1,600 as the floor (the `count_tokens` fallback's rule). A 4 MB PDF attached to a session near the threshold is then visible to the pre-turn gate, and one screenshot no longer counts as 250K tokens.

After a compaction the count misses the system prompt and tools, which the summary report does not include, until the next call reports. That call follows immediately, and a count right after a compaction only decides whether to log "still over the threshold".

Alternatives: keeping the estimate as a fallback floor (the shape that broke) or calibrating the estimate by the report ratio (still an estimate of a size the provider already measured). Neither adds information the report lacks.

### No report: the provider counts, for that check only

When `reportAnchor` finds nothing (no usage on the session, a report with no assistant message to anchor it, a compaction whose summary reported no output), the check asks the provider (`CountTokens`: the `count_tokens` endpoint, or its local estimate when the endpoint is unavailable) and decides on that. Skipping the check instead has real failures:

- a forked flow step copies its parent's whole message log, including everything before the parent's summary, and no usage. Its first call can be over the window; it fails, reports nothing, and every retry reuses the same session, so with no count it never compacts. With a working endpoint the count is exact; with the local estimate it undercounts, so it fires only where the real size is over the threshold too;
- a provider that never reports usage (an OpenAI-compatible server that ignores `include_usage`) would never compact at all.

The cost is one count request on a check without a report, which for a fresh session is its first model call only. This is the "estimate for that one call" the ticket allows, for a real case; it never overrides a report.

### A doubled report is the sum of a response's sampling passes, recorded as the last pass

The report that motivated the 1.5x guard had `cache_read = 1,383,037 = 690,908 + 692,129`. The source is pinned down now: a server-side tool search. The SDK documents the top-level usage as cumulative over "model sampling iterations, such as the turns of a server-side tool use loop" and says to "calculate the context window size from the last `message` entry" of `usage.iterations`. A probe on the LiteLLM Bedrock route Neo uses, one request with a deferred tool the model had to search for over a 9.6K prompt, returned `server_tool_use`, `tool_search_tool_result`, `tool_use` with:

| cache | input | cache_creation | cache_read | output |
|---|---|---|---|---|
| cold | 355 | 9,612 | 9,612 | 108 |
| warm | 355 | 0 | 19,224 | 108 |

Pass one wrote (or read) the prefix and pass two read it again. Both shapes are about twice the prompt, the warm one exactly the GENAI-414 shape, and the cold one doubles through cache creation, which a bound on the cache read alone would miss. The route sends no `usage.iterations`. The SDK accumulator is not involved: it overwrites `message_start` usage with `message_delta`'s cumulative counts, and a test pins that.

So the provider reports the last pass as `TokenUsage.LastPass`, and `TrackUsage` records it as the last-call usage:

- from `usage.iterations` (the last `message` entry) when an upstream sends it;
- otherwise from the response's content: one pass plus one per run of server tool results the model sampled after (a run ending the content, a `pause_turn`, has no pass after it). Each pass re-reads the whole prompt, so the input-side counts divided by the passes are the last pass's prompt to within the server tool results; the output stays whole, since every pass's output is in the message the next request sends. That gives exactly 9,612 for both rows above.

The cost and `TotalPromptTokens` keep the billed sum, and telemetry is unchanged.

The previous call's report is no bound. The MR first capped a cache read at the previous call's prompt plus output, but a call legitimately reads more than that whenever an unreported request of the same session cached a longer prefix: a stream retried after a stall or a transport error once its prompt was cached, a call cancelled or failed after its prompt was processed, an agent switch onto a prefix another session cached. The capped value then became the next call's bound, so a 40K undercount drained only by one call's output per call, tens of calls during which compaction ran late. A ratio test against the previous report has the same flaw unless it keeps uncapped reports in memory. The summed passes are recognisable in the response itself, so nothing about the previous report is needed.

A prompt above the context window is impossible for one pass whatever produced it, so `TrackUsage` records it at the window (cache read lowered first, then cache creation, then input) with a warning. At the window the session compacts, the safe reading of a size known to be wrong.

### max_tokens is lowered per call

`AdjustMaxTokens` halved the client's current `max_tokens` and set it on the provider, which every session of the agent shares, and nothing restored it. The 3.5x-low estimate kept it from firing; the real count reaches the band below the threshold where the count plus `max_tokens` overflows the window (872K-950K on a 1M window with 128K output at the default 0.95), so one long session would have left every later session of the process at 32K or less. It now halves from the configured value on every call and sets the result only when it changes.

### Logging

Every check logs one `compaction check` line: `reported_tokens`, `delta_tokens`, `tokens`, `threshold_tokens`, `context_window`, `over_threshold`; without a report, the provider's count. `over_threshold` is not the decision: the cycle-one skip, the backoff and a disabled auto-compaction come after it. So with `autoCompact` off the line is DEBUG (the count then only sizes `max_tokens`), and the backoff's "auto-compaction skipped" moves from DEBUG to INFO; it fires only over the threshold. `Track usage` gains the session id and `summed_passes`.

## Risks / Trade-offs

- A session shared by flow steps of different agents takes the previous step's report, whose system prompt and tools differ from the current agent's. The error is the difference between two system prompts, tens of thousands of tokens at most, and the next call corrects it.
- A call cancelled while streaming leaves the earlier report while its message is the anchor, so the next check misses the tool results the cancelled call carried. The next call reports the full size, and a cancel is not caused by size.
- A call that failed in its tool phase after its report is skipped as an anchor, so its content counts twice until the next call reports; that errs toward compacting.
- Dividing summed passes assumes each pass re-reads the whole prompt; it undercounts the last pass by about the server tool results (a search result is a few hundred tokens).
- On a check without a report and without a working count endpoint, the local estimate decides; it undercounts, so a forked step can still fail its first call. The flow fork copying the parent's summary boundary and report is its own change.

## Test plan

- `TestAnthropicUsage_ToolSearchReportsTheLastPass`: the probe's cold and warm usage, per-pass iterations, two searches in one pass, a search the turn paused on, a single pass.
- `TestCountContextTokens`: the daemon's numbers (757K reported, 400K threshold) fire; under the threshold does not; no report counted by the provider under and over the threshold; the post-compaction anchor; a synthetic completion, a failed call and an unfinished call in the delta; a cancelled call as the anchor; attachments and an image tool result priced as media; a zero window; Gemini cache counted once; a tool search's summed usage recorded as its last pass; a cache read above the previous report kept; the first call after a compaction; a prompt above the window. Each case also asserts the provider is asked for a count exactly when no report covers the history.
- `TestProcessGeneration_FailedCallDoesNotHideItsHistory`: a reported call, a large tool result, a failed call; the next turn compacts before calling.
- `TestAdjustMaxTokens_HalvingDoesNotStick`.
- The `processGeneration` tests (pre-turn, in-loop, re-entry, deferred-tool re-announcement, backoff, waiting for an in-flight summarize) drive the size through reports; the in-flight test now seeds an over-threshold report so a turn that did not wait would compact.
- Each fix is pinned by a mutant the tests kill: anchoring on any assistant, the plain estimate for the delta, a no-report count that never fires, ignoring `LastPass`, skipping cancelled calls.
