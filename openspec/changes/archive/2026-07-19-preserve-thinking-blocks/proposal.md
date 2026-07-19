# Preserve Thinking Blocks

## Why

Anthropic's API is stateless: the only memory a model has of its own reasoning is what the client echoes back. Anthropic documents thinking-block replay as the standard multi-turn pattern — blocks must be echoed byte-exact (each carries a cryptographic signature), and dropping them degrades multi-step tool-use quality on adaptive/interleaved-thinking models (on Fable-class models it can break the turn outright). opencode currently discards thinking at every tool boundary: `ReasoningContent` stores only concatenated text (no signature, block boundaries merged away), and `convertMessages` never emits thinking blocks. Every Claude tool loop runs with the model's working reasoning amputated. Moonshot's Kimi API documents the same replay contract ("always preserve the reasoning_content of each historical assistant message"), making this a prerequisite for the planned Kimi provider.

## What Changes

- `message.ReasoningContent` gains `Signature` and redacted-block support; streaming appends keep **per-block boundaries** instead of merging all thinking into one part.
- The Anthropic provider extracts finalized thinking blocks (text + signature, plus `redacted_thinking` passthrough) from the accumulated response and returns them on `ProviderResponse`; a new provider event signals block finalization during streaming.
- The agent loop persists authoritative reasoning blocks (with signatures) on the assistant message at completion, replacing the streamed preview parts.
- `convertMessages` in the Anthropic provider replays stored thinking blocks verbatim at the start of assistant turns. Blocks without a signature (legacy rows, non-Anthropic sources) are skipped, preserving today's behavior for old data.
- TUI/rendering behavior is unchanged: reasoning display continues to concatenate thinking text; redacted blocks render nothing.

## Capabilities

### New Capabilities

- `thinking-block-replay`: Persistence of provider reasoning blocks (per-block text + signature + redacted passthrough) on assistant messages, and verbatim replay of those blocks to reasoning-replay-contract providers (Anthropic dialect: native API, Bedrock, Vertex, and Anthropic-compatible endpoints).

### Modified Capabilities

<!-- none — no existing spec covers provider/message reasoning handling -->

## Impact

- `internal/message/content.go`, `internal/message/message.go` — part schema (JSON-serialized in DB; additive, backward compatible), append/finalize helpers.
- `internal/llm/provider/provider.go` — `ProviderResponse`/`ProviderEvent` carry finalized reasoning blocks.
- `internal/llm/provider/anthropic.go` — extract blocks from accumulated message (stream + send paths); emit thinking blocks in `convertMessages`.
- `internal/llm/agent/agent.go` — persist authoritative blocks at completion.
- Bedrock/Vertex paths inherit the behavior via the shared anthropic client. OpenAI/Gemini providers are untouched (they never see reasoning parts with signatures).
- Cache note: including thinking blocks changes the resent prefix once per rollout (one-time cache invalidation for in-flight sessions); Anthropic strips prior-turn thinking server-side without billing, so steady-state cost impact is limited to the active tool loop.
