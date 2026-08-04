# Design

## Context

`force-struct-output-final-turn` (archived) established the mechanism and the flow-layer safety net for schema-bearing steps that end in prose. This change extends that guarantee to the two remaining escape hatches found on `TPWEBAPP-62730` (GENAI-134):

- The agent loop's **max-turns wrap-up** actively discards a `struct_output` the model produced.
- The flow-layer force guard is **not attempted** for empty or errored runs.

Both are corrected in the engine; neither is addressable by prompt text.

## Decision 1 — Schema-aware max-turns wrap-up (`internal/llm/agent/agent.go`)

Today, when `cycles > effectiveMaxTurns`, the loop:

1. injects `prompts/max_turns.md` (*"Do NOT call any more tools. Provide your final response now …"*),
2. runs one final `streamAndHandleEvents` with the default (auto) tool choice,
3. if that turn made tool calls, **discards** them (`createErrorToolResults` + `finishMessage(EndTurn)`),
4. returns `finalResult` with whatever `structOutput` was captured earlier (nil if the model never called it in a normal cycle).

For a schema-bearing run this throws away the exact output the flow needs. The new branch:

- Detects schema-bearing runs with a helper `hasStructOutputTool(toolSet)` — true iff the resolved tool set contains a tool whose `Info().Name == tools.StructOutputToolName`. This mirrors the injection gate in `internal/llm/agent/tools.go` (`Output.Schema != nil`), so it is exactly the set of runs for which forcing is possible.
- **Schema-bearing:** inject a struct-output-specific wrap-up prompt (new `prompts/max_turns_struct_output.md`: *"You have reached the maximum number of tool-use turns. Call `struct_output` now with your result, conforming exactly to the schema. Do not reply with prose and do not call any other tool."*), build `finalCtx = provider.WithForcedTool(ctx, tools.StructOutputToolName)`, run one `streamAndHandleEvents(finalCtx, …)`.
- **Capture is nil-guarded (review finding — HIGH).** `streamAndHandleEvents` returns a **nil** `toolResults` message when the final turn makes no tool call (a provider that ignores forced `tool_choice`, or any `end_turn` text response — see `agent.go` returning `assistantMsg, nil, nil` for `len(toolCalls)==0`). `captureStructOutput` dereferences its first arg, so it MUST only be called when `finalToolResults != nil` (equivalently `finalMsg.FinishReason() == message.FinishReasonToolUse`), mirroring the existing guard in the normal loop. Otherwise leave `structOutput` untouched. Skipping this guard turns the promised graceful-degradation path into a recovered-panic hard-fail.
- `finalResult` for the schema case sets `StructOutput` **only when the captured result is a non-error result** (mirroring the existing finish-on-struct_output gate); an error/absent capture leaves `StructOutput` nil so the flow layer's own guard/empty handling can retry. `finalMsg` is kept as the event `Message`. The `struct_output` tool call is **captured, not discarded**.
- **Plain (no struct_output tool):** unchanged — inject `max_turns.md`, discard stray tool calls, return the text.
- Streaming error on the forced turn: same graceful handling as today (return with the prior `structOutput`), so a provider hiccup on the wrap-up never regresses below current behavior.

Forcing already omits extended thinking and temperature in the Anthropic client family (`forced-tool-choice` capability), so no extra handling is required for reasoning-effort models.

## Decision 2 — Broaden the flow-layer force guard (`internal/flow/service.go`)

Two shapes currently bypass `forceStructOutputTurn`:

- **Empty response** — `result.StructOutput` nil/empty **and** `result.Message.Content().Text == ""`. The retry loop sets `lastErr` (*"expects structured output but agent produced empty response"*) and, after attempts, the step fails. The existing guard is skipped by its `Text != ""` precondition.
- **Errored/cancelled run** — `result.Type == AgentEventTypeError`. The retry loop sets `lastErr` and the step fails at the `lastErr != nil` return, well before the guard.

New behavior: on the `lastErr != nil` path, **after** the transient-provider postpone check (unchanged) and **before** writing the terminal `failed` state, attempt exactly one `forceStructOutputTurn` when all of:

- `!step.Interactive && step.Output != nil && step.Output.Schema != nil` (schema-bearing, non-interactive), and
- `ctx.Err() == nil` — the **parent** context is still alive, so a forced turn can actually run, and
- `!isTransientProviderError(lastErr)` — a flaky provider that already exhausted retries would only fail again; leave it to the existing failure/postpone handling.

