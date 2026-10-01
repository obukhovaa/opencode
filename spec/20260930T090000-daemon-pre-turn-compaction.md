# GENAI-392: auto-compaction for daemon/bridge sessions

## Context
Daemon-mode opencode (c2-agent Neo, bridge/Slack) sessions never auto-compact. One Neo session grew to ~990-997K tokens on a 1M window. Since then every turn fails on its first model call with `stream error: stream ID N; INTERNAL_ERROR; received from peer`: LiteLLM/Bedrock reset the stream instead of returning "prompt too long". Langfuse shows zero summarizer calls. Each hourly heartbeat re-caches ~990K tokens (~$5/beat).

OpenSpec change: `openspec/changes/daemon-pre-turn-compaction/` (new capability `auto-compaction`).

## Current behaviour (verified on main @ f7a6460)
| Area | Location | Behaviour |
|---|---|---|
| Gate | `internal/llm/agent/agent.go` in-loop check | `cfg.AutoCompact && cycles != 1 && shouldTrigger`: a turn's first call is never checked |
| Post-turn check | `internal/tui/tui.go:569` | TUI only; bridge/daemon/ACP/CLI have none |
| Estimate | `internal/llm/provider/provider.go` `CountTokens` | max(count_tokens endpoint, local 4 B/token estimate incl. system + tools). Ignores real usage of previous calls |
| Real usage | `agent.TrackUsage` | `sess.PromptTokens = in + cache_create`; `sess.CompletionTokens = out + cache_read`. The SUM is the last call's full prompt+output size; neither field alone is |
| Threshold | `RunOptions.CompactionThreshold`, `effectiveCompactionThreshold`; set only by `internal/flow/service.go` and `struct_output_retry.go` from `step.Compact.Threshold` | Daemon turns always use 0.95 |
| Summarizer input | `performSynchronousCompaction`, `Summarize` | Both send the RAW `messages.List`, not the history after `SummaryMessageID`. Input grows without bound across compactions and can exceed the summarizer window |
| After compaction | `performSynchronousCompaction` | `PromptTokens=0`, `CompletionTokens=summary output tokens` |

## Requirements

### R1: Pre-turn compaction gate
In `processGeneration`, BEFORE the user message is persisted:
1. Resolve `toolSet` earlier (move `a.resolveTools()` above `createUserMessage`). Deferred-delta injection stays after the gate.
2. Build the count input: current filtered `msgs`, plus a transient unpersisted `message.Message{Role: User, Parts: text + attachment parts}` when `hasUserTurn`. Auto-resume turns (no content) count `msgs` only.
3. `eta, hit := a.countContextTokens(ctx, sessionID, a.resolveCompactionThreshold(opts), countInput, toolSet)`.
4. If `cfg.AutoCompact && hit`: log Info `"Auto-compaction triggered before turn"` (session_id, token_count, threshold, context_window) and call `performSynchronousCompaction`. On success: reload `msgs`, `filterMessagesFromSummary`, `filterEmptyUserMessages`; `withStructOutputSchema` runs after the gate anyway. Do NOT apply the TaskBudget-remaining ctx here: the turn has not started, so its budget is the full one (the in-loop path keeps it, via a `withTaskBudgetRemaining` helper). On an auto-resume turn, carry the trailing synthetic completion pair past the summary in memory. If a `Summarize` of the session is in flight on the agent, wait for it (bounded) and reload before counting. On failure: WARN and continue, the same contract as the in-loop path.
5. Only then persist the user message, so seq order is `summary → user message` and the user turn survives later reloads.
6. The in-loop check becomes `cycles != 1 || outerCycles > 1`: cycle 1 of the first outer cycle is covered by the gate (no double summarization), while a non-interactive re-entry's first call has no gate in front of it and is checked.

Applies to every `Run`/`RunWith` caller: bridge, daemon, ACP, CLI, TUI, flow steps. For the TUI it is additive: its post-turn check stays.

### R2: Token floor from reported usage
New `func (a *agent) countContextTokens(ctx, sessionID string, threshold float64, msgs []message.Message, toolSet []tools.BaseTool) (int64, bool)`:
- `est, _ := a.provider.CountTokens(ctx, threshold, msgs, toolSet)`. The provider's hit flag is ignored; hit is recomputed below.
- `sess := a.sessions.Get` (on error: return est and the provider hit).
- `reported := sess.PromptTokens + sess.CompletionTokens`. It covers history up to and including the last assistant message.
- `tail := message.EstimateTokens(msgs[lastAssistantIdx+1:], nil, message.BytesPerTokenEta)`. If there is no assistant message in `msgs`, tail = 0 and reported is ignored.
- `final := max(est, reported+tail)`; `hit := window > 0 && final >= int64(float64(window)*threshold)`.
- If `reported+tail > est*1.1`, log Info `"token estimate corrected by reported usage"` (estimate, reported, tail).
- Use it at the pre-turn gate, at the in-loop check and at the post-compaction recount. `AdjustMaxTokens` receives `final`.
- After compaction the session holds `PromptTokens=0`, so the floor is about the summary size and the estimate wins. That is correct.
- Leave `provider.CountTokens`'s signature alone: the provider has no session access, and the stub providers in tests implement the interface.

