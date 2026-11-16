## Context

The review lane's complexity gate (piano-developer) picks one model per tier. GENAI-375 turns the standard tier into a pool that the gate passes to the `review-code` step as JSON, plus a shadow rate. The harness picks deterministically per MR, records the pick in args so the prompt and later steps can read it, and optionally routes a shadow copy of the review to a second model whose findings are judged but never posted. The interface below is fixed with piano-developer and c2-agent; see proposal.md for the motivation.

## Interface contract (fixed)

- `modelPool`: substituted string → JSON array `[{"model","weight","effort"?,"shadowWeight"?}]`. `[]`, unresolved, or all weights 0 → inactive, `model:` applies. Entries naming a model unknown to the deployed harness are skipped with a warning; no valid entry left → inactive. Malformed JSON → step error.
- `modelSeed`: substituted string; empty → root session id + warning.
- `modelShadowRate`: substituted 0..100; invalid → 0 + warning.
- `denyTools: [globs]`: removed on top of the agent's tools/allowTools, carried in context so subagents inherit it; `struct_output` never denied; dispatch guard rejects denied calls.
- `bestEffort: true`: a failed step ends its branch without setting the run's error; its events carry `bestEffort: true`.
- Pick: weighted rendezvous hashing over sha256. Shadow when `hashUnit(seed+":shadow")*100 < rate`, shadow model among the other entries by `shadowWeight` (default weight; weight-0 entries count as 1). Effort = entry effort, else step effort, clamped with `models.ClampReasoningEffort`. Written before prompt substitution to `args.pool.<step id> = {model, effort, bucket, shadow, shadow_model, shadow_effort}`; `pool` is reserved. Every step event and flow result carries the resolved `model`.

## Goals / Non-Goals

Goals: the contract above; Kimi K3.1 and GPT-6.1 on Bedrock; config-defined Bedrock models; no behaviour change for flows without the new keys.

Non-goals: adaptive weights (a bandit) — weights are set by people from the scoreboard; a fallback model when a provider is down (separate ticket); pools on the implementation lane (client data, see Risks); a Responses-API client for GPT.

## Decisions

### D1. The pick lives in the harness, not in the gate's prompt

An LLM gate cannot be trusted to sample a distribution, and its output is not reproducible. The harness resolves the pool from the step's own keys right before the model override, so a re-run, a resume or a self-loop picks the same model for the same seed.

### D2. Weighted rendezvous hashing over sha256

`hashUnit(s)` takes the first 8 bytes of `sha256(s)` as a big-endian uint64 and maps its top 53 bits to the open interval (0, 1): `(x>>11 + 0.5) / 2^53`. Each entry with weight `w > 0` scores `w / -ln(hashUnit(seed + ":" + model))`; the highest score wins, ties going to the lexicographically smaller model id. Rendezvous hashing means adding or removing an entry only moves seeds to or from that entry, and raising one weight only moves seeds onto it, so a weight change does not reshuffle every MR. The key is the model id, not the list position, so reordering the list changes nothing. sha256 is in the standard library and is stable across Go versions and languages, so the scoreboard or a test can reproduce a pick.

Alternatives: a cumulative-weight bucket over `hashUnit(seed)` reshuffles every seed after the changed entry when one weight moves; `hash/fnv` is fine for distribution but less obviously portable.

### D3. Shadow sampling

`u = hashUnit(seed + ":shadow")`; the step is sampled when `u * 100 < rate`. `bucket = floor(u * 100)` is written to args so a person can see how close a seed was to sampling (with an integer rate, `shadow == bucket < rate`). The shadow model is a second rendezvous pick among the other usable entries, keyed `seed + ":shadow:" + model`, weighted by `shadowWeight` when set, else by `weight`, and a weight-0 entry counts as 1. That makes "weight 0, no shadowWeight" a shadow-only newcomer trial and "shadowWeight 0" a way to keep an entry out of shadows. No other usable entry with a positive shadow weight means no shadow. The harness never runs the shadow itself: it only writes `shadow`, `shadow_model` and `shadow_effort`; the flow routes on `${args.pool.<step>.shadow} == true`, which is false on a missing path, so old images, failed gates and teams without pools never take the shadow branch.

### D4. Unusable entries are skipped, malformed JSON fails

A pool comes from team configuration that can run ahead of the deployed image, so an entry naming a model this image does not know is skipped with a warning rather than failing the posting step. The same holds for entries whose provider is not configured or is disabled here (`NewAgent` would fail on them), and for an entry with an empty model, a negative or non-finite weight or an effort that is not a level. JSON that does not decode into an array of `{model: string, weight: number, ...}` objects is an authoring bug in the flow or the gate and fails the step like an unknown literal `model:` does. Literal pools, rates and seeds are checked at flow load, strictly except for unknown models (warning), and `modelSeed` / `modelShadowRate` without `modelPool` is a load error.

### D5. Effort

Entry effort, else the step's resolved `reasoningEffort`, then `models.ClampReasoningEffort`: `max` falls to `xhigh` or `high`, `xhigh` to `high`, a level the model cannot take at all (no reasoning, or not a level) to "" (provider default), with a warning when the value changed. Clamping instead of failing keeps a pool that pins `xhigh` usable across models that stop at `high`.

### D6. `args.pool.<step id>` and the reserved name

