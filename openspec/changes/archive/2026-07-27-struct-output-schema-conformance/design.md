## Context

`struct_output.Run` previously did `json.Unmarshal` → `json.MarshalIndent` with no schema awareness. The flow runner derives routing args from the tool's RETURNED content (`internal/flow/service.go`: `output = result.StructOutput.Content` → `json.Unmarshal` → `maps.Copy(args, structData)` → `resolveNextSteps(..., args, ...)`), so anything absent from the returned JSON is absent from `args`. This is why an omitted defaulted field strands routing rather than merely losing a value.

## Decisions

**1. Fix at the tool boundary, not the flow router.**
The `force-struct-output-final-turn` design suggested widening the flow gate to also re-force on `result.StructOutput.IsError`. Fixing in `struct_output.Run` is more fundamental and covers every consumer (flows, CLI `-f json_schema`, per-agent output), not just the flow runner. It also fixes the COMMON case (omitted default) without any retry at all — the default is materialized inline — where a gate-widening approach would still need a re-force round-trip.

**2. Apply defaults BEFORE checking required.**
A field can be both `required` and carry a `default` (the `blockers` case). Materializing defaults first means such a field is satisfied without a rejection; only a required field with genuinely no default surfaces as missing. This is the least surprising behavior and matches JSON-Schema intent (a default is the value used when none is supplied).

**3. Reject via an error `ToolResponse`, not a hard Go error.**
Returning `NewTextErrorResponse` (not `return _, err`) makes the missing-required case an `is_error=true` tool result. `message.StructOutput()` reports `ok=false` for an error result, so the agent loop's finish condition (`structOutput != nil && !structOutputIsErr`) is not met and it `continue`s — the model sees the error text and retries within the same run, bounded by `maxTurns`. This reuses the existing retry channel; no new control flow.

**4. Defaults are recursive; required is top-level only.**
`applyDefaults` descends into present nested objects so a nested `default` is honored. Required-enforcement is applied only to the top-level `required` list (`s.required`, already parsed by `buildParamsFromSchema` and advertised in the tool's `Info().Required`). Nested-required enforcement is deliberately out of scope: it is a stricter behavior change with a larger blast radius, and flow output schemas gate routing on top-level fields. Recursive DEFAULTS are purely additive and safe; recursive REQUIRED is not, so it is left for a future change if a need arises.

**5. Deep-copy materialized defaults.**
`cloneJSONValue` copies the default before assigning it into the result, so the per-call output never aliases the shared, reused schema map. Without this, a downstream mutation of a filled default (e.g. a slice append) would corrupt the schema for the next call. `struct_output` is non-parallel, but the isolation is cheap and removes a latent footgun (covered by `TestApplyDefaults_DoesNotAliasSchema`).

**6. Non-object / property-less schemas pass through unchanged.**
`objectProperties` keys off the presence of a `properties` map (so it also handles union types like `["object","null"]`). When absent — the non-object fallback that `buildParamsFromSchema` wraps as a single `output` parameter — the new pass is skipped entirely, preserving prior behavior for that rarely-used path.

## Risks / Trade-offs

- **A stubborn model could retry-loop** on a required-no-default field it keeps omitting, up to `maxTurns`. Acceptable: it is the same bound as any tool-retry loop, strictly better than a silent strand, and the common (defaulted) case never errors. On the single-attempt forced wrap-up turn a rejection simply degrades to the existing text fallback — no loop.
- **Stricter than before for required-no-default omissions.** A payload that would previously have been stored incomplete is now rejected. This is the intended correction; downstream code that depended on receiving an incomplete struct output would already have been mis-routing.

## Migration

None. Internal-only; rollback = revert the commit. No feature flag — the change is safe-by-construction (additive defaults + retry-on-incomplete).
