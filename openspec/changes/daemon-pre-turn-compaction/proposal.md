# Compact Before a Turn's First Model Call

## Why

Daemon-mode opencode sessions (the chat bridge, `opencode serve`, cron heartbeats) never auto-compact. A c2-agent daemon session (Neo, Slack) grew to ~990-997K tokens on a 1M-token window. From then on every turn failed on its first model call with `stream error: stream ID N; INTERNAL_ERROR; received from peer` — LiteLLM/Bedrock reset the stream instead of returning "prompt too long". Langfuse shows zero summarizer calls for the session, and each hourly heartbeat re-cached ~990K tokens (~$5 per beat).

Four defects combine:

1. **The gate skips a turn's first call.** The in-loop check in `processGeneration` is `cfg.AutoCompact && cycles != 1 && hit`. A daemon turn that is a single model call (a chat reply, a heartbeat) is never checked. The only post-turn check lives in the TUI (`internal/tui/tui.go`), so bridge/daemon/ACP/CLI sessions have none.
2. **The estimate ignores real usage.** `provider.CountTokens` is `max(count_tokens endpoint, local 4 B/token estimate)`. Neither consults the usage the provider actually reported for the previous call, which `TrackUsage` already stores on the session (`PromptTokens + CompletionTokens` is the last call's full prompt + output).
3. **The threshold is flow-only.** `RunOptions.CompactionThreshold` is set only by flow steps (`step.compact.threshold`). Daemon turns always use 0.95 — which on a 1M window leaves no room once a proxy starts resetting streams well below the window.
4. **The summarizer input is unbounded.** `performSynchronousCompaction` and `Summarize` send the RAW `messages.List`, not the history after `SummaryMessageID`. Input grows across compactions and can exceed the summarizer's own window — so an already-overflowed session cannot compact its way out.

## What Changes

- **Pre-turn compaction gate.** `processGeneration` counts the history plus the incoming (not yet persisted) user message BEFORE the user message is written. Over the threshold (and `autoCompact` on), it compacts synchronously, rebuilds the history from the new summary, and only then persists the user message so it lands after the summary. Applies to every `Run`/`RunWith` caller. The in-loop check keeps its `cycles != 1` guard, so cycle 1 is not summarized twice.
- **Token floor from reported usage.** A new agent-level `countContextTokens` floors the provider estimate by `sess.PromptTokens + sess.CompletionTokens` plus a local estimate of the messages after the last assistant message. Used at the pre-turn gate, the in-loop check and the post-compaction recount.
- **Per-agent `compactionThreshold`.** `agents.<name>.compactionThreshold` (and markdown frontmatter `compactionThreshold`), in (0, 1]. Precedence: flow step `compact.threshold` > agent `compactionThreshold` > 0.95. `autoCompact` still gates whether compaction runs at all.
- **Bounded summarizer input.** Both compaction paths start from the history after `SummaryMessageID` (the prior summary carries knowledge forward) and drop the oldest messages — never splitting a tool_use / tool_result pair — until the input fits in 90% of the summarizer's window, with a WARN naming how many were dropped.
- **Diagnosable overflow.** When a model call fails and the last estimate was >= 90% of the window, a WARN names the likely context overflow, so a proxy stream reset is no longer an opaque error.

## Capabilities

### New Capabilities

- `auto-compaction`: when and how a session's history is compacted automatically — the pre-turn gate, the usage-floored token count, the per-agent threshold and its precedence, the bounded summarizer input, and the overflow diagnostic.

### Modified Capabilities

<!-- none — no existing spec covers auto-compaction -->

## Impact

- Modified: `internal/llm/agent/agent.go` (gate, `countContextTokens`, `resolveCompactionThreshold`, `summarizerInput`, `likelyContextOverflow`), `internal/config/config.go` (`Agent.CompactionThreshold`, validation), `internal/agent/registry.go` (frontmatter + merge), `cmd/schema/main.go` + regenerated `opencode-schema.json`, `AGENTS.md`, `README.md`.
- `.opencode.json` public contract: additive `agents.<name>.compactionThreshold`.
- No change to `provider.Provider` (`CountTokens` signature unchanged), to the flow YAML schema, or to the TUI's post-turn check.