The pick is written to the step's own args map before NewAgent and before prompt substitution, so the prompt, the persisted row and every later step see it. An inactive pool removes a stale entry for the step. `pool` is reserved: a flow whose args or step output schema declares it fails to load, a caller-supplied `pool` arg is rejected, and a struct output field named `pool` is not merged into args. Without that a caller or a model could forge `shadow: true` routing.

### D7. `denyTools` rides the context

The list is put on the step's context (`tools.WithDeniedTools`) before NewAgent. `NewAgent` wraps the agent registry so `IsToolEnabled` / `IsToolExplicitlyEnabled` return false for matching tools (case-insensitive `permission.MatchWildcard`), which keeps them out of the tool set, deferred-tool search and MCP registration alike; the per-step agent cache key includes the list. The task tool builds subagents with the caller's context, so they inherit the list, and the async path carries it onto its detached context. `streamAndHandleEvents` rejects a denied call before lookup as a second line of defence (a hallucinated name, a cached tool set). `struct_output` is exempt so a schema step can always finish. Dynamic flows reject the key, as they already reject `model`.

### D8. `bestEffort`

`FlowState.BestEffort` carries the step's flag on every state. The API runner does not set the run error for a failed best-effort step, and the engine does not emit the error agent event for it, so session-owned runs do not fail either. Retries and `fallback.to` behave as on any step; without a fallback the branch simply ends.

### D9. Resolved model on every state

After NewAgent the agent's model id is put on the step context, and every `FlowState` built from then on carries it, including failures, retries and postpones. Failures before NewAgent (agent templating, pool JSON) carry none. The API adds `model` to `flow.step.*` events and `/flow/status` step records, and `cmd/flow.go` to its step results; a later state without a model does not erase one recorded earlier for the same step.

### D10. `Model.Wire` replaces `IsBedrockKimi`

Bedrock serves two body formats. `Wire` (`anthropic-messages`, the default for Bedrock, or `openai-chat`) says which; `Model.BedrockOpenAIChat()` is the one predicate the Bedrock client, `createAgentProvider` and `config.validateAgent` use. Routing by wire instead of catalog membership is what lets a config-defined Claude model on Bedrock work.

### D11. GPT-6.1 and Kimi K3.1 entries

Kimi K3.1 (`moonshotai.kimi-k3.1`, global and US profiles; no EU profile exists) joins `BedrockKimiModels`: $4/$20 per 1M, cache writes $5, reads $0.20, US +10%, 1,048,576-token context, 131,072 output, efforts up to `xhigh` and `max`, default `max`. GPT-6.1 Sol and Terra (`openai.gpt-6.1-sol|terra`, global and US) go into a new `BedrockOpenAIModels`: Sol $2/$10, cache writes $2.50, reads $0.10; Terra $1/$5/$1.25/$0.05; US +10%; 128,000 output; context 1,050,000 with prompts over 272,000 tokens billed at 2x input and 1.5x output; default effort `medium`; Sol takes `xhigh` and `max` (OpenAI's model page), Terra stops at `high` until its levels are confirmed; attachments off until probed. GPT-6.1 tool calling is documented for the Responses API, while the harness sends Chat Completions through the InvokeModel passthrough, so both GPT entries are "pending tool-call verification" until `scripts/probe_bedrock_tools.sh` passes; pools keep them at weight 0 until then.

`LongContextOutputCostMultiplier` is a new field because GPT-6.1 raises input and output by different factors; zero keeps the single multiplier for output. `DefaultReasoningEffort` is applied at config load for an agent that sets no effort (`unsetReasoningEffortDefault`, with K3's `max` as before) and by the OpenAI client on Bedrock when no effort reaches it (a step's model override resets an inherited effort the new model rejects).

### D12. OpenAI client and `xhigh`

`WithReasoningEffort` accepts `xhigh`; `preparedParams` sends it when the model has `SupportsXHighThinking`, else `high` with a warning. Before this, `xhigh` (the review lane's default) became `medium` silently.

### D13. `customModels`

A top-level array of Bedrock model definitions with every pricing and capability field, validated strictly at load (bad entries fail the load; a built-in id wins with a warning) and registered into `models.SupportedModels` right before `Validate`, so agents, flow steps and pools accept the ids like built-ins. They are removed again by `config.Reset` and a failed load. Ids must match `^bedrock\.[a-z0-9][a-z0-9._-]*$`; the schema accepts such ids for agent `model` through a third `anyOf` branch. Only Bedrock: it is the one provider whose routing is fully described by the wire format and whose credentials are already shared.

## Risks / Trade-offs

- [GPT tool calls may not work over Chat Completions on Bedrock] → entries marked pending; probe script and `bedrock_live` test; weight 0 until verified.
- [Pool models other than Claude have no EU profile] → pools are review-lane only (client code, no client data), enforced in piano-developer; the harness itself is lane-agnostic.
- [A shadow review could post] → `denyTools` on the shadow step plus the dispatch guard; piano-developer's scoreboard checks for leaked markers.
- [GPT-6.1 context window and Terra effort levels are not confirmed on the Bedrock cards] → values chosen conservatively and documented in `docs/providers.md`; revisit after the probe.
- [Custom models mutate the global catalog] → registration happens once per load, before agents are built; `Reset` restores the built-in catalog for tests.
- [A best-effort failure is easy to miss] → it is still a `failed` state and `flow.step.failed` event, flagged `bestEffort: true`.
