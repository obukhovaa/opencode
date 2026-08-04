## 1. Agent-loop max-turns forcing (`internal/llm/agent/agent.go`)

- [x] 1.1 Add a `hasStructOutputTool(toolSet []tools.BaseTool) bool` helper — true iff a tool's `Info().Name == tools.StructOutputToolName`.
- [x] 1.2 Add `prompts/max_turns_struct_output.md` instructing the model to call `struct_output` now (conforming to the schema, no prose, no other tool). Keep `prompts/max_turns.md` for plain runs.
- [x] 1.3 In the max-turns branch (`cycles > effectiveMaxTurns`): when `hasStructOutputTool(toolSet)`, inject the struct_output wrap-up prompt and run the final `streamAndHandleEvents` with `provider.WithForcedTool(ctx, tools.StructOutputToolName)`.
- [x] 1.4 **Nil-guard the capture:** only call `captureStructOutput(finalToolResults, …)` when `finalToolResults != nil`; a forced turn that returns text yields a nil tool-results message and capturing it would panic. Set `finalResult.StructOutput` only for a non-error capture; keep `finalMsg` as the event Message. Preserve the existing streaming-error handling.
- [x] 1.5 Keep the existing text wrap-up (inject `max_turns.md`, discard stray tool calls) for the plain (no struct_output tool) case.
- [x] 1.6 Add an exported `provider.ForcedTool(ctx) string` wrapping the unexported `forcedTool`, so callers/tests observe the forced-tool signal.
- [x] 1.7 **Exempt the forced tool from loop detection** (`streamAndHandleEvents`): `provider.ForcedTool(ctx) != toolCall.Name && tracker.Track(...)`, so the guaranteed terminal wrap-up call is never blocked by the loop detector.
- [x] 1.8 Agent unit tests: schema-bearing max-turns forces & captures; plain run stays text & unforced; forced turn returning text does not panic; forced turn returning a schema-rejected struct_output is NOT promoted.

## 2. Flow force-guard broadening (`internal/flow/service.go`)

- [x] 2.1 Add a `forceStructOutputMaxWait` constant (~2m). On the `lastErr != nil` path, after the transient-provider postpone check and before the terminal `failed` write, attempt one `forceStructOutputTurn` under a **bounded** ctx (`context.WithTimeout(ctx, forceStructOutputMaxWait)`) when `!step.Interactive && step.Output != nil && step.Output.Schema != nil && ctx.Err() == nil && !isTransientProviderError(lastErr)`.
- [x] 2.2 On a non-empty, non-error result: synthesize a fresh `agentpkg.AgentEvent{Type: AgentEventTypeResponse, StructOutput: forced, Done: true}`, clear `lastErr`, fall through to completion; otherwise fall through to the terminal-failure write. Do NOT mutate the pending error event in place.
- [x] 2.3 Leave the existing success-path prose guard unchanged.
- [x] 2.4 Flow unit tests (existing `stubAgent`/`runForceFlow`, no DB): empty → rescued; errored + parent ctx alive → rescued (fresh Response event); parent ctx cancelled → no forcing turn, terminal failure; transient provider error → no forcing turn. Assert the forcing turn was invoked with `ForceStructOutput==true` under a bounded (deadline) ctx (updated `stubAgent` to record per-call `RunOptions` + ctx-deadline). Updated the pre-existing `TestRunStepStructOutputValidation` empty cases to the new "force-attempted then fails" contract.

## 3. End-to-end gate (`make test-e2e`)

- [x] 3.1 Add `scripts/test/struct_output.sh` (background.sh style: colored PASS/FAIL, self-contained, no live LLM) that exercises BOTH decisions end-to-end through the real runtime subsystems — the real agent loop (`processGeneration`) for Decision 1 and the real `flow.Service.runStep` for Decision 2 — via the in-package scripted provider / stub agent.
- [x] 3.2 It wraps `go test` on the two real-subsystem suites rather than a bespoke `cmd/` driver: the agent loop exposes no seam to inject a `provider.Provider` from an external package, so driving Decision 1 end-to-end needs the in-package scripted provider; wrapping both suites covers both decisions with no stub duplication.
- [x] 3.3 Confirmed the script is picked up by `make test-e2e` (which loops `scripts/test/*.sh`).

## 4. Verification

- [x] 4.1 `go build ./...` is clean.
- [x] 4.2 `go test ./internal/llm/agent/ ./internal/flow/ ./internal/llm/provider/` passes (authoritative gate for both decisions).
- [x] 4.3 `scripts/test/struct_output.sh` passes.
- [x] 4.4 `openspec validate struct-output-terminal-guarantee --strict` passes.
