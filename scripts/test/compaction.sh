#!/usr/bin/env bash
# E2E test for auto-compaction token counting.
#
# Reproduces the count_tokens bug where a proxy (LiteLLM in front of
# Bedrock) answers HTTP 200 from /v1/messages/count_tokens but silently
# omits the system prompt + tool schemas from the returned count. The
# agent loop trusts that number to decide when to auto-compact, so the
# truncation made compaction fire late or never — risking a hard
# context-overflow error mid-run.
#
# The cmd/compaction-e2e driver stands up a real Anthropic provider
# (provider.NewProvider) pointed at an in-process httptest server that
# mimics the truncating proxy, then calls provider.CountTokens and
# reports what the reconciliation logic decided. This exercises the full
# real client → HTTP endpoint → reconcile path, not a mock.
#
# Usage: ./scripts/test/compaction.sh

set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'
PASS=0; FAIL=0

log_pass() { PASS=$((PASS + 1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL + 1)); printf "${RED}FAIL${NC}  %s  (%s)\n" "$1" "$2"; }

for cmd in jq; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "Required tool not found: $cmd" >&2
        exit 1
    fi
done

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DRIVER="$(mktemp)"

cleanup() { [ -f "$DRIVER" ] && rm -f "$DRIVER"; }
trap cleanup EXIT

echo "Building cmd/compaction-e2e …"
(cd "$ROOT" && go build -o "$DRIVER" ./cmd/compaction-e2e) || { echo "Build failed"; exit 1; }

echo ""
echo "=== Auto-compaction token counting E2E ==="
echo ""

OUTPUT=$("$DRIVER")

ERR_COUNT=$(echo "$OUTPUT" | jq -r '.errors | length // 0')
if [ "$ERR_COUNT" != "0" ] && [ "$ERR_COUNT" != "null" ]; then
    echo "${YELLOW}Driver reported errors:${NC}"
    echo "$OUTPUT" | jq -r '.errors[]' | sed 's/^/  /'
fi

endpoint_hit=$(echo "$OUTPUT" | jq -r '.count_tokens_endpoint_hit')
truncated_value=$(echo "$OUTPUT" | jq -r '.truncated_endpoint_value')
reconciled=$(echo "$OUTPUT" | jq -r '.reconciled_tokens')
triggered=$(echo "$OUTPUT" | jq -r '.compaction_triggered')
sys_approx=$(echo "$OUTPUT" | jq -r '.system_prompt_tokens_approx')
healthy_value=$(echo "$OUTPUT" | jq -r '.healthy_endpoint_value')
healthy_reported=$(echo "$OUTPUT" | jq -r '.healthy_reported')
healthy_triggered=$(echo "$OUTPUT" | jq -r '.healthy_triggered')

# ── 1. the truncating count_tokens endpoint was actually hit ────────
if [ "$endpoint_hit" = "true" ]; then
    log_pass "count_tokens endpoint was invoked (real HTTP path)"
else
    log_fail "count_tokens endpoint was invoked" "endpoint not hit"
fi

# ── 2. the endpoint truncated (returned 8) ──────────────────────────
if [ "$truncated_value" = "8" ]; then
    log_pass "proxy returned truncated count (8) omitting system + tools"
else
    log_fail "proxy returned truncated count" "got: $truncated_value"
fi

# ── 3. reconciliation floored to the local estimate, NOT 8 ──────────
if [ "$reconciled" -gt 90000 ]; then
    log_pass "reconciled token count reflects true footprint (>90k, not 8)"
else
    log_fail "reconciled token count reflects true footprint" "got: $reconciled (system approx: $sys_approx)"
fi

# ── 4. auto-compaction fires despite the truncating endpoint ────────
if [ "$triggered" = "true" ]; then
    log_pass "auto-compaction triggered despite truncating count_tokens"
else
    log_fail "auto-compaction triggered despite truncating count_tokens" "triggered=false — REGRESSION: the original bug"
fi

# ── 5. healthy endpoint value is used verbatim ──────────────────────
if [ "$healthy_reported" = "$healthy_value" ]; then
    log_pass "healthy endpoint count used verbatim ($healthy_reported)"
else
    log_fail "healthy endpoint count used verbatim" "reported $healthy_reported, endpoint $healthy_value"
fi

# ── 6. healthy endpoint above threshold still triggers ──────────────
if [ "$healthy_triggered" = "true" ]; then
    log_pass "healthy endpoint above threshold triggers compaction"
else
    log_fail "healthy endpoint above threshold triggers compaction" "triggered=false"
fi

echo ""
printf "Results: %d passed, %d failed\n" "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
    echo ""
    echo "Full driver output:"
    echo "$OUTPUT" | jq .
    exit 1
fi
exit 0
