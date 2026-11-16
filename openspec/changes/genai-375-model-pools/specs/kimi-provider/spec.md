## ADDED Requirements

### Requirement: Kimi K3.1 is registered on Bedrock
The system SHALL register `bedrock.kimi-k3.1` (API model `global.moonshotai.kimi-k3.1`) and `bedrock.us-kimi-k3.1` (API model `us.moonshotai.kimi-k3.1`) on the `bedrock` provider with the `openai-chat` wire, a 1,048,576-token context, 131,072 default output tokens, vision, and reasoning efforts `low`, `medium`, `high`, `xhigh` and `max` with default `max`. Global pricing per million tokens SHALL be $4 input, $20 output, $5 cache writes and $0.20 cache reads; the US profile SHALL carry the 10% regional premium. Neither model SHALL advertise adaptive thinking, task budgets or tool search.

#### Scenario: Effort unset
- **WHEN** an agent uses `bedrock.us-kimi-k3.1` without `reasoningEffort`
- **THEN** its resolved config and request carry `max`

#### Scenario: xhigh is sent as xhigh
- **WHEN** an agent or flow step sets `xhigh` on `bedrock.kimi-k3.1`
- **THEN** the request body carries `reasoning_effort: "xhigh"`
