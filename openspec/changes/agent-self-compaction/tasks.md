## 1. Configuration

- [x] 1.1 `config.SelfCompact{Enabled, MinTokens}` on `config.Agent`; a negative floor is warned about and read as no floor.
- [x] 1.2 `AgentInfo.SelfCompact` from markdown frontmatter (validated), the config overlay and builtins; a JSON entry replaces the markdown value, an unset one keeps it.
- [x] 1.3 Schema generator entry, `opencode-schema.json` regenerated.

## 2. Tool

- [x] 2.1 `compact_session` with optional `keep`: counts the session, returns `skipped: <n> tokens < floor <m>` below the floor with no metadata, otherwise records `CompactSessionRequest` in the result metadata and returns the count.
- [x] 2.2 Registered for primary agents with `selfCompact.enabled` and the tool allowed; the counter is bound when the tool set resolves.

## 3. Execution

- [x] 3.1 `pendingSelfCompactionNotes`: stored requests after the current summary and their keep notes.
- [x] 3.2 `compactAfterTurn` in `RunWith` after the terminal event is sent, before the channel closes, holding the slot; waits for `WithTurnDelivery` when present; skips failed and cancelled turns.
- [x] 3.3 Pre-turn fallback in `processGeneration`, independent of `autoCompact`, keeping an auto-resume tail.
- [x] 3.4 Through `compactionAllowed` / `autoCompact` (backoff), INFO before/after log, `self_after_turn` / `self_pre_turn` triggers, self-compaction trace name (`<agent>/self-compaction`).
- [x] 3.5 Keep notes appended to the compaction prompt in `summarizerInputWithStats`; callers use the returned prompt.
- [x] 3.6 The bridge dispatcher marks runs with `WithTurnDelivery` and signals after handling the terminal event.
- [x] 3.7 Post-turn compaction detached from the caller's cancellation, ended by the agent's `Cancel` and a bound; flow-step runs skip it; a cancelled compaction is not a backoff failure.
- [x] 3.8 Completions written during a self-compaction are moved after the summary; a skipped auto-resume is recorded by the task package and started once the slot is released.

- [x] 3.9 Track synthetic completions actually observed by model calls; preserve unseen pairs across the final-call/post-turn handoff, and publish resume markers before the busy check.

## 4. Tests and docs

- [x] 4.1 Tool: below the floor stores nothing; above it stores the request; errors store nothing.
- [x] 4.2 Agent: compaction runs after the run's terminal event and delivery, not inside the turn; `keep` reaches the summarizer prompt; a pending request survives a reload and runs before the next turn's first call; a failed turn leaves it for the next; two requests and concurrent entry points never compact twice; registration; a caller cancel after the turn does not abort it, an agent cancel does; flow steps defer; a completion during the compaction is kept and resumed; the trace name.
- [x] 4.3 Bridge: the reply is posted before the compaction starts.
- [x] 4.4 Config viper round trip and validation; registry frontmatter and overlay merges.
- [x] 4.5 README (Auto Compact, Tools), AGENTS.md agent keys.

- [x] 4.6 Deterministic regressions for completions during the final model call, after the terminal response, already observed before compaction, and concurrent slot release during the busy check.
