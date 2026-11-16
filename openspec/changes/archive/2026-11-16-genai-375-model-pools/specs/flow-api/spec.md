## ADDED Requirements

### Requirement: Flow steps pick their model from a weighted pool
A flow step SHALL accept `modelPool`, `modelSeed` and `modelShadowRate`, each substituted with `${args.*}` / `${step.*}` like `model`. `modelPool` SHALL resolve to a JSON array of entries `{"model": string, "weight": number, "effort"?: string, "shadowWeight"?: number}`. The pool SHALL be inactive, and the step's `model` / `reasoningEffort` SHALL apply as before, when the value is empty, unresolved, `[]`, has no usable entry, or every usable entry has weight 0. An entry SHALL be unusable, and be skipped with a warning, when its model is not in the deployed catalog (built-in or `customModels`), its model is empty or repeats an earlier entry's, its weight or shadow weight is negative or not finite, or its effort is not a reasoning level. A value that does not decode into such an array SHALL fail the step. `modelSeed` SHALL default to the flow's root session id, with a warning, when empty or unresolved. `modelShadowRate` SHALL be a number from 0 to 100; anything else SHALL count as 0 with a warning, and an absent rate is 0.

The primary entry SHALL be chosen by weighted rendezvous hashing: `hashUnit(s)` maps the first 8 bytes of `sha256(s)`, read as a big-endian unsigned integer `x`, to `((x >> 11) + 0.5) / 2^53`; each usable entry with weight `w > 0` scores `w / -ln(hashUnit(seed + ":" + model))`; the highest score wins and ties go to the smaller model id. The step SHALL be shadow-sampled when `hashUnit(seed + ":shadow") * 100 < rate` and at least one other usable entry has a positive shadow weight; the shadow entry SHALL be chosen the same way among the other usable entries with keys `seed + ":shadow:" + model`, weighted by `shadowWeight` when set, else by `weight`, a weight of 0 counting as 1. An entry's effort SHALL be its own `effort`, else the step's resolved `reasoningEffort`, clamped to the levels its model accepts (`max` → `xhigh` → `high`; a model that cannot reason, or a value that is not a level, → empty).

When the pool is active the runtime SHALL write `args.pool.<step id>` before building the agent and before rendering the prompt, as `{"model", "effort", "bucket", "shadow", "shadow_model", "shadow_effort"}` where `bucket` is `floor(hashUnit(seed + ":shadow") * 100)`, `shadow` is a boolean and the shadow fields are empty when `shadow` is false, and SHALL run the step on the picked model and effort. When the pool is inactive the runtime SHALL remove `args.pool.<step id>` if present. The keys SHALL be inheritable through `extends` and rejected in dynamic flows. Literal values SHALL be checked at load: a literal pool must decode and every entry must be well formed (an unknown model only warns), a literal rate must be a number from 0 to 100, and `modelSeed` or `modelShadowRate` without `modelPool` SHALL fail the load.

#### Scenario: Deterministic pick
- **WHEN** a step with an active pool runs twice with the same seed and the same usable entries
- **THEN** both runs pick the same model and effort and write the same `args.pool.<step id>`

#### Scenario: Adding an entry moves seeds only onto it
- **WHEN** an entry is added to a pool
- **THEN** every seed either keeps its previous pick or moves to the new entry

#### Scenario: Prompt reads the pick
- **WHEN** a pool step's prompt contains `${args.pool.<step id>.model}`
- **THEN** the rendered prompt carries the picked model id

#### Scenario: Unknown model in the pool
- **WHEN** a pool names a model this build does not know next to a known one
- **THEN** the unknown entry is skipped with a warning and the step runs on the known model

#### Scenario: Malformed pool
- **WHEN** `modelPool` resolves to text that is not a JSON array of entry objects
- **THEN** the step fails and its `fallback` applies

#### Scenario: Shadow-only newcomer
- **WHEN** a pool has a weight-100 entry and a weight-0 entry without `shadowWeight`, and the seed is sampled
- **THEN** the primary pick is the weight-100 model and `shadow_model` is the weight-0 model

#### Scenario: Routing on the shadow flag
- **WHEN** a rule reads `if: ${args.pool.review-code.shadow} == true` and the `review-code` step had no active pool
- **THEN** the predicate is false and the rule does not fire

### Requirement: pool is a reserved flow arg
The runtime SHALL own the `pool` arg. A flow whose `args` schema or step `output.schema` declares a top-level `pool` property SHALL fail to load, a run whose caller args contain `pool` SHALL be refused as invalid flow args, and a `pool` field in a step's structured output SHALL NOT be merged into args.

#### Scenario: Forged pool arg
- **WHEN** a caller starts a flow with `args.pool = {"review-code": {"shadow": true}}`
- **THEN** the run is refused with an invalid-flow-args error

### Requirement: Flow steps can deny tools
A flow step SHALL accept `denyTools`, a list of tool-name globs (case-insensitive, `*` wildcards as in agent `tools`). The step's agent SHALL be built without matching tools, on top of its own `tools` / `allowTools`, and every subagent the step spawns (synchronous or async `task`) SHALL inherit the list. A call to a denied tool that still reaches dispatch SHALL be answered with an error result and not executed. `struct_output` SHALL never be denied. Blank entries SHALL fail the load, the key SHALL be inheritable through `extends`, and dynamic flows SHALL reject it.

#### Scenario: Denied MCP tool
- **WHEN** a step declares `denyTools: ["gitlab_create_*"]` and its agent has the GitLab MCP server
- **THEN** no `gitlab_create_*` tool is offered to the step's agent or to its subagents

#### Scenario: struct_output survives a catch-all
- **WHEN** a schema step declares `denyTools: ["*"]`
- **THEN** its agent still has `struct_output`

### Requirement: Best-effort flow steps
A flow step with `bestEffort: true` SHALL behave like any other step while it runs (retries and `fallback.to` included), but its failure SHALL NOT set the run's error: the run's terminal status is decided by the other steps. Its `failed` state SHALL still be recorded and published as `flow.step.failed`, and every `flow.step.*` event of the step and its `/flow/status` record SHALL carry `bestEffort: true`.

#### Scenario: Failed best-effort branch
- **WHEN** the only failing step of a run is a best-effort step on a parallel branch
- **THEN** the run ends `completed` and the step's `flow.step.failed` event carries `bestEffort: true`

### Requirement: Step events carry the resolved model
Once a step's agent is built, every state the step publishes SHALL carry the model id the agent runs on. `flow.step.started`, `flow.step.completed`, `flow.step.failed`, `flow.step.postponed`, `flow.step.retrying` and `flow.waiting_for_input` SHALL carry it as `model`, `/flow/status` step records SHALL carry `model`, and the CLI flow result SHALL carry `model` per step. A step that fails before its agent is built carries no model.

#### Scenario: Pool step reports its pick
- **WHEN** a pool step completes on `bedrock.us-kimi-k3.1`
- **THEN** its `flow.step.completed` event carries `"model": "bedrock.us-kimi-k3.1"`
