## Why

When a non-interactive flow step declares an `output.schema` but the agent ends its turn with prose and never calls `struct_output`, the flow logs a warning and accepts the text as a fallback (`internal/flow/service.go`). The step's structured fields are then never populated, so conditional routing rules referencing those fields all evaluate false and the flow silently stalls — observed repeatedly on `developer-react-on-jira`'s `plan-to-implement` step, where the agent posts a prose "plan complete" summary and the flow stops instead of advancing to `implement`.

Prompt-level nudges (a system-prompt instruction and per-step guidance) have proven insufficient: an available tool under the default `tool_choice: auto` is called at the model's discretion, and a long, task-specific step prompt can out-weigh a generic "you must call struct_output" line. A guarantee requires a mechanism, not more prompt text.

## What Changes

- Add a **forcing wrap-up turn** to the flow runner: when a schema-bearing step returns a non-empty text response with no `struct_output`, re-invoke the step's agent **once** with the `struct_output` tool forced via `tool_choice`, feeding the prior prose back so the model only has to structure it.
- Add provider-layer support to **force a specific tool** on a single request, threaded from the flow runner without affecting the preceding agentic turns (which keep `tool_choice: auto`).
- On the forcing turn, **disable extended thinking** for that request, since the Anthropic Messages API rejects a forced `tool_choice` while thinking is enabled.
- **Graceful degradation:** if the forcing turn errors, or the provider still returns no `struct_output` (e.g. Moonshot/Kimi's Anthropic-compatible endpoint may not honor forced `tool_choice`), fall back to the existing text-fallback behavior. No hard failure, no regression.
- Bound the forcing turn to a single attempt so it cannot loop.

## Capabilities

### New Capabilities
- `forced-tool-choice`: provider request-builder ability to force a single named tool (`struct_output`) on one request, with extended thinking disabled for that request. Implemented for the Anthropic client family — native Anthropic, AWS Bedrock, GCP Vertex, and Moonshot/Kimi, which all share the Anthropic request builder. Providers outside that family (OpenAI, Gemini) ignore the signal without erroring; the flow's graceful degradation (text fallback) covers them, so there is no regression and no requirement that they force.

### Modified Capabilities
- `flow-runtime-resume`: this capability owns how the flow runner executes a step (it already specs the `agent.RunWith` invocation, `NonInteractive`, per-step timeout, and step-scoped context). Its step-execution contract gains a new behavior — a schema-bearing step that ends in prose triggers a bounded forcing wrap-up turn before the text fallback is accepted, so structured output is obtained deterministically on compliant providers.

## Impact

- **Code:**
  - `internal/flow/service.go` — the step retry/validation loop gains the forcing wrap-up turn between "text produced, no struct_output" detection and accepting the text fallback.
  - `internal/llm/agent/agent.go` — `RunOptions` gains a flag to force `struct_output`; the agent threads it to the provider request (via context value, matching the existing `taskBudgetRemainingKey` / step-scoped-context pattern).
  - `internal/llm/provider/anthropic.go` — `preparedMessages` sets `ToolChoice` to the forced tool and omits thinking/`OutputConfig` when the force signal is present (covers Anthropic, Bedrock, Vertex, Kimi).
  - `internal/llm/provider/` — OpenAI and Gemini builders are left unchanged; not reading the signal means they ignore it (no-op), which is the intended best-effort behavior.
- **APIs:** internal only (`RunOptions`, a new context key). No public/config surface change.
- **Behavior:** deterministic structured output on Anthropic/Bedrock/Vertex; unchanged behavior on providers that ignore/reject forced tool choice (Kimi worst case, and OpenAI/Gemini, = today).
- **Tests:** flow-runner forcing-then-fallback paths; provider request-builder assertions that the force signal sets `tool_choice` and drops thinking.