**Bounded forcing ctx (review finding).** `forceStructOutputTurn` derives a fresh step-scoped ctx via `stepCtx`, which re-applies the step's full `Step.Timeout`. For the errored/cancelled shape that means a step that already burned its budget on a step-scoped deadline would be granted **another full budget** — re-opening the wall-clock hang this change targets. So the last-ditch attempt runs under a **short bounded ctx** — `boundedCtx, cancel := context.WithTimeout(ctx, forceStructOutputMaxWait)` (a small constant, ~2m) — passed as the parent to `forceStructOutputTurn`; because `context.WithTimeout` honors the earliest deadline, `stepCtx`'s larger `Step.Timeout` cannot extend it. A single forced `struct_output` turn (`maxTurns=1`, terminal tool call, no task spawning) fits comfortably inside that bound. The empty-response shape uses the same bounded path.

**Synthesize a fresh success event (review finding).** On the errored shape the pending `result` is an `AgentEventTypeError` event (or a zero-value `AgentEvent` from a `RunWith` start error) with `Error` set and no `StructOutput`. Mutating it in place would publish an error-typed event on `agentEvents` for a step persisted as `completed`. Instead, on a successful rescue build a fresh event: `result = agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeResponse, StructOutput: forced, Done: true}` (and clear `lastErr`), then fall through to the normal completion path (routing derives args from `forced.Content`). Otherwise fall through to the existing terminal-failure write.

The existing prose guard (success path, `lastErr == nil`, full-`stepCtx` forcing) is untouched. This deliberately supersedes the prior `flow-runtime-resume` scenario **"Empty response remains a retryable failure"**.

### Why not fix only Decision 1?

Decision 1 covers the common max-turns case at the loop level (the cheaper, in-session fix). Decision 2 is the backstop for runs that end below max-turns with empty content, and for runs that error/cancel with the job still alive. They are complementary; together they close every non-kill path to a schema-bearing step failing without a forced attempt.

## Edge cases & non-regression

- **Plain (no-schema) steps:** `hasStructOutputTool` is false and `Output.Schema` is nil, so neither decision changes their behavior.
- **Forced turn returns text (provider ignores forcing):** nil `finalToolResults` → capture skipped (Decision 1) / `forceStructOutputTurn` returns nil → graceful fallback; no panic, no hard-fail.
- **Error struct_output result at max-turns:** captured but not promoted to `finalResult.StructOutput` (kept as fallback); the flow layer's guard/empty handling then applies.
- **Hard pod kill (run 1 of GENAI-134):** out of scope — a SIGKILL mid-Run returns control to no in-process guard. Addressed separately (drain liveness / infra deadlines / orchestrator resume).
- **`ErrSessionBusy`:** `forceStructOutputTurn` already retries with bounded backoff.
- **Parent ctx already cancelled:** Decision 2 is skipped (guarded), so no doomed re-invocation; the step fails as today.

## Testing

- **Unit — agent loop (`internal/llm/agent`):** extend the scripted-provider harness so `StreamResponse` binds its ctx and reads the **exported** `provider.ForcedTool(ctx)` (a new exported wrapper over the private `forcedTool`; the harness cannot reach the unexported one). Script: never emit `struct_output` on normal turns, drive to max-turns, and on the forced final turn (ctx carries the forced-tool signal) return a `struct_output` tool call. Assert the run finishes with `StructOutput` set (captured, not discarded) and the wrap-up call happened. Add a plain-step regression (no `struct_output` tool → text wrap-up, stray tool calls discarded) and a graceful case (forced turn returns text → no panic, prior `structOutput` returned).
- **Unit — flow runner (`internal/flow`):** with the existing `stubAgent`/`runForceFlow` doubles (no DB): empty response → forced turn invoked and rescues (fresh `Response` event, step completed); errored run with parent ctx alive → forced turn invoked and rescues; parent ctx cancelled → no forced turn, terminal failure; transient provider error → postpone/failure path unchanged (no forced turn).
- **E2E (`scripts/test/struct_output.sh`, run by `make test-e2e`):** a self-contained gate that exercises BOTH decisions end-to-end through the real runtime subsystems — the real agent loop (`processGeneration`) for Decision 1 and the real `flow.Service.runStep` for Decision 2 — driven by the in-package scripted provider / stub agent (no live LLM). It is a thin `go test` wrapper on the two real-subsystem suites rather than a bespoke `cmd/` driver: the agent loop exposes no seam to inject a `provider.Provider` from an external package (`newAgent` builds its provider internally), so driving Decision 1 end-to-end requires the in-package scripted provider, and wrapping both suites covers both decisions with no stub duplication (a `cmd/` driver could only reach Decision 2). The in-package unit tests remain the authoritative correctness gate for both decisions.

## Non-goals

- Async drain liveness / per-subagent turn cap (run 1's self-deadlock).
- Job `activeDeadlineSeconds` vs turn budget; orchestrator resume of a killed step.
