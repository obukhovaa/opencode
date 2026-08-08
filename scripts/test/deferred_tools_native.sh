#!/usr/bin/env bash
# deferred_tools_native.sh — live e2e for the deferred-tools NATIVE path.
# Runs cmd/deferred-e2e in native mode: a recording reverse proxy in front
# of api.anthropic.com captures the real request, asserting defer_loading +
# the server tool-search tool on the wire and that the model discovered and
# invoked a deferred tool. SKIPs (does not fail) without ANTHROPIC_API_KEY —
# CI stays hermetic; the live integration runs wherever credentials exist.
set -u

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
PASS=0; FAIL=0; SKIP=0
log_pass() { PASS=$((PASS+1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL+1)); printf "${RED}FAIL${NC}  %s (%s)\n" "$1" "${2:-}"; }
log_skip() { SKIP=$((SKIP+1)); printf "${YELLOW}SKIP${NC}  %s (%s)\n" "$1" "$2"; }

echo "── deferred_tools_native.sh (native path, live) ──"
if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  log_skip "native deferred-tools e2e" "ANTHROPIC_API_KEY not set"
  printf "Results: %d passed, %d failed, %d skipped\n" "$PASS" "$FAIL" "$SKIP"
  exit 0
fi

cd "$(dirname "$0")/../.."
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT
DRIVER="$WORKDIR/deferred-e2e"

if ! go build -o "$DRIVER" ./cmd/deferred-e2e; then
  log_fail "build deferred-e2e driver"
  printf "Results: %d passed, %d failed, %d skipped\n" "$PASS" "$FAIL" "$SKIP"
  exit 1
fi

OUT="$("$DRIVER" -mode native 2>"$WORKDIR/stderr")"
STATUS=$?
if [ "$STATUS" -eq 3 ]; then
  log_skip "native deferred-tools e2e" "driver reported missing credentials"
  printf "Results: %d passed, %d failed, %d skipped\n" "$PASS" "$FAIL" "$SKIP"
  exit 0
fi
if [ -z "$OUT" ]; then
  log_fail "driver produced no output" "$(tail -3 "$WORKDIR/stderr" | tr '\n' ' ')"
  printf "Results: %d passed, %d failed, %d skipped\n" "$PASS" "$FAIL" "$SKIP"
  exit 1
fi

for check in request_carries_defer_loading \
             request_carries_server_tool_search \
             model_ran_server_tool_search \
             deferred_tool_invoked_after_discovery; do
  if echo "$OUT" | jq -e --arg c "$check" '.checks | index($c)' >/dev/null; then
    log_pass "$check"
  else
    log_fail "$check" "$(echo "$OUT" | jq -r '.errors // [] | join("; ")')"
  fi
done

printf "Results: %d passed, %d failed, %d skipped\n" "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
