# Compaction

OpenCode keeps long sessions inside the model's context window by summarizing them. This page covers automatic compaction (`autoCompact`), its per-agent tuning (`compactionThreshold`, `summarizerMaxInputTokens`) and agent-requested self-compaction (`selfCompact`). The normative requirements live in `openspec/specs/auto-compaction/spec.md` and `openspec/specs/agent-self-compaction/spec.md`.

## Auto compact

When enabled (default), automatically summarizes conversations approaching the context window limit (95%) and continues in a new session.

```json
{ "autoCompact": true }
```

The check runs before every model call: before a turn's first one, before each later call of its tool-use loop, and before the first call after a non-interactive run re-enters the loop for drained background tasks. A long-lived session (chat bridge, `opencode serve`, cron heartbeats) therefore compacts before the turn that would overflow it. On an auto-resume turn the background-task completion it reacts to is kept after the summary. The context size is the usage the provider reported for the session's last completed model call (its whole prompt, system prompt and tools included, plus its output) and a local estimate of only the messages added since: the new user message, tool results, background-task completions, and the messages of a call that failed or was cancelled before it completed, which reports nothing. Text counts at 4 bytes/token, and an attachment as the Anthropic and OpenAI clients send it: an image at 1,500 tokens, a PDF by its size, a text file as its text, other media as the short note that replaces it. No estimate of the history a report covers takes part. A history no report covers (a forked flow step that inherited its parent's log, the first call after a compaction whose summary reported no usage, a provider that reports no usage) is counted by the provider's `count_tokens`, or its local estimate, for that check. A response that ran a server-side tool search sampled more than once and reports every pass's tokens together, about twice the prompt for one search; the session records the last pass as its context (from `usage.iterations` when sent, otherwise the input-side counts divided by the passes), and a prompt above the window at the window, with a WARN. The cost and the session totals keep the billed counts. `max_tokens` is lowered from its configured value on each call whose count leaves too little room (Bedrock included), and is back to the configured value on the next call that has room. Every check logs a `compaction check` line naming the reported tokens, the delta, the threshold and `over_threshold` (DEBUG when `autoCompact` is off); a compaction the backoff skips logs INFO `auto-compaction skipped`.

### Failure backoff

When a compaction fails, the session does not retry for the rest of that turn, and then waits 1 minute, doubling per consecutive failure up to 30 minutes. Growth of 10% of the window since the failure overrides both the wait and the same-turn rule, so a session heading for overflow still gets another try. A success resets the backoff, and a manual `/compact` ignores it. Each failure logs a WARN with the failure count, the next retry time, the estimated summarizer input and the error. The summarizer's Langfuse generation carries `compaction.trigger` (`pre_turn`, `in_loop`, `overflow_retry`, `manual`), `compaction.estimated_input_tokens`, `compaction.messages`, `compaction.trimmed_messages`, `compaction.truncated_tool_payloads` and `compaction.failures`, so a failure can be read without logging the input.

### Overflow retry

When a model call fails because the request was too long anyway — the provider says so (`prompt is too long`, `Input is too long`, `context_length_exceeded`, `maximum context length`, …) or the call failed with the context at 90% of the window or more for no reason of its own — the turn compacts once (trigger `overflow_retry`) and sends the same call again on the compacted history. This happens at most once per run, only with `autoCompact` on, only when the backoff above allows a compaction, and never once the run is cancelled or past its deadline. If the compaction fails or the retry fails too, the run fails with the original error. Only an error from the model's response counts: the failed call ran no tools, so nothing is executed twice. LiteLLM's `ContextWindowExceededError` counts as the provider saying so, whatever provider it wraps. Near the window, an error the provider already retried as transient (a rate limit or an overload after retries, a 503/529, Bedrock's throttling) or a timeout (the HTTP client's, a stalled stream) does not count: it names its own cause, and a compaction would not fix it. A stream reset there still counts, since that is how some proxies report an overflow.

### What the summarizer sees

The summarizer only sees the history since the previous summary, as one text transcript: tool calls become `[tool_call <name> id=<id>] <input>` lines, tool results `[tool_result <name> id=<id>] <content>`, a server-side tool search one `[tool_search ...]` line with its query and the tools it found, reasoning blocks and binary attachments are dropped, and each tool input or result over 2,000 estimated tokens keeps only its head and tail around a `[... N tokens omitted ...]` marker. The request declares no tools, and replaying native tool, server-tool or signed thinking blocks into it makes some proxies (LiteLLM in front of Bedrock) reset the stream instead of returning the 400. If the transcript does not fit 90% of the summarizer's window (or the effective `summarizerMaxInputTokens`, when set and smaller), the oldest messages are dropped (with a warning) rather than failing the compaction, and a tool result never stays without its call. The turn's own prompt, such as a flow step's task, is kept unless it alone takes more than half of the space left, or keeping it would leave no recent history or the input over budget. The fit is judged on the local 4 bytes/token estimate, scaled up by the session's last reported usage when that is larger, so an overflowed session whose size the estimate undercounts is still trimmed. It remains an estimate: a summarizer whose tokenizer counts far more than the main model's can still reject its input; lower `summarizerMaxInputTokens` then.

## Per-agent tuning

The threshold can be lowered per agent — useful when a proxy resets streams well before the model's nominal window. For an agent defined in markdown, set it in the frontmatter:

```markdown
---
model: bedrock.claude-opus-4-6
compactionThreshold: 0.4
---
```

For a JSON-only agent, set `agents.<id>.compactionThreshold` in `.opencode.json`. Do not add a JSON `agents.<id>` entry just for the threshold when the agent is defined in markdown: an entry without `model` is given the default model and its default `maxTokens`, and those replace the frontmatter values. If you need the JSON entry, repeat the agent's `model` (and `maxTokens`, if set) in it:

```json
{ "agents": { "neo": { "model": "bedrock.claude-opus-4-6", "compactionThreshold": 0.4 } } }
```

`compactionThreshold` is a fraction in (0, 1]; out-of-range values are ignored with a warning. A flow step's `compact.threshold` wins over it, and it never enables compaction when `autoCompact` is off.

`summarizerMaxInputTokens` (frontmatter or `agents.<id>`, same caveat) caps what the agent's compactions send the summarizer, in estimated tokens. Set it when the summarizer's route rejects large inputs below its nominal window. Precedence: the agent's frontmatter/registry value, then `agents.<id>`, then `agents.summarizer` — set on the `summarizer` agent it caps every agent without its own value:

```json
{ "agents": { "summarizer": { "model": "bedrock.claude-sonnet-4-5", "summarizerMaxInputTokens": 300000 } } }
```

## Self-compaction

A long-lived session (a daemon agent behind the chat bridge) moves from task to task, and a finished task's tool calls and output are noise for the next one. With `selfCompact` the agent compacts its own session when it decides a task is done; the threshold above stays in place as the safety net. Set it in the agent's frontmatter (or `agents.<id>.selfCompact`, with the same caveat as `compactionThreshold`):

```markdown
---
model: bedrock.claude-opus-4-6
selfCompact:
  enabled: true
  minTokens: 150000
---
```

`enabled` registers the `compact_session` tool for the agent (primary agents only; the agent's `tools` / `allowTools` can still exclude it). The tool takes an optional `keep`: notes the agent wants carried into the summary, such as open items, pending decisions and the ids of background work still running. A call only records the request and returns the session's context size, counted as the auto-compaction check counts it; below `minTokens` it returns `skipped: <n> tokens < floor <m>` and records nothing (unset or 0: no floor). The request is the persisted tool result, so it survives a restart.

The compaction runs right after the turn ends, while the session is still held, so a message that arrives meanwhile waits for it; through the chat bridge it starts only once the turn's reply has been posted, and the run's progress card closes when it is done. A caller that stops listening once it has the reply does not abort it; only cancelling the agent does. A background task that finishes meanwhile is kept after the summary, and the session is resumed for it once the compaction is done. If the turn failed, the process stopped first, or the turn was a flow step (the flow runner reuses the session at once), it runs before the next turn's first model call.

The summarizer's prompt carries every pending request's `keep` text with the instruction to carry it forward verbatim — also when another compaction (the threshold, `/compact`) happens to serve the request. Self-compaction does not depend on `autoCompact`, honours the failure backoff (a failed one is retried by the next turn; a cancelled one does not count as failed), logs INFO lines with the tokens before and after, and its summarizer trace is named `<agent>/self-compaction` (`<agent>/<flow>/<step>/self-compaction` in a flow), with the input `self-compaction requested by agent over <n> messages` and `compaction.trigger` `self_after_turn` or `self_pre_turn`.
