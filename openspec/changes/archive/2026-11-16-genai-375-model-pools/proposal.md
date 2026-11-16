## Why

GENAI-375 replaces the single standard-tier model of the MR review lane with a weighted pool (Claude Sonnet 5.5, GPT-6.1 Sol, GPT-6.1 Terra, Kimi K3.1 on Bedrock). Each model is measured on live traffic, volume moves to the best one, and the weakest is swapped for a newcomer without a harness release. The harness has no way to pick a step's model from a list, to run a second, read-only "shadow" copy of a step on another model, or to define a model outside the compiled catalog. Kimi K3.1 and GPT-6.1 are not in the catalog on Bedrock, and the OpenAI client folds `xhigh` to `medium`, which would silently run the review lane's default effort at the wrong level.

The interface (step keys, `args.pool` shape, routing rule) is fixed with piano-developer and c2-agent, which are built against it in parallel.

## What Changes

- Flow steps gain `modelPool`, `modelSeed` and `modelShadowRate`: a deterministic weighted pick (rendezvous hashing over sha256) of the step's model and effort, plus an optional shadow sample on another pool entry, written to `args.pool.<step id>` before the prompt is rendered. `pool` becomes a reserved arg name.
- Flow steps gain `denyTools` (globs removed from the step's agent and every subagent it spawns, enforced again at dispatch; `struct_output` is never denied) and `bestEffort` (a failed step ends its branch without failing the run).
- Every step state, `flow.step.*` event, `/flow/status` step record and CLI flow result carries the resolved `model`; best-effort steps carry `bestEffort: true`.
- The OpenAI client sends `xhigh` to models that support it instead of folding it to `medium`.
- `Model.Wire` (`anthropic-messages` | `openai-chat`) replaces the Kimi-specific Bedrock routing, so Bedrock serves any model by its wire format.
- New Bedrock entries: `bedrock.kimi-k3.1`, `bedrock.us-kimi-k3.1`, `bedrock.gpt-6.1-sol`, `bedrock.us-gpt-6.1-sol`, `bedrock.gpt-6.1-terra`, `bedrock.us-gpt-6.1-terra`, with a separate long-context output multiplier and per-model default efforts applied at config load and in the Bedrock client. GPT-6.1 is marked pending a live tool-call probe (`scripts/probe_bedrock_tools.sh`, opt-in `bedrock_live` test).
- `customModels` in `.opencode.json` registers Bedrock models (either wire) at config load, so a newcomer can join a pool without a release.

## Capabilities

### New Capabilities

- `bedrock-openai-models`: Bedrock routing by wire format, the GPT-6.1 Sol / Terra entries, the long-context output multiplier and default efforts for OpenAI-wire Bedrock models.
- `custom-models`: config-defined Bedrock models.

### Modified Capabilities

- `flow-api`: model pools and shadow sampling, `denyTools`, `bestEffort`, and the resolved model on step events and results.
- `kimi-provider`: Kimi K3.1 on Bedrock.

## Impact

`internal/flow` (step keys, pool resolution, validation, best-effort handling), `internal/llm/agent` (tool deny wrapper, dispatch guard, provider construction), `internal/llm/provider` (OpenAI effort mapping, Bedrock routing, cost), `internal/llm/models` (wire, new entries, clamp, custom registry), `internal/config` (`customModels`, effort defaults), `internal/api` and `cmd/flow.go` (event and result fields), `cmd/schema` and `opencode-schema.json`, docs (`providers.md`, `flows.md`, `configuration.md`), the flow-creator and add-model skills. No new dependencies. Flows and teams that do not use the new keys behave exactly as before; older orchestrators ignore the new event fields.
