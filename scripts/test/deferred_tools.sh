#!/usr/bin/env bash
# deferred_tools.sh — hermetic e2e for the deferred-tools FALLBACK path.
# Drives cmd/deferred-e2e in fallback mode: an in-process mock
# OpenAI-compatible server records each request's tools array, asserting the
# wire contract (omission before activation, toolsearch presence,
# append-after-prefix activation, no-deferral bypass identity).
set -u

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
PASS=0; FAIL=0
log_pass() { PASS=$((PASS+1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL+1)); printf "${RED}FAIL${NC}  %s (%s)\n" "$1" "${2:-}"; }

cd "$(dirname "$0")/../.."
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT
DRIVER="$WORKDIR/deferred-e2e"

echo "── deferred_tools.sh (fallback path) ──"
if ! go build -o "$DRIVER" ./cmd/deferred-e2e; then
  log_fail "build deferred-e2e driver"
  printf "Results: %d passed, %d failed\n" "$PASS" "$FAIL"
  exit 1
fi

OUT="$("$DRIVER" -mode fallback 2>"$WORKDIR/stderr")" || true
if [ -z "$OUT" ]; then
  log_fail "driver produced no output" "$(tail -3 "$WORKDIR/stderr" | tr '\n' ' ')"
  printf "Results: %d passed, %d failed\n" "$PASS" "$FAIL"
  exit 1
fi

for check in request1_omits_deferred_and_has_toolsearch \
             toolsearch_returned_contract \
             request2_appends_activated_after_prefix \
             no_deferral_payload_identical_to_bypass; do
  if echo "$OUT" | jq -e --arg c "$check" '.checks | index($c)' >/dev/null; then
    log_pass "$check"
  else
    log_fail "$check" "$(echo "$OUT" | jq -r '.errors // [] | join("; ")')"
  fi
done

printf "Results: %d passed, %d failed\n" "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
