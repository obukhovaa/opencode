## Why

A non-interactive flow step that declares an `output.schema` must end with a `struct_output` call — the flow derives its routing args from that JSON, so a step that finishes any other way strands the flow. The `force-struct-output-final-turn` change added a flow-layer safety net for one shape of this failure (the agent ends in **prose**). Two other shapes still slip through, both observed on prod (`developer-react-on-jira`, session `TPWEBAPP-62730`, GENAI-134):

1. **Max-turns discards a struct_output the model *did* produce.** When the agentic loop hits `maxTurns`, it injects `prompts/max_turns.md` — *"Do NOT call any more tools. Provide your final response now"* — and then **discards** any tool call the model makes on that turn (`internal/llm/agent/agent.go`). For a schema-bearing step this is backwards: the model correctly tried to call `struct_output`, and the runtime threw it away, leaving an empty-text terminal message. The step was then recorded FAILED with *"expects structured output but agent produced empty response"*.

2. **The flow-layer force guard never fires for empty or errored runs.** `forceStructOutputTurn` is gated on `result.Message.Content().Text != ""` and sits only on the success path (after `lastErr == nil`). So an empty response (nil `struct_output` **and** empty text) and an errored/cancelled run both bypass it and go straight to terminal failure — even when the parent context is still alive and one more forced turn would have produced the output.

Prompt-level guidance cannot close either gap: in case 1 the model already emitted `struct_output` and the engine discarded it; in case 2 no prompt runs at all. The guarantee has to live in the engine.

## What Changes

- **Agent loop, max-turns wrap-up (`agent.go`):** when the run's tool set contains `struct_output` (a schema-bearing step), the final max-turns turn FORCES `tool_choice=struct_output` (reusing the existing forced-tool-choice mechanism, which already disables extended thinking) and CAPTURES the result, instead of requesting prose and discarding the model's `struct_output` call. Plain (no-schema) runs keep the existing text wrap-up.
- **Flow runner force guard (`internal/flow/service.go`):** broaden the bounded forcing wrap-up turn so it is also attempted before terminal failure for
  - an **empty** response (nil/empty `struct_output` and empty text), and
  - an **errored/cancelled** run, **only while the parent context is still alive** and the error is not a transient provider error already handled by the postpone path.
  Still best-effort and bounded to one attempt. The last-ditch attempt runs under a short bounded ctx (not a fresh full `Step.Timeout`) so a step that already timed out cannot re-hang, and a successful rescue is published as a fresh `Response` event (not the in-place error event). On no result it falls through to the existing failure/fallback behavior. This supersedes the prior "Empty response remains a retryable failure" behavior.

## Capabilities

### Modified Capabilities

- `forced-tool-choice`: gains a second in-engine forcing site. Beyond the flow runner's post-Run forcing turn, the **agent loop itself** now forces `struct_output` on the max-turns wrap-up turn for schema-bearing runs, and must not discard a `struct_output` produced there. The provider request-builder mechanism is unchanged.
- `flow-runtime-resume`: the step-execution contract's forcing wrap-up turn is broadened from "ends in prose" to also cover the **empty-response** and **errored/cancelled (parent-ctx-alive)** shapes, before the step is recorded as failed. This supersedes the prior "Empty response remains a retryable failure" behavior.

## Impact

- **Code:**
  - `internal/llm/agent/agent.go` — the max-turns wrap-up branch splits on whether `struct_output` is in the tool set; schema-bearing runs get a forced, captured wrap-up turn; a small `hasStructOutputTool` helper.
  - `internal/llm/agent/prompts/` — a new max-turns prompt variant that instructs the model to emit `struct_output` (schema case); the existing `max_turns.md` is retained for plain runs.
  - `internal/flow/service.go` — a last-ditch forcing attempt on the `lastErr != nil` path (empty/errored, parent-ctx-alive, non-transient), reusing `forceStructOutputTurn`; the existing prose guard is unchanged.
- **APIs:** internal only. No config/public surface change.
- **Behavior:** deterministic `struct_output` for schema-bearing steps at max-turns and on empty/errored-but-recoverable runs, on providers that honor forced tool choice; unchanged behavior for plain steps and for providers that ignore forced tool choice (graceful degradation, = today).
- **Tests:** agent-loop unit test (max-turns forced+captured, plus a plain-step regression); flow-runner unit tests (empty → forced rescue; errored+parent-ctx-alive → forced rescue; parent-ctx-dead → no attempt); a new `scripts/test/struct_output.sh` e2e driving both the agent-loop and flow-layer paths against a scripted provider (no LLM), run by `make test-e2e`.
