## 1. Summarized display

- [x] 1.1 `preparedMessages`: adaptive thinking sets `Display: summarized` unless the model's provider is Kimi.
- [x] 1.2 Tests: request shape for Anthropic, Bedrock and Vertex Claude (summarized) and Kimi (no display); the Bedrock wire body carries it.

## 2. Coalesced thinking deltas

- [x] 2.1 Anthropic stream loop: first delta of a block at once, then 256-byte batches, flushed at block stop, message stop and on a truncated stream.
- [x] 2.2 Test: 60 × 10-byte deltas emit 10/260/260/70 and the final block keeps the full text and signature.

## 3. Thinking tokens

- [x] 3.1 `TokenUsage.ThinkingTokens` from Anthropic `output_tokens_details.thinking_tokens` and OpenAI `completion_tokens_details.reasoning_tokens`.
- [x] 3.2 `langfuse.Usage.Thinking` → `usage_details.thinking` when non-zero.
- [x] 3.3 Tests: streamed usage on SSE and Bedrock, OpenAI usage, the Langfuse usage attribute.

## 4. Bedrock betas

- [x] 4.1 `bedrockMiddleware` splits comma-joined `anthropic-beta` values into array elements; covered by the Bedrock wire-body test.

## 5. Docs and verification

- [x] 5.1 `docs/telemetry.md`: usage keys and the summarized display.
- [x] 5.2 Live e2e on the LiteLLM Bedrock route (Sonnet 5.5, deferred tools, thinking replay across the tool loop); replay input-cost A/B; streamed delta counts.
- [x] 5.3 `make test` before merge.
