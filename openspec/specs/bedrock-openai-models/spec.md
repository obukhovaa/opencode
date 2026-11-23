# Bedrock OpenAI Models

## Purpose

Defines how the bedrock provider serves models by wire format (`Model.Wire`: Anthropic Messages or OpenAI chat completions on InvokeModel), the OpenAI-wire request rules (`xhigh`, default efforts), OpenAI's GPT-6.1 Sol and Terra on Bedrock with their long-context pricing, and the tool-call probe that gates them.

## Requirements

### Requirement: Bedrock routes models by wire format
Every model SHALL declare its wire format through `Model.Wire`: `anthropic-messages` (the default for Bedrock models) or `openai-chat`. The Bedrock client SHALL serve `openai-chat` models through the OpenAI chat-completions client with requests rewritten onto `/model/{id}/invoke` and `/model/{id}/invoke-with-response-stream`, and every other Bedrock model through the Anthropic client, whether or not the model is a built-in. Provider construction and config effort rules SHALL use the same predicate.

#### Scenario: Config-defined Claude model
- **WHEN** a Bedrock model with the `anthropic-messages` wire that is not in the built-in catalog is used
- **THEN** it is served by the Anthropic client

### Requirement: OpenAI-wire requests send xhigh to models that accept it
The OpenAI client SHALL accept the `xhigh` effort. It SHALL send `reasoning_effort: "xhigh"` when the model supports `xhigh`, and `high` with a warning otherwise. On Bedrock, when no effort reaches the client, it SHALL send the model's default effort if it has one and omit the field otherwise.

#### Scenario: xhigh on a model without it
- **WHEN** `xhigh` reaches the OpenAI client for a model without `xhigh` support
- **THEN** the request carries `high` and a warning is logged

### Requirement: GPT-6.1 Sol and Terra are registered on Bedrock
The system SHALL register `bedrock.gpt-6.1-sol` (API model `global.openai.gpt-6.1-sol`), `bedrock.us-gpt-6.1-sol` (`us.openai.gpt-6.1-sol`), `bedrock.gpt-6.1-terra` (`global.openai.gpt-6.1-terra`) and `bedrock.us-gpt-6.1-terra` (`us.openai.gpt-6.1-terra`) with the `openai-chat` wire, a 1,050,000-token context, 128,000 default output tokens, reasoning with default effort `medium`, and no attachments. Sol SHALL accept `xhigh` and `max`; Terra SHALL stop at `high`. Global pricing per million tokens SHALL be $2 input, $10 output, $2.50 cache writes and $0.10 cache reads for Sol and $1, $5, $1.25 and $0.05 for Terra; US profiles SHALL carry the 10% regional premium. A prompt over 272,000 tokens SHALL bill input (cache reads and writes included) at 2x and output at 1.5x. The entries SHALL be documented as pending tool-call verification until the Bedrock tool-call probe passes.

#### Scenario: Long prompt pricing
- **WHEN** a GPT-6.1 Sol request has a 300,000-token prompt
- **THEN** its input cost is twice and its output cost one and a half times the base rates

#### Scenario: Effort unset
- **WHEN** an agent uses `bedrock.gpt-6.1-terra` without `reasoningEffort`
- **THEN** its resolved config and request carry `medium`

### Requirement: Long-context output pricing can differ from input
`Model.LongContextOutputCostMultiplier`, when non-zero, SHALL replace `LongContextCostMultiplier` for output tokens of a request whose prompt exceeds `LongContextThreshold`; zero SHALL keep the single multiplier for both.

#### Scenario: Single multiplier unchanged
- **WHEN** a model sets only `LongContextCostMultiplier`
- **THEN** a long prompt multiplies input and output cost by it, as before

### Requirement: A Bedrock tool-call probe exists
The repository SHALL ship `scripts/probe_bedrock_tools.sh`, which runs a two-turn tool-call round trip against `$LOCAL_ENDPOINT/bedrock/model/<profile>/invoke` for the Kimi K3.1 and GPT-6.1 profiles with `$LOCAL_ENDPOINT_API_KEY`, never printing the key, and exits non-zero when any profile fails; and an opt-in test behind the `bedrock_live` build tag that drives the Bedrock client's send and stream paths with a tool for all six ids.

#### Scenario: Missing credentials
- **WHEN** the probe or the live test runs without `LOCAL_ENDPOINT` and `LOCAL_ENDPOINT_API_KEY`
- **THEN** the script exits with a usage error and the test skips
