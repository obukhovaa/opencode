# Visible Reasoning on Claude Adaptive Thinking

## Why

Every Claude reasoning part the production agents stored (Opus 5, Sonnet 5, Sonnet 5.5, Haiku 5.5) is an empty `thinking` string with a signature. Capture and persistence are not at fault: the anthropic client sends `thinking: {type: adaptive}` with no `display`, and on Claude Opus 4.7 and later and the Claude 5 family the API default is `omitted`. A probe of every adaptive Claude model on the LiteLLM Bedrock route (2026-10-20) showed:

- Opus 4.6 and Sonnet 4.6 return text either way;
- Opus 4.7, 4.8, 5 and 5.5, Sonnet 5 and 5.5 and Haiku 5.5 return none by default, and a summary with `display: "summarized"`;
- every model accepts the field.

So nobody can read why an agent decided what it did (it came up while investigating a code reviewer that stopped finding issues after the Sonnet 5 → 5.5 switch). The volume of thinking is invisible as well: the upstream reports `output_tokens_details.thinking_tokens` on every response, and nothing reads it.

## What Changes

- **Summarized display.** An adaptive-thinking request asks for `thinking.display: "summarized"` on every provider the anthropic client serves except Kimi (Moonshot does not document the field).
  - Billing does not change. Output is billed on the full thinking tokens in both modes. A replayed block costs its thinking tokens + 1 as input in both modes, measured on Sonnet 5.5 (676 vs 677, 1,185 vs 1,186), so the summary text adds no input.
  - Replay is unchanged: the summarized text and its signature go back byte-exact.
- **Coalesced thinking deltas.** Summarized thinking streams in deltas of about 11 characters (167 for one 2.3k-token block). The agent writes the message store on every `EventThinkingDelta`. So the stream emits a block's first delta at once (time-to-first-token), then the rest in 256-byte batches and at every block boundary. The stored blocks and their text are unchanged.
- **Thinking tokens.** `TokenUsage.ThinkingTokens` comes from Anthropic `output_tokens_details.thinking_tokens` and from OpenAI-compatible `completion_tokens_details.reasoning_tokens`. It is reported as a `thinking` key in the generation's Langfuse `usage_details`, a breakdown of `output` that changes neither the total nor the cost.
- **Bedrock betas.** The Bedrock middleware now splits a comma-joined `anthropic-beta` header into one `anthropic_beta` element per beta, as the SDK's own middleware does. It used to send `["a,b"]`, which was harmless only while one beta was configured.

## Capabilities

### New Capabilities

- `thinking-token-usage`: thinking tokens captured from the provider's usage and reported on the generation.

### Modified Capabilities

- `thinking-block-replay`: Claude adaptive thinking asks for summarized display, and streamed thinking deltas are coalesced.

## Impact

- **Modified:**
  - `internal/llm/provider/anthropic.go` (display, delta coalescing, usage);
  - `internal/llm/provider/openai.go` (usage);
  - `internal/llm/provider/provider.go` (`TokenUsage.ThinkingTokens`, Langfuse usage);
  - `internal/llm/provider/bedrock.go` (beta split);
  - `internal/langfuse/types.go` and `span.go` (`thinking` usage key);
  - `docs/telemetry.md`.
- **`.opencode.json` public contract:** unchanged.
- **Behaviour change:**
  - stored reasoning parts of Claude 4.7+ and 5-family turns now carry text;
  - `logOutput` generations show it;
  - the TUI and the HTTP API's `reasoning` parts show it;
  - thinking previews arrive in 256-byte batches rather than per token.
