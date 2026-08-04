#!/usr/bin/env bash
# E2E gate for the struct-output-terminal-guarantee change (GENAI-134).
#
# Validates BOTH engine fixes end-to-end through the REAL runtime subsystems,
# driven by an in-package scripted provider / stub agent (no live LLM) and
# self-contained (each test loads config into a temp HOME):
#
#   Decision 1 — the agent loop (real processGeneration) FORCES tool_choice=
#                struct_output on the max-turns wrap-up for a schema-bearing run
#                and CAPTURES it, instead of asking for prose and discarding the
#                struct_output call. Also covers the plain-run and graceful
#                (forced turn returns text -> no panic) paths.
#   Decision 2 — the flow runner (real flow.Service) issues ONE bounded
#                last-ditch forcing turn on an empty / errored run before failing,
#                publishing a fresh Response event on rescue.
#
# Unlike the sibling scripts, this one wraps `go test` on the two real-subsystem
# suites rather than a bespoke cmd/ driver: the agent loop has no exported seam
# to inject a fake provider.Provider from an external package, so exercising
# Decision 1 end-to-end requires the in-package scripted provider. Both suites
# drive the real runtime code paths (processGeneration and flow.Service.runStep).
#
# Usage: ./scripts/test/struct_output.sh
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
echo "=== struct-output terminal-guarantee E2E (GENAI-134) ==="
echo ""

run_group "Decision 1: agent-loop max-turns forces+captures struct_output" \
    ./internal/llm/agent/ 'TestProcessGeneration_MaxTurns'
run_group "Decision 2: flow last-ditch forcing on empty/errored runs" \
    ./internal/flow/ 'TestForceStructOutput_(EmptyResponseRescued|ErroredRunRescued|TransientErrorSkips|ParentCtxCancelled)'

echo ""
printf "Results: %d passed, %d failed\n" "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
