#!/usr/bin/env bash
# E2E gate for the struct-output-schema-message-delivery change (GENAI-325).
#
# The bug: `struct_output`'s input_schema was built from the flow step's
# output.schema, so two consecutive steps of one agent shipped byte-different
# tool blocks. Anthropic renders the cacheable prefix as tools -> system ->
# messages and invalidates everything after the first changed byte, so that one
# differing tool definition took down the whole prefix — including the message
# history a forked step had just copied verbatim. Symptom: the first LLM call of
# every flow step is a cache miss.
#
# The fix moves the schema out of the tool block and into a <struct_output_schema>
# envelope in the message tail. That splits into four properties, each verified
# against the REAL subsystem that owns it rather than a live API call — the cache
# matches on serialized bytes, so asserting the bytes is both stronger and
# cheaper than chasing a cache_read_input_tokens figure:
#
#   1. Tool surface   — the struct_output definition is invariant across schemas,
#                       validation/unwrapping still enforces the full schema (the
#                       non-regression gate), and the schemas message delivery
#                       cannot express (non-object roots, an `output` property of
#                       their own) fall back to the legacy surface intact.
#   2. Provider       — the serialized Anthropic tools block and its cache
#                       breakpoint position are identical across two steps'
#                       schemas; `tool` delivery still varies (the escape hatch
#                       is a real revert).
#   3. Agent loop     — the envelope is injected once per session per schema,
#                       re-injected for a forked step with a new schema and after
#                       compaction drops it (including a compaction part-way
#                       through a run), and never for a schema-less agent.
#   4. Flow           — the whole path from two-step flow YAML to tool surface,
#                       envelope, and the round-tripped step document.
#
# Usage: ./scripts/test/struct_output_schema_cache.sh
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
PASS=0; FAIL=0

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

run_group() {
    local name="$1" pkg="$2" pattern="$3"
    local out
    out="$(mktemp)"
    if go test "$pkg" -run "$pattern" -count=1 >"$out" 2>&1; then
        printf "${GREEN}PASS${NC}  %s\n" "$name"
        PASS=$((PASS + 1))
    else
        printf "${RED}FAIL${NC}  %s\n" "$name"
        sed 's/^/    /' "$out"
        FAIL=$((FAIL + 1))
    fi
    rm -f "$out"
}

echo ""
echo "=== struct_output schema delivery / prompt-cache stability E2E (GENAI-325) ==="
echo ""

run_group "1. tool surface invariant + validation preserved" \
    ./internal/llm/tools/ 'TestStructOutput|TestSchemaFingerprint|TestRenderSchemaEnvelope|TestParseSchemaDelivery'
run_group "2. anthropic tools block is byte-stable across step schemas" \
    ./internal/llm/provider/ 'TestConvertTools(IsByteStableAcrossStepSchemas|BreakpointPositionUnchangedAcrossSchemas|VariesAcrossSchemasInToolDelivery)'
run_group "3. agent injects the envelope once per session per schema" \
    ./internal/llm/agent/ 'TestInjectStructOutputSchema|TestWithStructOutputSchema|TestResolveSchemaDeliveryPrecedence'
run_group "4. two-step flow keeps one tool block and two schemas" \
    ./internal/flow/ 'TestFlowStep(SchemasDoNotPerturbTheToolBlock|SchemasProduceDistinctEnvelopes|OutputsRoundTripThroughStructOutput)'
run_group "5. config round-trip for the delivery escape hatch" \
    ./internal/config/ 'TestConfig_StructOutputSchemaDelivery'
run_group "6. published JSON schema matches the runtime validator" \
    ./cmd/schema/ 'TestSchemaEnumsMatchRuntimeValidators|TestSchemaIsUpToDate'

echo ""
printf "Results: %d passed, %d failed\n" "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
