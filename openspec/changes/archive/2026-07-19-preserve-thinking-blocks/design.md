# Design — Preserve Thinking Blocks

## Context

Anthropic streams reasoning as `thinking` content blocks: `content_block_start (type=thinking)` → N× `thinking_delta` → `signature_delta` → `content_block_stop`. The SDK's `Message.Accumulate` already assembles complete `ThinkingBlock{Thinking, Signature}` and `RedactedThinkingBlock{Data}` values on the accumulated message — opencode just never reads them. On the persistence side, `message.ReasoningContent{Thinking string}` is built by appending `EventThinkingDelta` payloads, merging every block into one part and discarding signatures. `convertMessages` (anthropic.go) rebuilds assistant turns from text + tool_use only.

Replay contract (Anthropic docs, claude-api reference): echo thinking blocks back **unchanged** when continuing on the same model; blocks are signature-verified — *modified* blocks are rejected, *absent* blocks are tolerated on adaptive models but forfeit reasoning continuity; prior-turn blocks are stripped server-side unbilled. Moonshot's Anthropic-compatible endpoint documents the same contract.

Current message part serialization is a JSON envelope per part with a `partType` discriminator (`internal/message/message.go:416`), stored in SQLite/MySQL — additive struct fields are backward compatible.

## Goals / Non-Goals

**Goals:**
- Persist reasoning per block, verbatim: text + signature, and `redacted_thinking` opaque data.
- Replay stored blocks byte-exact at the start of assistant turns for all anthropic-client providers (Anthropic, Bedrock, Vertex, Anthropic-compatible base URLs).
- Keep live streaming display behavior (incremental thinking text in TUI) unchanged.
- Degrade gracefully for legacy rows and non-Anthropic reasoning (no signature → not replayed, same as today).

**Non-Goals:**
- OpenAI-dialect `reasoning_content` echo (DeepSeek/Kimi `/v1` path) — separate change if ever needed.
- Replaying reasoning across different models/providers (the server ignores or drops foreign blocks; we still send only signature-bearing blocks the session's own provider produced).
- TUI rendering changes; redacted blocks simply render nothing.
- Compaction/summarization changes.

## Decisions

1. **Authoritative extraction at completion, not during streaming.**
   `EventThinkingDelta` keeps feeding the TUI a merged preview part. (Pre-existing bug found during review: `agent.go` appends `event.Content` — always empty on thinking events — instead of `event.Thinking`, so thinking never actually reached persistence/TUI for anthropic models; this change fixes the field.) When the stream completes (or `send` returns), the provider extracts the finalized block list from the SDK-accumulated message and returns it as `ProviderResponse.Reasoning []message.ReasoningContent`. The agent then **replaces** the message's reasoning parts with the authoritative list before finalizing. Alternative considered: emit per-block start/signature/stop events and assemble block-accurate parts incrementally — rejected: more streaming protocol surface for zero user-visible benefit; the accumulator already holds the truth at completion.

2. **Single part type, extended.** `ReasoningContent` gains `Signature string`, `Redacted bool`, `Data string` (all `omitempty`). A redacted block is `{Redacted: true, Data: <opaque>}`. Alternative: a separate `RedactedReasoningContent` part type — rejected: new `partType` discriminator churn in marshal/unmarshal and every renderer for a block users never see.

3. **Replay placement and gating.** In `convertMessages`, assistant messages emit reasoning blocks **first** (thinking blocks with signature → `ThinkingBlockParam`; redacted → `RedactedThinkingBlockParam`), then text, then tool_use — matching the API's own emission order. Gate: skip parts with empty `Signature` unless `Redacted` (covers legacy rows and openai-sourced reasoning automatically). Emit for **all** assistant messages: the server strips prior-turn blocks itself, and "replay everything verbatim" is the documented contract — selective trimming would be a client-side heuristic with no upside.
   Simplification: if a response ever interleaves text between thinking blocks, reconstruction is thinking-first; the API accepts this (signatures bind block content, not position), and the common shapes (`thinking…, text`, `thinking…, tool_use`) are order-preserved.

4. **Cache breakpoints unaffected.** `cache_control` continues to attach to the *last* block of the last messages; reasoning blocks are prepended, so the existing `OfText`/`OfToolUse` last-block logic still finds the same block kinds. (Thinking blocks are not cacheable blocks; no marker is ever placed on them.)

5. **`cleanMessages` invariant preserved.** Assistant messages with only reasoning parts (no text, no tool calls) are already dropped before conversion; a thinking-only turn cannot reach the API as an empty content array.

## Risks / Trade-offs

- [Byte-exactness] Signature verification rejects modified blocks → store text exactly as accumulated (no trimming/normalization) and add a unit test asserting the replayed `ThinkingBlockParam` equals the stored part verbatim.
- [Prefix change on rollout] First request of an in-flight session after upgrade re-writes the prompt cache once → accepted; steady-state cache behavior is unchanged since block inclusion is deterministic thereafter.
- [Provider quirks] An Anthropic-compatible endpoint could reject replayed blocks or unsigned absence differently → gating on signature keeps us within the documented contract; per-provider spike (Kimi T5/T6) validates before that provider ships.
- [Rare interleaved order loss] thinking-first reconstruction versus true interleaved order → documented; revisit only if the API starts enforcing positional order.
- [Larger request payloads] Active-loop thinking blocks ride along until the next user turn → bounded by one task's reasoning; prior turns are stripped server-side and unbilled.

## Migration Plan

Additive JSON fields on persisted parts — old rows deserialize with empty `Signature` and are simply never replayed. No DB migration, no config or schema changes. Rollback = revert; new fields are ignored by old binaries.

## Open Questions

- None blocking. (Whether Moonshot's endpoint *requires* the blocks — vs. merely benefiting — is answered by the Kimi change's spike; this change is correct for Anthropic regardless.)
