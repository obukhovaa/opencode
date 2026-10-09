# Design: compaction-failure-backoff

## Root cause, measured

Phase 0 ran from an agent pod against the LiteLLM Bedrock route for `eu-claude-sonnet-5-5`, with the body shape opencode sends (`anthropic_version`, `anthropic_beta: [context-1m-2025-08-07]`, `max_tokens: 64000`, adaptive thinking, `output_config.effort: low`):

| # | Request | Result |
|---|---|---|
| R1 | One plain user text message | 200, stream completed |
| R2 | A `tool_use` + `tool_result` pair, no `tools` declared | 200, completed |
| R3 | R2 plus a `server_tool_use` / `tool_search_tool_result` pair | **Stream reset after 0.2–0.4 s with `INTERNAL_ERROR`, three out of three times.** The same body on the non-streaming `invoke` returns **400 `Tool reference 'bash' not found in available tools`** |
| R4 | An Opus-5.5-signed `thinking` block replayed to Sonnet | 200, completed |
| R5 | About 585K tokens of plain text, no tools | 200, completed (`input_tokens` 585,040) |

So the summarizer request failed on its **shape**, not its size: the tools-less request replayed the server tool-search blocks a `deferredTools` session carries, Bedrock answered 400, and LiteLLM turned the 400 into a stream reset on the streaming route. opencode's RST retry (3 retries, 0.5/1/2 s) then explains the 5–13 s per failed attempt seen in Langfuse. Neo therefore needs no config change: no `summarizerMaxInputTokens`, no summarizer model switch.

## Transcript, not a special case in the provider

The summarizer request has no tools, so nothing in a native tool block is actionable for it. Rendering the history as text in the agent (`summarizerTranscript`, `compaction_transcript.go`) removes every shape the upstream may reject — undeclared `tool_use`, undeclared server tool search, thinking signed by another model — for every provider at once, instead of teaching each `convertMessages` about a tools-less request. Each rendered message keeps its ID, role and `Synthetic` flag, so the existing trim still recognises the summary head, the turn's prompt and the tool pairs. Messages that render empty (reasoning-only assistant turns) are dropped.

After the trim the entries are collapsed into **one user message** (`summarizerRequest`): a `<transcript>` block with one `## role` section per entry, then the compaction prompt. One message is valid on every provider whatever the entries' roles — a tool result rendered as text has no role of its own to keep, a transcript headed by the previous summary would otherwise start the request with a non-user turn, and consecutive same-role turns need no provider-side merging.

Tool output dominates agent histories, so the per-payload cap (`summarizerToolPayloadMaxTokens`, 2,000 estimated tokens, head and tail on rune boundaries) is the main size bound; the trim is the backstop. The budget is `min(0.9 × summarizer window, summarizerMaxInputTokens)`, resolved as the compacting agent's registry value, else its `agents.<id>` entry, else the `summarizer` agent's entry — so one key on the summarizer bounds the route for every agent.

## Backoff

State lives in `compactionBackoffs` (`compaction_backoff.go`): a mutex-guarded map of per-session `compactionBackoff` on the agent, with an injectable clock for tests. It is deliberately in memory: a daemon's agent is long-lived, a flow step's agent lives for one step, and a process restart simply allows the next attempt. Only sessions with a failure on record have an entry — a success deletes it — so the map is bounded by the sessions currently backing off.

The gates in `processGeneration` call `compactionAllowed` before the trigger log and `autoCompact` for the attempt. `allow` returns true when there is no failure on record, or when the context has grown by 10% of the window since the last failure; otherwise it refuses while the failure happened in the current turn (`startTurn` counts turns per session) or while the wait (`min(2^(n-1) min, 30 min)`) has not passed. Growth therefore beats the same-turn rule as well as the wait: a turn that grows by a tenth of the window is the long tool loop that can overflow inside one turn, and a bounded extra attempt per 10% is cheaper than the overflow. A successful compaction resets the state and sets no per-turn flag, so a long tool loop can still compact twice when it legitimately grows past the threshold again. `Summarize` and `SummarizeSync` call the summarizer directly and never consult the backoff; their success resets it.

## Usage guard

`countContextTokens` uses `PromptTokens + CompletionTokens` as a floor only when it is at most the window and the floor is at most 1.5× the estimate of the same history. The estimate (`CountTokens`) covers the system prompt and the serialised tools, so the 1.5× margin is the ordinary 4 B/token undercount, not the system prompt. The WARN fires only when the ignored floor would have reached the threshold — the report that would have cost a summarizer call; below it the ignore is a DEBUG line, so small sessions do not log on every call. `TrackUsage` and Langfuse keep the reported value. `anthropicClient.usage` reads the SDK accumulator, which overwrites `message_start` usage with `message_delta`'s cumulative counts; a test pins that, so the doubling is not produced in-process. Datadog carried no LiteLLM data for the window, so the upstream source of the doubled `cache_read` is unproven; the guard covers it either way.

## Metadata

`provider.WithGenerationMetadata(ctx, map)` merges extra keys into the Langfuse generation metadata of calls made with ctx; built-in keys (`opencode_version`, `agent_id`) win. The summarizer paths attach the compaction stats there, so a failure is diagnosable without `telemetry.generations` input logging. `performSynchronousCompaction` returns the same stats so the failure WARN names the estimated input and whether it was likely an overflow of the summarizer's own window.
