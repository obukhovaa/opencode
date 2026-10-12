# Design: compaction decides on the reported context size

## Context

`countContextTokens` is the one place every auto-compaction decision gets its number: the pre-turn gate, the in-loop check before each model call, its re-count after a compaction, and the check on a non-interactive re-entry. Its result also feeds `AdjustMaxTokens` and the likely-overflow warning on a failed call.

`TrackUsage` stores the last call's usage on the session as `PromptTokens = input + cache_creation` and `CompletionTokens = output + cache_read`. Their sum is that call's whole prompt (system prompt and tools included) plus its output: the exact size of the history up to and including the assistant message the call produced. A compaction stores `PromptTokens = 0` and `CompletionTokens = ` the summary's output.

Measured on a daemon session (1M window, threshold 0.4): reported 756,968 against an estimate of 214,983; also 421K/94K, 545K/161K and 740K/213K on the same session. The estimate is 3.5-4.5x low on tool-heavy histories, so a guard that trusts a report only within 1.5x of it never trusts one.

## Decisions

### The count is the report plus the delta

`count = PromptTokens + CompletionTokens + estimate(msgs[anchor+1:])`. The anchor (`reportAnchor`) is the last message the report covers.

- **The message of the reported call.** Once `TrackUsage` has recorded a completed call's usage, `processEvent` marks the call's finish part `reported` (`Finish.Reported`, a JSON field of the stored part, so no migration), and `AddFinish` keeps the mark when a later finish replaces the reason: a call cancelled during its tools was reported. The anchor is the last non-synthetic assistant message with the mark. Everything else after it is delta: synthetic messages (background-task completions written after the call), and the message of a call that failed, was cancelled while streaming, or never finished (a crash, a failed max-turns wrap-up). Anchored on such a message, the tool results before it would drop out of every later count, and a call that failed *because of* its size never reports, so the session would fail every turn without compacting.
- **Messages from before the mark.** A row written by an earlier version has no mark; for it the finish reason decides: anything but `error` or `canceled` counts as reported. A cancelled legacy message is not trusted, which at worst counts one call's content twice on the first turn after the upgrade. Recording the reported message's id on the session instead would need a column; process memory would be lost on a restart and by every other agent on the session.
- **After a compaction** (`PromptTokens == 0`, `CompletionTokens > 0`), the anchor is the summary message. The report is the summary's output, and everything after it is the delta, including an assistant message re-appended past the summary in the loop.

The delta keeps the 4 bytes/token local estimate for text, tool calls and tool results. It is a new user message or a turn's tool results, small next to the history, and the next response replaces it with a reported number; its undercount is bounded by the delta, not by the history. Media is priced the way the request carries it, not by `EstimateTokens`, which counts an attachment as 100 bytes of overhead and a `view_image` result's base64 as text:

