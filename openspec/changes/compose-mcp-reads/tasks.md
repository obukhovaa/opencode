## 1. Preserve MCP result correctness

- [ ] 1.1 Preserve MCP `IsError` through ordinary rendering; test valid-JSON error content and existing successful text formatting.
- [ ] 1.2 Add runtime-only complete/text-only eligibility metadata, including server/global output caps; test structured-only, mixed content and spilled outputs without changing ordinary rendering.

## 2. Establish one authorized dispatch boundary

- [ ] 2.1 Extract single-call execution from ordinary parallel/sequential dispatch, separating post-hook content, hook context and status from conversation publication; run existing hook, permission, watchdog and loop tests.
- [ ] 2.2 Add parity tests for mutated inputs, hook blocks/redaction/context, rule denial, human rejection, inherited outer hook approval, identity context and repeated-call tracking; include exact numeric argument IDs through hook/MCP decoders and assert the actual request value `9007199254740993`.
- [ ] 2.3 Verify parent cancellation, ownership loss, child timeout, chained/shared nested permission-wait accounting (including a wait longer than the active budget), pool eviction and existing retry semantics through the extracted boundary.

## 3. Implement explicit eligibility and bounded plans

- [ ] 3.1 Add `mcpComposition.readTools` configuration, exact-name validation and Viper round-trip tests; update schema generation and add default-off registration gated by an exact `mcp_compose` agent grant.
- [ ] 3.2 Bind composition to the filtered resolved snapshot; reject non-MCP/duplicate names, missing endpoint grants, explicit non-read-only hints and inactive deferred tools without changing discovery state.
- [ ] 3.3 Implement the strict static plan parser, duplicate-key/depth checks and preflight validation for IDs, bindings, return IDs and selection shapes; test every input bound before any child executes.
- [ ] 3.4 Implement RFC6901 projection and earlier-result bindings with exact numeric preservation, missing/null distinction, field/row limits and nonoverlapping existing targets.
- [ ] 3.5 Execute sequential children through the shared boundary, clearing inherited composition approval and enforcing active-time/call/byte limits; stop on failure and release unprojected data after selection.
- [ ] 3.6 Accept only complete post-hook JSON text; test that raw structured data, spill files, metadata and pre-hook values cannot bypass filters or supply fallback output.
- [ ] 3.7 Return bounded provenance/status/error envelopes and explicit row incompleteness, preserve hook context and control outcomes, and exclude raw child results/bound values from history, SSE and telemetry payloads; test concurrent-session isolation.

## 4. Demonstrate correctness and context reduction

- [ ] 4.1 Add deterministic ordinary-versus-composed fixtures for list-to-detail, status reads and nested JSON, with pinned starting transcripts/configuration and explicit required-answer-field expectations.
- [ ] 4.2 Verify exact child request/fact parity and all negative cases, and add default-off Claude request golden tests for unchanged tools/order/system/cache markers.
- [ ] 4.3 Generate a reproducible report of ordinary/composed result bytes, full serialized request bytes including schemas and plan inputs, large hook-context overhead, and scripted model decisions avoided; require each fixture's result ratio at most 0.20 and aggregate full-request reduction before pilot eligibility.

## 5. Document and verify the disabled pilot

- [ ] 5.1 Document configuration, exact grants, reviewed endpoint selection, unsupported outputs, resource bounds and rollback in the existing configuration/tools/MCP/permissions docs; keep all runtime/workspace opt-ins absent.
- [ ] 5.2 Regenerate `opencode-schema.json`, run schema checks, targeted regression suites and `make test`; add a local fake-MCP E2E fixture for config-to-dispatch behavior and run it without external services or live model spend.
- [ ] 5.3 Run strict OpenSpec validation and attach the measured fixture report and remaining limitations to implementation review; keep rollout disabled unless the documented gates pass and a separate single-agent pilot opt-in is authorized.