### R3: Per-agent `compactionThreshold`
- `config.Agent.CompactionThreshold float64 `json:"compactionThreshold,omitempty"``.
- `validateAgent`: a value outside (0,1] → `logging.Warn` + set to 0 (inherit default).
- `internal/agent/registry.go`: yaml frontmatter `compactionThreshold`, merged at the same sites as `TaskBudget` (debug args, builtin copy, config overlay, markdown overlay).
- `cmd/schema/main.go`: add `compactionThreshold` (number, exclusiveMinimum 0, maximum 1, description) to BOTH agent definitions. Run `make schema` and keep `make schema-check` / `go test ./cmd/schema/...` green.
- `func (a *agent) resolveCompactionThreshold(opts RunOptions) float64`: `opts.CompactionThreshold > 0` → `effectiveCompactionThreshold(opts.CompactionThreshold)`; else if `config.Get().Agents[a.agentID].CompactionThreshold > 0` → effective(that); else `AutoCompactionThreshold`.
- Precedence: flow step `compact.threshold` > agent `compactionThreshold` > 0.95. Compaction stays gated by the global `autoCompact`; the per-agent key does not enable it.
- Docs: AGENTS.md config list (next to `taskBudget`), the docs page describing autoCompact, and the RunOptions comment.

### R4: Bounded, overflow-safe summarizer input
Shared helper `func (a *agent) summarizerInput(ctx, sessionID string, prompt message.Message) ([]message.Message, error)`, used by `performSynchronousCompaction` and `Summarize`:
1. `messages.List` → `filterMessagesFromSummary(sess.SummaryMessageID)` → `filterEmptyUserMessages`. The prior summary message is kept as the head, so knowledge carries forward.
2. Estimate with `message.EstimateTokens` plus the summarizer's system prompt. If it reaches `0.9 * summarizeProvider.Model().ContextWindow` (skip when the window is 0), drop messages from the oldest end, never splitting an assistant tool_use from its tool_result (reuse the pairing logic in `filterMessagesFromSummary`), until it fits.
3. WARN `"compaction input exceeded summarizer window; dropped oldest messages"` (session_id, dropped, kept, estimated_tokens, window).
4. Append the compaction prompt.

Calibrate step 2 by the session's reported usage: when `PromptTokens + CompletionTokens` exceeds the local estimate of the history up to the last assistant message, scale the budget by `local / reported`, so an overflowed session that the 4 B/token estimate undercounts is still trimmed. The cut keeps the turn's prompt (the latest non-synthetic user message with text, e.g. a flow step's task) after the head, unless it alone exceeds half the budget: the in-loop rebuild does not re-append it, so a summary written without it would lose the task. This lets an already-overflowed session (Neo) recover on its next turn once R1 fires; it remains estimate-based if the summarizer's tokenizer differs a lot.

### R5: Diagnosable overflow failures
Pure helper `likelyContextOverflow(estimated, window int64) bool` returns `window > 0 && estimated >= 0.9*window`. In the non-cancel error branches after `streamAndHandleEvents` (main call, max-turns final call), when it returns true, log WARN `"model call failed with context near the window; likely context overflow — compact or reset the session"` with session_id, agent, estimated_tokens, context_window, ratio, error. Keep `etaTokens` in scope from the last count.

## Non-goals
- The TUI post-turn check (it stays).
- Flow YAML schema: `step.compact.threshold` is unchanged.
- c2-agent changes: follow-up after the release tag (dockerfile literal tag + chart image tag, dev/neo `agents.neo.compactionThreshold: 0.4`, redeploy).
- Slack `/compact` reachability: tracked separately.

## Tests (Go, table-driven, `t.Run`)
| # | Test | Asserts |
|---|---|---|
| a | `TestProcessGeneration_CompactsBeforeFirstCall` | History over the threshold: the summarizer is called before the first `StreamResponse`; the first request starts with the summary; the persisted user message seq > summary seq. Under the threshold: no summarizer call |
| b | `TestCountContextTokens` | Undercounting estimate floored by `PromptTokens+CompletionTokens`+tail → hit; no assistant message → estimate only; post-compaction zero usage → estimate; context window 0 → no hit |
| c | `TestResolveCompactionThreshold` | default 0.95; agent 0.4 → 0.4; agent 0.4 + opts 0.7 → 0.7; agent invalid (zeroed by validation) → 0.95 |
| d | `internal/config` viper round-trip | `agents.<Name>.compactionThreshold` survives `viper.Unmarshal` (case-folded keys) |
| e | registry | frontmatter `compactionThreshold` merges over builtin/config |
| f | `TestSummarizerInput` | starts at SummaryMessageID; trims oldest to fit the window; never splits tool_use/tool_result; WARN path |
| g | `TestLikelyContextOverflow` | boundary cases |
| h | existing `compaction_threshold_test.go`, `cmd/schema` probe tests | still green |

Verification: `go test ./internal/llm/agent/... ./internal/config/... ./internal/agent/... ./internal/flow/... ./internal/bridge/... ./cmd/schema/...`, `make schema-check`, `make test`.

## Acceptance
- A daemon session over the threshold compacts before its next turn's first call, and turns stop failing.
- With `agents.neo.compactionThreshold: 0.4`, Neo compacts around 400K, with a visible Langfuse `auto-compaction over N messages` summarizer trace.
- Flow steps with `compact.threshold` keep their override.
- A call failure near the window emits the overflow WARN.

## Risks
- Moving `resolveTools()` earlier: it has no data dependency on the persisted user message.
- Double summarization: prevented by keeping the `cycles != 1` guard.
- The floor overcounts slightly after a model switch or filtered messages. That is the safe direction.
