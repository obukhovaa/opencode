#!/usr/bin/env bash
# deferred_tools_native.sh — live e2e for the deferred-tools NATIVE path
# (Anthropic server-side tool search) through the real opencode binary and
# the ambient provider config in ~/.opencode.json — the same setup the grep
# e2e uses. No dedicated API key: it rides whatever Claude provider the
# user already has configured (e.g. the LiteLLM proxy).
#
# It defers the builtin `ls` tool for the coder agent, then asks the model
# to list files. On the native path `ls` ships with `defer_loading: true`
# (its schema hidden from the model) alongside the server
# `tool_search_tool_regex` tool, so the model MUST discover `ls` via
# server-side tool search before it can call it. We assert both the
# wire contract (deterministic) and the end result (proves discovery→
# invocation actually worked):
#   - the request carries `defer_loading` and the server tool-search tool
#   - the model's answer reports the correct file listing
#
# SKIPs (does not fail) when ~/.opencode.json is absent so CI without the
# proxy config stays green. Override the model with OPENCODE_TEST_MODEL
# (must be a Claude model on anthropic/vertexai/bedrock — SupportsToolSearch).
set -u

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
PASS=0; FAIL=0; SKIP=0
log_pass() { PASS=$((PASS+1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL+1)); printf "${RED}FAIL${NC}  %s (%s)\n" "$1" "${2:-}"; }
log_skip() { SKIP=$((SKIP+1)); printf "${YELLOW}SKIP${NC}  %s (%s)\n" "$1" "$2"; }

MODEL="${OPENCODE_TEST_MODEL:-vertexai.claude-sonnet-4-6}"

echo "── deferred_tools_native.sh (native path, live) ──"
if [ ! -f "$HOME/.opencode.json" ]; then
  log_skip "native deferred-tools e2e" "no ~/.opencode.json provider config"
  printf "Results: %d passed, %d failed, %d skipped\n" "$PASS" "$FAIL" "$SKIP"
  exit 0
fi

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
DATA="$WORKDIR/data"
BIN="$WORKDIR/opencode"
trap 'rm -rf "$WORKDIR"' EXIT

echo "Building opencode …"
if ! (cd "$ROOT" && go build -o "$BIN" .); then
  log_fail "build opencode"
  printf "Results: %d passed, %d failed, %d skipped\n" "$PASS" "$FAIL" "$SKIP"
  exit 1
fi

FX="$WORKDIR/fx"
mkdir -p "$FX"
cat > "$FX/.opencode.json" <<EOF
{
  "debug": true,
  "data": { "directory": "$DATA" },
  "agents": {
    "coder": {
      "model": "$MODEL",
      "maxTokens": 8000,
      "maxTurns": 6,
      "deferredTools": { "ls": true }
    }
  }
}
EOF
printf 'a\n' > "$FX/alpha.txt"; printf 'b\n' > "$FX/beta.txt"; printf 'c\n' > "$FX/gamma.txt"

echo "Model: $MODEL — deferring the ls tool, asking the model to list files …"
ANSWER=$( (cd "$FX" && OPENCODE_DEV_DEBUG=true "$BIN" -q --timeout 3m --auto-approve \
  -p "Use the ls tool to list files in the current directory, then say how many regular files there are." 2>/dev/null) | tail -3 )

REQ="$WORKDIR/req.json"
cat "$DATA"/messages/*/*_request.json 2>/dev/null > "$REQ" || true

if grep -q 'defer_loading' "$REQ" 2>/dev/null; then
  log_pass "request carries defer_loading (deferred tool schema withheld)"
else
  log_fail "request carries defer_loading" "native path did not engage — is $MODEL a SupportsToolSearch model?"
fi

if grep -q 'tool_search_tool_regex' "$REQ" 2>/dev/null; then
  log_pass "request carries the server tool-search tool"
else
  log_fail "request carries the server tool-search tool" "server tool not appended"
fi

# End-to-end: the model can only answer correctly if it discovered ls via
# tool search and invoked it (its schema was withheld by defer_loading).
if echo "$ANSWER" | grep -qiE '\b3\b|three'; then
  log_pass "model discovered+invoked the deferred tool (answer: 3 files)"
else
  log_fail "deferred tool used end-to-end" "unexpected answer: $(echo "$ANSWER" | tr '\n' ' ')"
fi

printf "Results: %d passed, %d failed, %d skipped\n" "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
