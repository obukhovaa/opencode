## Why

The `struct_output` tool (`internal/llm/tools/struct_output.go`) only checked that its input was syntactically valid JSON — it never reconciled the payload against the schema. Two consequences, both of which silently strand flows:

1. **Omitted defaulted fields stay absent.** Models routinely drop empty-valued fields that carry a `default`; an empty array like `"blockers": []` is the most common. The stored output then has no `blockers` key, so a flow routing rule that keys off `${args.blockers}` sees a MISSING key — every predicate (`sizeof … != 0`, `sizeof … == 0`) evaluates false, no rule matches, and the step completes with no transition. Observed on `developer-react-on-jira`'s `plan-to-implement` step (MICRO-1024): the agent emitted `struct_output` with `{estimation, plan, tech_spec}`, omitted the required-with-default `blockers`, and the flow stopped instead of advancing to `implement`. The job was marked `completed` having produced only a plan.

2. **Missing required fields are accepted.** A `required` field with no default that the model omitted was stored as-is (`is_error=false`), threading an incomplete output downstream. This was explicitly called out as an out-of-scope follow-up in the `force-struct-output-final-turn` change's design notes ("the identical silent-strand failure mode").

The prior `force-struct-output-final-turn` change guaranteed that `struct_output` is *called* (vs. the model ending in prose). This change guarantees that once called, its payload is *schema-conformant* before it is stored and routed on.

## What Changes

- **Materialize declared defaults.** `struct_output.Run` fills in a deep copy of any schema `default` for a property the model omitted, recursively descending into present nested objects. A required field that declares a default is thereby satisfied without a round-trip.
- **Enforce required fields.** After defaults are applied, a `required` field that is still absent (no default to fall back on) is rejected with an error naming the missing field(s). The agent loop re-enters on an error `struct_output` result (`agent.go` finishes the run only on a non-error struct output), so the model retries with a complete payload instead of the flow persisting a half-filled output.
- **Preserve existing behavior otherwise.** Provided values are never overwritten by a default; non-object / property-less "wrapper" schemas skip the new processing and pass through exactly as before; syntactically-invalid JSON still returns the existing `Invalid JSON` error.

## Capabilities

### New Capabilities
- `structured-output`: the `struct_output` tool's schema-conformance contract — materialize declared defaults for omitted fields, and reject a call missing a required field that has no default so the model retries. Captures behavior that previously lived only in `docs/structured-output.md` with no spec.

## Impact

- **Code:** `internal/llm/tools/struct_output.go` — `Run` gains a default-materialization + required-enforcement pass and four unexported helpers (`objectProperties`, `applyDefaults`, `missingRequiredFields`, `cloneJSONValue`). No signature or interface change.
- **APIs:** internal only. No config, `.opencode.json` schema, or public-API surface change.
- **Behavior:** flow steps whose model omits a defaulted routing field (e.g. `blockers`) now route deterministically instead of stranding; steps whose model omits a required-no-default field now retry instead of storing an incomplete output. Compliant, already-complete `struct_output` calls are unaffected.
- **Docs:** `docs/structured-output.md` gains a "Schema conformance" section.
- **Tests:** `internal/llm/tools/struct_output_test.go` — default materialization (the `blockers` reproduction), required-with-default not rejected, required-without-default rejected, multiple-missing sorted message, default-does-not-override-present, nested defaults, and schema-aliasing safety.