- an attachment through `provider.AttachmentTokens`, which follows the Anthropic and OpenAI converters (`convertBinaryContent`, `convertBinaryContentOpenAI`, sharing `attachmentMIME`): an image at 1,500 tokens (`count_tokens`' fallback price), a PDF at 100 bytes per token with 1,500 as the floor, a text attachment as the text it is inlined as, and audio, video or an archive as the one-line note that replaces it. A 4 MB PDF or a 360 KB log attached near the threshold is then visible to the pre-turn gate, and a 19 MB voice note no longer compacts a small session;
- an image tool result at 1,500 tokens only where the provider sends it as an image (`SendsToolResultImages`: the Anthropic client, directly, on Bedrock or on Vertex); the OpenAI and Gemini clients send its base64 JSON as text, which costs its length.

After a compaction the count misses the system prompt and tools, which the summary report does not include, until the next call reports. That call follows immediately, and a count right after a compaction only decides whether to log "still over the threshold". The summarizer's input calibration (`summarizerInput`) compares the report with the estimate of the history up to the same anchor.

Alternatives: keeping the estimate as a fallback floor (the shape that broke) or calibrating the estimate by the report ratio (still an estimate of a size the provider already measured). Neither adds information the report lacks.

### No report: the provider counts, for that check only

When `reportAnchor` finds nothing (no usage on the session, a report with no assistant message to anchor it, a compaction whose summary reported no output), the check asks the provider (`CountTokens`: the `count_tokens` endpoint, or its local estimate when the endpoint is unavailable) and decides on that. Skipping the check instead has real failures:

- a forked flow step copies its parent's whole message log, including everything before the parent's summary, and no usage. Its first call can be over the window; it fails, reports nothing, and every retry reuses the same session, so with no count it never compacts. With a working endpoint the count is exact; with the local estimate it undercounts, so it fires only where the real size is over the threshold too;
- a provider that never reports usage (an OpenAI-compatible server that ignores `include_usage`) would never compact at all.

The cost is one count request on a check without a report, which for a fresh session is its first model call only. This is the "estimate for that one call" the ticket allows, for a real case; it never overrides a report.

### A doubled report is the sum of a response's sampling passes, recorded as the last pass

The report that motivated the 1.5x guard had `cache_read = 1,383,037 = 690,908 + 692,129`. The source is pinned down now: a server-side tool search. The SDK documents the top-level usage counts as cumulative, and its (beta) `usage.iterations` as one entry per sampling pass, "such as the turns of a server-side tool use loop", from whose last `message` entry to "calculate the context window size". A probe on the LiteLLM Bedrock route Neo uses, one request with a deferred tool the model had to search for over a 9.6K prompt, returned `server_tool_use`, `tool_search_tool_result`, `tool_use` with:

| cache | input | cache_creation | cache_read | output |
|---|---|---|---|---|
| cold | 355 | 9,612 | 9,612 | 108 |
| warm | 355 | 0 | 19,224 | 108 |

Pass one wrote (or read) the prefix and pass two read it again. Both shapes are about twice the prompt, the warm one exactly the GENAI-414 shape, and the cold one doubles through cache creation, which a bound on the cache read alone would miss. The route sends no `usage.iterations`. The SDK accumulator is not involved: it overwrites `message_start` usage with `message_delta`'s cumulative counts, and a test pins that.

So the provider reports the last pass as `TokenUsage.LastPass`, and `TrackUsage` records it as the last-call usage:

- from `usage.iterations` (the last `message` entry) when an upstream sends it;
- otherwise from the response's content: one pass plus one per run of server tool results the model sampled after (a run ending the content, a `pause_turn`, has no pass after it). Each pass re-reads the whole prompt, so the input-side counts divided by the passes are the last pass's prompt to within the server tool results; the output stays whole, since every pass's output is in the message the next request sends. That gives exactly 9,612 for both rows above;
- on a stream, only once a `message_delta` has reported the cumulative input counts. A stream cut off before it (the truncated-response path), or an upstream whose `message_delta` carries the output only, leaves `message_start`'s counts: one pass's prompt already, which dividing would halve.

The cost, `TotalPromptTokens` and telemetry keep the billed sum. `CalculateCost` still judges the long-context price tier on that sum, which errs high when a summed report crosses the threshold no pass did; whether Anthropic tiers a multi-pass response per pass is not documented, and an undercounted cost is the worse error.

The previous call's report is no bound. The MR first capped a cache read at the previous call's prompt plus output, but a call legitimately reads more than that whenever an unreported request of the same session cached a longer prefix: a stream retried after a stall or a transport error once its prompt was cached, a call cancelled or failed after its prompt was processed, an agent switch onto a prefix another session cached. The capped value then became the next call's bound, so a 40K undercount drained only by one call's output per call, tens of calls during which compaction ran late. A ratio test against the previous report has the same flaw unless it keeps uncapped reports in memory. The summed passes are recognisable in the response itself, so nothing about the previous report is needed.

A prompt above the context window is impossible for one pass whatever produced it, so `TrackUsage` records it at the window (cache read lowered first, then cache creation, then input) with a warning. At the window the session compacts, the safe reading of a size known to be wrong.

### max_tokens is lowered per call

`AdjustMaxTokens` halved the client's current `max_tokens` and set it on the provider, which every session of the agent shares, and nothing restored it. The 3.5x-low estimate kept it from firing on routes without a working `count_tokens`; the real count reaches the band below the threshold where the count plus `max_tokens` overflows the window (872K-950K on a 1M window with 128K output at the default 0.95), so one long session would have left every later session of the process at 32K or less. It now halves from the configured value on every call whose count is under the window, and sets the result only when it changes. On Bedrock the wrapper's `setMaxTokens` never reached the child client that builds the requests, so the halving never applied there; it now goes to the child.

### Logging

Every check logs one `compaction check` line: `reported_tokens`, `delta_tokens`, `tokens`, `threshold_tokens`, `context_window`, `over_threshold`; without a report, the provider's count. `over_threshold` is not the decision: the cycle-one skip, the backoff and a disabled auto-compaction come after it. So with `autoCompact` off the line is DEBUG (the count then only sizes `max_tokens`), and the backoff's "auto-compaction skipped" moves from DEBUG to INFO ("Backoff decisions are logged" is modified accordingly); it fires only over the threshold. `Track usage` gains the session id and `summed_passes`.

## Risks / Trade-offs

- A session shared by flow steps of different agents takes the previous step's report, whose system prompt and tools differ from the current agent's. The error is the difference between two system prompts, tens of thousands of tokens at most, and the next call corrects it.
- The Gemini client sends every attachment inline, so audio and video there are priced as the note the other converters substitute and undercounted until the next report; on Kimi (no document blocks) a PDF is priced by size though it is sent as a note, which errs toward compacting. Neither is a production route of this feature.
- Dividing summed passes assumes each pass re-reads the whole prompt; it undercounts the last pass by about the server tool results (a search result is a few hundred tokens). Parallel server calls in one pass are assumed to come back as one run of results.
- On a check without a report and without a working count endpoint, the local estimate decides; it undercounts, so a forked step can still fail its first call. The flow fork copying the parent's summary boundary and report is its own change.
- The pre-turn gate runs before the struct_output schema envelope, the peer note and the deferred-tools announcement are added, as it did before, so it does not count them; the in-loop checks do.

## Test plan

- Provider: `TestAnthropicUsage_ToolSearchReportsTheLastPass` (the probe's cold and warm usage, iterations, parallel searches, a paused turn, a single pass); `TestAnthropicStreamUsage_LastPass` over Anthropic SSE and the Bedrock event stream (cumulative `message_delta`, iterations, output-only `message_delta` and a cut-off stream leave `LastPass` nil); `TestSamplingPasses`; `TestAttachmentTokens`; `TestSendsToolResultImages`; `TestBedrockMaxTokensReachTheChild`; `TestAdjustMaxTokens_HalvingDoesNotStick`.
- Message: `TestFinishReportedSurvivesRefinishAndStorage`.
- `TestCountContextTokens`: the daemon's numbers (757K reported, 400K threshold) fire; under the threshold does not; no report counted by the provider under and over the threshold; the post-compaction anchor; a synthetic completion, a failed call, an unfinished call and a call cancelled while streaming in the delta; a call cancelled during its tools as the anchor; attachments priced as the converters send them; an image tool result as an image or as text by provider; a zero window; Gemini cache counted once; a tool search's summed usage recorded as its last pass; a cache read above the previous report kept; the first call after a compaction; a prompt above the window. Each case also asserts the provider is asked for a count exactly when no report covers the history.
- `TestProcessGeneration_FailedCallDoesNotHideItsHistory`: a reported call, a large tool result, a failed call; the next turn compacts before calling, and the call that completed is marked reported.
- The `processGeneration` tests (pre-turn, in-loop, re-entry, deferred-tool re-announcement, backoff, waiting for an in-flight summarize) drive the size through reports; the in-flight test seeds an over-threshold report so a turn that did not wait would compact.
- Each fix is pinned by a mutant the tests kill: anchoring on any assistant, the plain estimate for the delta, a no-report count that never fires, ignoring `LastPass`, trusting a cancel, a finish that drops the mark, no mark set, dividing a stream's one-pass counts, flat-pricing image results everywhere, attachments as overhead, Bedrock's wrapper-only `max_tokens`.
