## ADDED Requirements

### Requirement: Schema-bearing step that ends in prose triggers a forcing wrap-up turn

When a non-interactive flow step declares an `output.schema` and the step's agent completes with a **non-empty text response but no `struct_output`**, the flow runner SHALL issue exactly one additional bounded "forcing" turn before accepting the text as a fallback. The forcing turn MUST re-invoke the same step agent and session with the `struct_output` tool forced (via the provider forced-tool-choice mechanism) and MUST be bounded to a single turn.

If the forcing turn yields a non-empty `struct_output`, the runner MUST use it as the step result so downstream conditional routing rules see the structured fields. If the forcing turn errors or still yields no non-empty `struct_output`, the runner MUST fall back to the original text response (the pre-existing behavior) without failing the step.

The forcing turn MUST NOT run during the step's normal agentic turns (which keep the provider's default tool choice), MUST be attempted at most once per step execution, and MUST NOT apply to interactive steps.

#### Scenario: Prose result is upgraded to struct_output

- **WHEN** a schema-bearing non-interactive step's agent ends with a non-empty text response and no `struct_output`
- **THEN** the runner issues one forcing turn with `struct_output` forced on the same session
- **AND** on receiving a non-empty `struct_output`, uses it as the step result

#### Scenario: Forcing turn is best-effort with graceful degradation

- **WHEN** the forcing turn errors, or still returns no non-empty `struct_output` (e.g. a provider that does not honor forced tool choice)
- **THEN** the runner falls back to the original text response
- **AND** the step does not fail solely because `struct_output` was not produced

#### Scenario: Empty response remains a retryable failure

- **WHEN** a schema-bearing step's agent produces neither `struct_output` nor any text
- **THEN** the runner treats it as the existing retryable failure (no forcing turn is required for the empty case)

#### Scenario: Forcing turn does not loop

- **WHEN** the forcing turn itself ends without `struct_output`
- **THEN** the runner does NOT issue any further forcing turns for that step execution
