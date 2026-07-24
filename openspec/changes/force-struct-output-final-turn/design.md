## Context

A non-interactive flow step may declare an `output.schema`. The flow runner (`internal/flow/service.go`) validates the agent's result: if the step has a schema but the agent produced **text and no `struct_output`**, it logs a warning and accepts the prose as a text fallback. The step's structured fields are then never populated, so conditional routing rules referencing them evaluate false and the flow silently stalls (seen on `developer-react-on-jira` `plan-to-implement`).

The `struct_output` tool IS injected for schema steps, so the model *can* call it — but under the default `tool_choice: auto` it may choose prose, especially when a long step prompt frames the task as "produce a final answer." Prompt-level nudges are probabilistic. A guarantee needs forced tool use.

Relevant existing structure:
- Flow runner: `agentSvc.RunWith(ctx, sessID, prompt, maxTurns, RunOptions{NonInteractive:true})` → `result` (`AgentEvent` with `.StructOutput`, `.Message`, `.Type`). The text-fallback branch is `internal/flow/service.go` (schema set, no struct_output, non-empty text).
- Agent: `RunWith` → `genCtx, _ := context.WithCancel(ctx)` → `processGeneration(genCtx,…)` → `streamAndHandleEvents(ctx,…)` → `provider.StreamResponse(ctx,…)` → `preparedMessages(ctx,…)`. The ctx flows end-to-end; `preparedMessages` already reads ctx values (`taskBudgetRemainingKey`).
- Provider: native Anthropic, AWS Bedrock (`bedrockClient` delegates to the anthropic client), GCP Vertex, and Moonshot/Kimi all share `anthropic.go`'s `preparedMessages` builder. `anthropic.go:430` carries a pre-existing TODO: *"Consider adding ToolChoice in case of agent having output schema set, however it limits tool calls."* SDK `v1.37.0` exposes `ToolChoiceToolParam`/`ToolChoiceUnionParam`.
- Anthropic API constraint: a forced `tool_choice` is **rejected when extended thinking is enabled** (only `auto`/`none` allowed with thinking).

## Goals / Non-Goals

**Goals:**
- Deterministically obtain `struct_output` from a schema-bearing step that would otherwise end in prose, on providers that honor forced tool choice (Anthropic, Bedrock, Vertex).
- Never regress or hard-fail when a provider ignores/rejects forced tool choice (Moonshot/Kimi worst case = current behavior).
- Keep the agentic phase untouched (`tool_choice: auto` throughout the real work); force only on a wrap-up turn.

**Non-Goals:**
- Forcing during the agentic loop (that would suppress intermediate tool use — the exact concern in the `anthropic.go:430` TODO).
- Changing interactive-step behavior (interactive steps have their own multi-turn `struct_output` flow).
- Grammar-constrained decoding / native response-format schemas.
- OpenAI and Gemini forced tool choice (left as graceful no-ops for now; they ignore the signal and fall through to the text fallback). Scope is the Anthropic client family that motivates this change.

## Decisions

**1. Force via a context value read in the provider request builder.**
Add `provider.ForceStructOutputToolKey` (a typed context key). When set to a non-empty tool name, `preparedMessages` sets `ToolChoice` to that tool and omits thinking/`OutputConfig`. Rationale: matches the established ctx-value pattern (`taskBudgetRemainingKey`, `tools.StepScopedContextKey`) and needs no change to the `Provider`/`StreamResponse` interface. *Alternative:* add a parameter to `StreamResponse`/`send` — rejected: touches the interface and every provider implementation.

**2. Flow↔agent contract is `RunOptions.ForceStructOutput bool`.**
The flow runner sets it; `agent.RunWith` translates it into the ctx value on `genCtx` before `processGeneration`. Rationale: keeps a typed, explicit boundary at flow↔agent, while the provider ctx key stays internal to the llm layer (the flow package never imports the provider ctx key). *Alternative:* flow sets the provider ctx value directly — rejected: leaks a provider-internal key across package boundaries.

**3. Disable extended thinking AND omit temperature on the forced turn.**
When the force signal is present, `preparedMessages` skips the entire thinking-selection block (leaving `thinkingParam`/`outputConfig` zero) — required because Anthropic rejects forced `tool_choice` + thinking. It MUST ALSO omit `temperature` (leave the zero `param.Opt[float64]{}`) rather than the default `anthropic.Float(0)`: the skipped thinking block is where adaptive-but-not-XHigh models (Claude 4.6 Opus/Sonnet, Kimi) would otherwise set `temperature=1`, and `Float(0)` is a non-default value that Opus 4.7+ rejects. Omitting it lets the API use its own default. Safe because the forcing turn is pure formatting of an already-decided result — no reasoning budget or temperature tuning needed. Also sidesteps Moonshot's incomplete `OutputConfig` support.

