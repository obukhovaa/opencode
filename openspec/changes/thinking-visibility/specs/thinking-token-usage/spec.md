## ADDED Requirements

### Requirement: Thinking tokens are captured from provider usage
A provider response's token usage SHALL carry the number of output tokens spent on reasoning when the upstream reports it: Anthropic `usage.output_tokens_details.thinking_tokens` (on the response, or on the stream's `message_delta`), and OpenAI-compatible `usage.completion_tokens_details.reasoning_tokens`. It SHALL be zero when the upstream reports no breakdown. It is a part of the output tokens, never added to them.

#### Scenario: Streamed Anthropic response over Bedrock
- **WHEN** a streamed response's `message_delta` reports `output_tokens: 108` and `output_tokens_details.thinking_tokens: 61`
- **THEN** the response usage has 108 output tokens and 61 thinking tokens, on both the SSE and the Bedrock event-stream routes

#### Scenario: No breakdown
- **WHEN** the upstream reports output tokens without a breakdown
- **THEN** the thinking tokens are zero

### Requirement: Thinking tokens are reported on the generation
A Langfuse generation SHALL report non-zero thinking tokens as a `thinking` key in its `usage_details`, beside `input`, `output` and `total`, without changing `total` or the cost details. Zero thinking tokens SHALL leave the key out.

#### Scenario: Generation with reasoning
- **WHEN** a generation's usage is 10 input, 108 output and 61 thinking tokens
- **THEN** its `usage_details` carry `thinking: 61`, `output: 108` and `total: 118`
