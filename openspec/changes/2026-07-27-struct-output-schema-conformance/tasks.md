## 1. Schema-conformance pass in struct_output.Run

- [x] 1.1 Add `objectProperties(schema)` helper that returns the `properties` map (keying off its presence, not `type`, so `["object","null"]` unions work) and `ok=false` for non-object / property-less schemas.
- [x] 1.2 Add `applyDefaults(schema, data)` that recursively materializes a deep copy of each omitted property's `default`, descending into present nested objects; never overwrites a provided value.
- [x] 1.3 Add `missingRequiredFields(required, data)` returning the still-absent required keys, sorted for a stable error message.
- [x] 1.4 Add `cloneJSONValue(v)` deep-copy so a materialized default never aliases the reused schema map.
- [x] 1.5 In `Run`, after JSON unmarshal: when the schema is an object-with-properties, apply defaults then reject with `NewTextErrorResponse` naming any still-missing required field(s); otherwise pass through unchanged.

## 2. Tests

- [x] 2.1 `TestStructOutputTool_Run_MaterializesOmittedDefault` — the MICRO-1024 reproduction: omit required-with-default `blockers` ⇒ no error, output carries `blockers: []`, other fields preserved.
- [x] 2.2 `TestStructOutputTool_Run_RejectsMissingRequiredWithoutDefault` — omit required-no-default ⇒ error naming the field, not the present one.
- [x] 2.3 `TestStructOutputTool_Run_MultipleMissingRequiredSorted` — all-missing ⇒ deterministic sorted list.
- [x] 2.4 `TestStructOutputTool_Run_DefaultDoesNotOverridePresent` — provided value survives.
- [x] 2.5 `TestStructOutputTool_Run_NestedDefaults` — default filled inside a present nested object.
- [x] 2.6 `TestApplyDefaults_DoesNotAliasSchema` — mutating a materialized default does not leak into the next call.

## 3. Docs

- [x] 3.1 `docs/structured-output.md` — add a "Schema conformance" subsection describing default materialization and required enforcement.

## 4. Verification

- [x] 4.1 `go build ./...` is clean.
- [x] 4.2 `go test ./internal/llm/tools/ ./internal/llm/agent/ ./internal/flow/ ./internal/llm/prompt/ ./internal/message/` passes.
- [x] 4.3 `openspec validate 2026-07-27-struct-output-schema-conformance --strict` passes.