**4. One bounded wrap-up turn, feeding prose back — with a fresh step-scoped context.**
After the retry loop (once `lastErr == nil`) and before output derivation, the runner checks `!step.Interactive && step.Output != nil && no non-empty struct_output && non-empty text` and, if so, issues exactly one extra `RunWith` on the same session with `maxTurns=1`, `ForceStructOutput:true`, and a short corrective prompt ("you ended without calling struct_output; emit it now"). The model's prior prose is already in session history, so it only has to structure it. Forced `tool_choice` + the finish-on-`struct_output` short-circuit means this terminates in a single model call. Placed after the loop, it runs at most once per step execution (not multiplied by the retry budget) and does not touch the loop's `lastErr`/`break`/`continue` control flow.

The loop's step-scoped context is already cancelled by the time the loop exits (`cancelStep()` runs right after `result = <-done`), so the forcing turn MUST derive its OWN fresh step-scoped context from the parent `ctx` — mirroring the loop's `stepCtx(ctx, step)` + re-installing `tools.StepScopedContextKey` and the flow telemetry values — and cancel it when done.

**4a. Session-busy race on the forcing turn.**
`agent.RunWith` releases the session busy-lock in a deferred `activeRequests.Delete` that runs *after* it sends the terminal event on the (buffered) channel. The flow's `result = <-done` therefore unblocks slightly before the lock is released, so an immediate forcing `RunWith` can return `ErrSessionBusy`. The forcing helper MUST tolerate this: retry on `ErrSessionBusy` with a short bounded backoff (a handful of attempts over ≲300ms). If it still cannot acquire the session, treat it as the graceful-degradation path (keep the prose).

**5. Graceful degradation.**
If the forcing `RunWith` errors, or its result still has no non-empty `struct_output` (e.g. Moonshot ignored the forced choice, or the model returned a schema-invalid struct_output captured as an error fallback), the runner keeps the original prose as the text fallback — exactly today's behavior. No new failure mode.

**6. Provider coverage — Anthropic client family only.**
Implement the ctx-signal read solely in `anthropic.go`'s `preparedMessages`, which is the single builder shared by native Anthropic, AWS Bedrock (`bedrockClient.send` delegates to the anthropic child), GCP Vertex, and Moonshot/Kimi (the anthropic client pointed at Moonshot's endpoint). OpenAI and Gemini are left unchanged and simply do not read the signal, so they ignore it (a no-op) and the flow's graceful degradation covers them. Rationale: the motivating flows run on the Anthropic family; one well-understood builder covers all four transports; OpenAI's `preparedParams` doesn't take a `ctx` today, so wiring it would add surface for a provider outside the motivating scope. *Alternative:* also force on OpenAI via `applyMetadata` (which is ctx-aware) — deferred as a follow-up; graceful degradation means no regression in the meantime.

## Risks / Trade-offs

- **Moonshot/Kimi may not honor forced `tool_choice`** → Mitigation: graceful degradation (decision 5) — behaves exactly as today; no regression.
- **Forced turn still returns prose on a non-compliant provider** → Mitigation: detected as "no struct_output" → text fallback.
- **Schema-invalid `struct_output` on the forced turn** → Mitigation: `captureStructOutput` records it as an error fallback; runner treats "no non-empty content" as failure → text fallback.
- **Extra latency/cost of one more turn** → Only on the failure path (agent already ended in prose), bounded to one turn with thinking disabled (cheaper than a reasoning turn). Not on the happy path.
- **Loop risk** → Mitigation: single attempt, `maxTurns=1`; not tied to the retry counter (placed after the loop).
- **Session-busy race** → `RunWith` releases the busy-lock in a deferred `Delete` that runs after the terminal event is delivered, so `result = <-done` can return before the lock frees and an immediate forcing `RunWith` may hit `ErrSessionBusy`. Mitigation: bounded retry on `ErrSessionBusy` (decision 4a); if still busy, degrade gracefully to the prose fallback.
- **Temperature rejection on the forced turn** → skipping the thinking block would otherwise leave `temperature=Float(0)`, a non-default value Opus 4.7+ rejects and a behavior change for 4.6/Kimi (which would send `1`). Mitigation: omit temperature on the forced turn (decision 3).
- **Cancelled step-scoped context** → the loop's step ctx is cancelled before the forcing turn runs. Mitigation: the forcing turn builds its own fresh step-scoped context (decision 4).

## Migration Plan

Internal-only change (`RunOptions`, one context key, two provider builders, one flow-runner branch). No config, schema, or public-API surface changes; nothing to migrate. Rollback = revert the commit. The behavior is safe-by-default via graceful degradation, so no feature flag is introduced.

## Open Questions

- Should Gemini also force tool choice? Deferred — graceful fallback covers it; can be a follow-up if flows start running schema steps on Gemini.
- Corrective-prompt wording is fixed in the flow runner for now; if it proves brittle across models it could move to a per-flow/per-step override, but that is out of scope here.
