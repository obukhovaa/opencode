## ADDED Requirements

### Requirement: Bedrock models can be defined in config
`.opencode.json` SHALL accept a top-level `customModels` array whose entries define a model with `id`, `name`, `provider`, `wire`, `apiModel`, the four `costPer1M*` rates, `contextWindow`, `defaultMaxTokens`, the capability flags (`canReason`, `supportsAdaptiveThinking`, `supportsMaximumThinking`, `supportsXHighThinking`, `supportsTaskBudget`, `supportsToolSearch`, `supportsAttachments`, `rejectsForcedToolChoice`), the long-context fields (`longContextThreshold`, `longContextCostMultiplier`, `longContextOutputCostMultiplier`) and `defaultReasoningEffort`. Config load SHALL register valid entries into the model catalog right before validating agents, so agents, flow step `model` values and model pools accept the ids like built-ins, and SHALL remove them again on `config.Reset` and when the load fails.

#### Scenario: Agent on a custom model
- **WHEN** `customModels` defines `bedrock.acme-large` and an agent sets `model: bedrock.acme-large`
- **THEN** the config loads and the agent runs on that model with the configured pricing

### Requirement: Custom models are validated strictly
Config load SHALL fail when an entry's `id` does not match `^bedrock\.[a-z0-9][a-z0-9._-]*$`, an id repeats, `provider` is not `bedrock`, `wire` is not `anthropic-messages` or `openai-chat`, `apiModel` is empty, a cost is negative, the input or output rate is not positive, `contextWindow` or `defaultMaxTokens` is not positive or the latter exceeds the former, a thinking flag is set without `canReason`, an Anthropic-only flag (`supportsAdaptiveThinking`, `supportsTaskBudget`, `supportsToolSearch`, `rejectsForcedToolChoice`) is set on the `openai-chat` wire, the long-context threshold and multipliers are not set together, or `defaultReasoningEffort` is a level the model does not accept. An entry whose id is a built-in model SHALL be ignored with a warning; the built-in wins.

#### Scenario: Built-in id
- **WHEN** `customModels` redefines `bedrock.claude-sonnet-5-5`
- **THEN** the config loads, a warning is logged and the built-in definition stays in effect

### Requirement: Custom models are part of the config schema
`opencode-schema.json` SHALL describe `customModels`, with `wire` as an enum, and SHALL accept agent `model` values that match the custom id pattern in addition to the built-in enum.

#### Scenario: Editor validation
- **WHEN** an agent's `model` is `bedrock.acme-large`
- **THEN** the schema accepts it
