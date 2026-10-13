# An Agent Compacts Its Own Session When a Task Is Done

## Why

A daemon agent runs one long-lived session that moves from task to task. When a task is finished, its tool calls and their output (searches, diffs, API payloads, logs) are noise for the next task, yet every later turn carries and re-caches them until the auto-compaction threshold forces a summary.

The agent cannot act on "this task is done". Compaction runs only at a token threshold (automatically) or on a manual `/compact`, which some chat surfaces cannot deliver. No tool triggers it, and a tool could not compact inline anyway: the turn that calls it holds the session's run slot.

## What Changes

- **`compact_session` tool, opt-in per agent.** A new `selfCompact: {enabled, minTokens}` agent setting, in `.opencode.json` and in agent markdown frontmatter (a JSON-only entry without `model` would reset a markdown agent's model), registers the tool for a primary agent. The agent's `tools` / `allowTools` can still exclude it.
- **The tool only records a request.** It takes an optional `keep` (notes to carry into the summary verbatim) and returns the session's context size, counted by the same function every auto-compaction decision uses. Below `minTokens` it returns `skipped: <n> tokens < floor <m>` and records nothing. The request is the persisted tool result: it sits after the current summary until a compaction writes a summary after it, so it survives a restart and is served exactly once.
- **The compaction runs right after the turn.** After the turn's terminal event is sent and before the run's event channel closes, still holding the session's run slot, the agent serves a pending request. Bridge, heartbeat, cron and auto-resume turns all go through that path. The chat bridge marks its runs so the compaction waits until the turn's reply has been posted; a message that arrives meanwhile waits behind the compaction.
- **Fallback before the next turn.** A request still pending when a turn starts (the turn failed, the process stopped) is served before the turn's first model call, at the existing pre-turn gate.
- **The keep notes reach the summarizer.** Every compaction appends the pending requests' `keep` notes to the compaction prompt ("Carry these forward verbatim: ..."), whatever started it, so an auto-compaction or `/compact` that happens to serve a request keeps them too.
- **Observability.** The summarizer trace is named `self-compaction requested by agent over <n> messages`, `compaction.trigger` is `self_after_turn` or `self_pre_turn`, and INFO lines name the context size before and after.
- Self-compaction does not depend on `autoCompact`, honours the compaction failure backoff (a pending request is retried without the agent asking again), and leaves auto-compaction at the threshold unchanged.

## Non-goals

- A per-agent extra compaction instruction in config.
- Changing how session token usage is stored or named.

## Capabilities

### New Capabilities

- `agent-self-compaction`: the `compact_session` tool, its configuration, when the requested compaction runs, and what its summarizer is told.

## Impact

- `internal/config`: `SelfCompact` type and `Agent.SelfCompact`, validated (negative floor ignored).
- `internal/agent/registry.go`: `AgentInfo.SelfCompact` from frontmatter, config overlay and builtins.
- `internal/llm/tools/compact_session.go`: the tool and its request metadata.
- `internal/llm/agent`: `compaction_self.go` (pending detection, post-turn and pre-turn compaction, `WithTurnDelivery`), the post-turn hook in `RunWith`, the pre-turn fallback in `processGeneration`, keep notes in `summarizerInputWithStats`, tool registration in `NewToolSet`.
- `internal/bridge/service/dispatch.go`: runs are marked with `WithTurnDelivery`, signalled once the terminal event is handled.
- `cmd/schema` and `opencode-schema.json`, README, AGENTS.md.
