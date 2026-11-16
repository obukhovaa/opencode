## 1. Provider and catalog

- [x] 1.1 OpenAI client: accept `xhigh`, send it when the model supports it, else `high` with a warning.
- [x] 1.2 `Model.Wire` and `BedrockOpenAIChat()`; route the Bedrock client, provider construction and config effort rules by wire; drop `IsBedrockKimi`.
- [x] 1.3 Register `bedrock.kimi-k3.1` / `bedrock.us-kimi-k3.1` and the four GPT-6.1 entries; `LongContextOutputCostMultiplier` in `CalculateCost`; `DefaultReasoningEffort` at config load and in the Bedrock OpenAI client; schema, `docs/providers.md`, add-model skill.
- [x] 1.4 `scripts/probe_bedrock_tools.sh` and the opt-in `bedrock_live` test for all six ids.

## 2. Config-defined models

- [x] 2.1 `customModels` config, strict validation, registration in `Load` before `Validate`, removal on `Reset` and failed loads.
- [x] 2.2 Schema property, third `anyOf` branch for agent `model`, `wire` enum probe, viper round-trip test, `docs/configuration.md` and `docs/providers.md`.

## 3. Flow runtime

- [ ] 3.1 Resolved `model` on every step state, `flow.step.*` event, `/flow/status` record and CLI result.
- [ ] 3.2 `denyTools`: context carrier, registry wrapper in `NewAgent` with the list in the step cache key, dispatch guard, async subagent carry, load validation, rejected in dynamic flows.
- [ ] 3.3 `bestEffort`: state and event flag; no run error and no error agent event for a failed best-effort step.
- [ ] 3.4 `modelPool` / `modelSeed` / `modelShadowRate`: parsing, rendezvous pick, shadow sampling, effort clamp, `args.pool.<step id>`, reserved `pool`, literal validation, rejected in dynamic flows, include coverage samples.
- [ ] 3.5 `docs/flows.md` (step fields, "Model pools and shadow runs", events) and the flow-creator skill references.

## 4. Verification and completion

- [ ] 4.1 `make test`, `make schema-check`; run the probe only where `LOCAL_ENDPOINT` credentials are already present, otherwise record GPT as pending.
- [ ] 4.2 Sync the delta specs and archive the change.
