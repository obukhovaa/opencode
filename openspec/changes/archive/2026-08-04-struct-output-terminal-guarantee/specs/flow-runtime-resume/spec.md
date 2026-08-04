## MODIFIED Requirements

### Requirement: Schema-bearing step that ends in prose triggers a forcing wrap-up turn

When a non-interactive flow step declares an `output.schema` and the step's agent finishes without a usable `struct_output`, the flow runner SHALL issue exactly one additional bounded "forcing" turn — re-invoking the same step agent and session with the `struct_output` tool forced (via the provider forced-tool-choice mechanism), bounded to a single turn — before the step's result is accepted or the step is failed. This applies to three finish shapes:

1. **Prose:** a non-empty text response with no `struct_output` (the original behavior). The forcing turn is attempted on the success path before the text fallback is accepted.
2. **Empty:** neither `struct_output` nor any text. The forcing turn is attempted before the step is recorded as failed.
3. **Errored/cancelled:** the agent run returned an error, **and** the parent context is still alive (`ctx.Err() == nil`), **and** the error is not a transient provider error already handled by the postpone path. The forcing turn is attempted before the step is recorded as failed.

For the empty and errored/cancelled shapes the forcing turn MUST run under a short bounded deadline (not a fresh full step timeout), so a step that already exhausted its step-scoped budget cannot re-hang for another full budget. On a successful rescue of an errored/empty run the runner MUST publish a fresh successful (`Response`-typed) result carrying the `struct_output`, not the pending error result.

If the forcing turn yields a non-empty `struct_output`, the runner MUST use it as the step result so downstream conditional routing rules see the structured fields (clearing the pending error for the empty/errored shapes). If the forcing turn errors or still yields no non-empty `struct_output`, the runner MUST fall back to the prior behavior for that shape — the text fallback for the prose shape, or the terminal failure for the empty/errored shapes — without any additional forcing turn.

The forcing turn MUST NOT run during the step's normal agentic turns (which keep the provider's default tool choice), MUST be attempted at most once per step execution, MUST NOT apply to interactive steps, and MUST NOT be attempted when the parent context is already cancelled (a forced turn could not run).

#### Scenario: Prose result is upgraded to struct_output

- **WHEN** a schema-bearing non-interactive step's agent ends with a non-empty text response and no `struct_output`
- **THEN** the runner issues one forcing turn with `struct_output` forced on the same session
- **AND** on receiving a non-empty `struct_output`, uses it as the step result

#### Scenario: Empty result triggers a forcing turn before failing

- **WHEN** a schema-bearing non-interactive step's agent produces neither `struct_output` nor any text, and the parent context is still alive
- **THEN** the runner issues one forcing turn with `struct_output` forced on the same session before recording any failure
- **AND** on receiving a non-empty `struct_output`, uses it as the step result and does not fail the step

#### Scenario: Errored run with the parent context alive triggers a forcing turn

- **WHEN** a schema-bearing non-interactive step's agent run returns an error that is not a transient provider error, and the parent context is still alive
- **THEN** the runner issues one forcing turn with `struct_output` forced on the same session before recording the failure
- **AND** on receiving a non-empty `struct_output`, uses it as the step result and does not fail the step

#### Scenario: Parent context already cancelled skips the forcing turn

- **WHEN** a schema-bearing step's run has failed and the parent context is already cancelled
- **THEN** the runner MUST NOT attempt a forcing turn
- **AND** records the terminal failure as before

#### Scenario: Graceful fallback when the forcing turn yields nothing

- **WHEN** the forcing turn errors, or still returns no non-empty `struct_output` (e.g. a provider that does not honor forced tool choice)
- **THEN** the runner falls back to the prior behavior for that shape (text fallback for prose; terminal failure for empty/errored)
- **AND** the step does not gain a second forcing turn

#### Scenario: At most one forcing turn per step execution

- **WHEN** the forcing turn itself ends without `struct_output`
- **THEN** the runner does NOT issue any further forcing turns for that step execution
