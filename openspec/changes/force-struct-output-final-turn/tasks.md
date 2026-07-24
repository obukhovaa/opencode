## 1. Provider forced-tool-choice mechanism

- [ ] 1.1 Add an exported context key `ForceStructOutputToolKey` (typed struct key) in the `provider` package, documented as "non-empty tool name ⇒ force tool_choice + disable thinking for this request; best-effort; only the Anthropic client family reads it".
- [ ] 1.2 In `anthropic.go` `preparedMessages`: read the forced-tool name from ctx; when non-empty, skip the thinking-selection block (leave `thinking`/`OutputConfig` zero), **omit temperature** (leave the zero `param.Opt[float64]{}`, not `Float(0)`, so Opus 4.7+/4.6/Kimi don't get a rejected or off-default value), and set `MessageNewParams.ToolChoice` via `anthropic.ToolChoiceParamOfTool(name)`.
- [ ] 1.3 Confirm OpenAI/Gemini builders do NOT read the signal (no change needed) so they ignore it without erroring.
- [ ] 1.4 Provider unit tests: (a) anthropic sets `ToolChoice` and omits `thinking`/`OutputConfig` when forced, even for a reasoning-effort model; (b) anthropic leaves `ToolChoice` unset and preserves thinking when the signal is absent.

## 2. Agent RunOptions plumbing

- [ ] 2.1 Add `ForceStructOutput bool` to `agentpkg.RunOptions` with a doc comment (flow↔agent contract; translated into the provider ctx signal).
- [ ] 2.2 In `agent.RunWith`, when `opts.ForceStructOutput` is set, wrap `genCtx` with `context.WithValue(genCtx, provider.ForceStructOutputToolKey, tools.StructOutputToolName)` before `processGeneration`.
- [ ] 2.3 Confirm the forced turn terminates in one model call (forced `struct_output` + existing finish-on-struct_output short-circuit); no change needed if it already does.

## 3. Flow runner forcing wrap-up turn

- [ ] 3.1 In `internal/flow/service.go`, AFTER the retry loop (`doneRetry`, once `lastErr == nil`) and BEFORE output derivation (~line 742), add a helper that issues exactly one `RunWith` on the same session with `maxTurns=1`, `RunOptions{NonInteractive:true, ForceStructOutput:true}`, and a short corrective prompt. The helper MUST build its OWN fresh step-scoped context from the parent `ctx` (the loop's step ctx is already cancelled), mirroring `stepCtx(ctx, step)` + re-installing `tools.StepScopedContextKey`, and cancel it when done. It MUST retry on `agentpkg.ErrSessionBusy` with a short bounded backoff (busy-lock release races the terminal event).
- [ ] 3.2 If the forcing turn returns a non-empty, non-error `struct_output`, copy it onto `result.StructOutput` (preserving the original prose `Message`); otherwise keep the original text-fallback `result` (graceful degradation) and log at warn.
- [ ] 3.3 Gate the forcing turn on `!step.Interactive && step.Output != nil && (result.StructOutput == nil || result.StructOutput.Content == "") && result.Message.Content().Text != ""`, so it runs at most once per step execution, is skipped for the empty-response case, and never runs for interactive steps.
- [ ] 3.4 Flow tests: (a) agent returns prose then struct_output on the forced turn ⇒ step result is the struct_output; (b) agent returns prose and the forced turn yields no struct_output ⇒ step falls back to the text result without failing.

## 4. Verification

- [ ] 4.1 `go build ./...` is clean.
- [ ] 4.2 `go test ./internal/llm/provider/ ./internal/llm/agent/ ./internal/flow/` passes.
- [ ] 4.3 `openspec validate force-struct-output-final-turn --strict` passes.
