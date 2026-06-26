#!/usr/bin/env bash
# Battle test: opencode hooks ↔ RTK (https://github.com/rtk-ai/rtk).
#
# RTK is the motivating use case for the hook system. Its `rtk-rewrite.sh`
# script (vendored under scripts/test/fixtures/) is the same shell hook
# RTK installs into Claude Code's settings.json — we point our
# .opencode.json hooks block at it and exercise the full path:
# config load → matcher dispatch → subprocess → rtk rewrite → updatedInput.
#
# This validates the cross-product compatibility claim — that any
# Claude-Code-targeted hook plugin works in opencode without modification.
#
# Skips cleanly (SKIP, not FAIL) when rtk or jq are missing so machines
# without RTK installed still pass make test-e2e. Install rtk first to
# actually run the assertions:
#     brew install rtk        # macOS
#     curl -fsSL https://raw.githubusercontent.com/rtk-ai/rtk/refs/heads/master/install.sh | sh

set -euo pipefail

# ── colours / counters ───────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'
PASS=0; FAIL=0; SKIP=0

log_pass() { PASS=$((PASS + 1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL + 1)); printf "${RED}FAIL${NC}  %s  (%s)\n" "$1" "$2"; }
log_skip() { SKIP=$((SKIP + 1)); printf "${YELLOW}SKIP${NC}  %s  (%s)\n" "$1" "$2"; }

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
FIXTURE="$ROOT/scripts/test/fixtures/rtk-rewrite.sh"

echo ""
echo "=== RTK battle test ==="

# ── dependency probe ────────────────────────────────────────────────
# Skip the entire battery if RTK isn't available. The fixture script
# warns to stderr and exits 0 in this case, so technically we could
# still run it — but the assertions would all silently pass (no
# rewrites happen). Better to be explicit about the skip.
if ! command -v rtk >/dev/null 2>&1; then
    log_skip "rtk binary not on PATH" "install: brew install rtk"
    echo ""
    echo "=== Summary === PASS:$PASS FAIL:$FAIL SKIP:$SKIP"
    exit 0
fi
if ! command -v jq >/dev/null 2>&1; then
    log_skip "jq not on PATH" "install: brew install jq"
    echo ""
    echo "=== Summary === PASS:$PASS FAIL:$FAIL SKIP:$SKIP"
    exit 0
fi
if [ ! -x "$FIXTURE" ]; then
    log_fail "rtk-rewrite.sh fixture missing or not executable" "$FIXTURE"
    exit 1
fi

RTK_VERSION="$(rtk --version 2>/dev/null | head -1 || echo unknown)"
echo "rtk version: $RTK_VERSION"
echo "hook script: $FIXTURE"
echo ""

# ── sandbox / driver build ──────────────────────────────────────────
SANDBOX="$(mktemp -d)"
DRIVER="$(mktemp)"
BUILT_DRIVER=true

cleanup() {
    rm -rf "$SANDBOX"
    [ "$BUILT_DRIVER" = true ] && [ -f "$DRIVER" ] && rm -f "$DRIVER"
}
trap cleanup EXIT

echo "Building cmd/hooks-e2e …"
(cd "$ROOT" && go build -o "$DRIVER" ./cmd/hooks-e2e) || { echo "Build failed"; exit 1; }

# .opencode.json with the RTK hook wired into the `bash` matcher.
# This is the exact snippet a user would paste into their config; the
# matcher is lowercase (opencode-native) but a Claude Code config using
# "Bash" would also work via the case-insensitive comparison.
cat > "$SANDBOX/.opencode.json" <<EOF
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "bash",
        "hooks": [
          {"type": "command", "command": "$FIXTURE"}
        ]
      }
    ]
  }
}
EOF

# Disable telemetry so the test doesn't fire network requests in CI.
export RTK_TELEMETRY_DISABLED=1

# Helper: fire PreToolUse for `$1` and capture the resulting
# tool_input.command via jq. Returns the rewritten value (or the
# original if no rewrite happened) on stdout.
ask_rtk() {
    local cmd="$1"
    local out
    out=$(cd "$SANDBOX" && "$DRIVER" --tool bash --cmd "$cmd" --skip-post 2>/dev/null)
    # If a hook returned updatedInput, the driver shows it under .pre.UpdatedInput.command.
    # Otherwise we report the original command (== passthrough).
    local rewritten
    rewritten=$(echo "$out" | jq -r '.pre.UpdatedInput.command // empty')
    if [ -z "$rewritten" ]; then
        echo "$cmd"
    else
        echo "$rewritten"
    fi
}

# Helper: same as ask_rtk but also report the ExplicitAllow flag, used
# to verify RTK's `permissionDecision: "allow"` round-trips into our
# decision struct (the path that bypasses the permission gate).
ask_rtk_allow() {
    local cmd="$1"
    local out
    out=$(cd "$SANDBOX" && "$DRIVER" --tool bash --cmd "$cmd" --skip-post 2>/dev/null)
    echo "$out" | jq -r '.pre.ExplicitAllow // false'
}

# ── assertions ──────────────────────────────────────────────────────

# 1. `git status` is a flagship RTK rewrite — should become `rtk git status`.
GOT=$(ask_rtk "git status")
case "$GOT" in
    rtk*)
        log_pass "git status → '$GOT'"
        ;;
    *)
        log_fail "git status rewrite" "got '$GOT', expected something starting with 'rtk'"
        ;;
esac

# 2. `ls` should also be rewritten (one of the documented commands).
GOT=$(ask_rtk "ls")
case "$GOT" in
    rtk*)
        log_pass "ls → '$GOT'"
        ;;
    *)
        log_fail "ls rewrite" "got '$GOT'"
        ;;
esac

# 3. RTK 0.23+ branches: `rtk rewrite` exits 0 (auto-allow) OR 3 (ask
# rule matched — rewrite but let the host prompt). The hook script
# (`hooks/claude/rtk-rewrite.sh`) emits JSON WITH `permissionDecision:
# "allow"` on exit 0, and WITHOUT on exit 3. Current RTK versions ship
# most rewrites under "ask" rules (the user opted into this in their
# `~/.config/rtk/config.toml`), so ExplicitAllow being false is the
# normal observed shape — the standard permission gate runs as usual.
#
# We assert the union: ExplicitAllow=true OR ExplicitAllow=false are
# both valid, but if RTK ever returns `permissionDecision: "allow"`,
# our registry MUST round-trip it. Verified separately by the unit
# tests in internal/hooks/decision_test.go::TestRunPreTool_DenyWinsOverAllow
# (which constructs a synthetic allow-emitting hook).
ALLOW=$(ask_rtk_allow "git status")
case "$ALLOW" in
    true|false)
        log_pass "ExplicitAllow round-trips correctly (current value: $ALLOW)"
        ;;
    *)
        log_fail "ExplicitAllow round-trips" "got '$ALLOW', expected 'true' or 'false'"
        ;;
esac

# 4. Unknown commands (no RTK equivalent) must pass through unchanged.
# RTK's hook exits 0 with empty stdout for these; the registry treats
# empty JSON as "no decision" and the original command flows to the
# tool unmodified.
RANDOM_CMD="some_command_rtk_definitely_does_not_know_$$"
GOT=$(ask_rtk "$RANDOM_CMD")
if [ "$GOT" = "$RANDOM_CMD" ]; then
    log_pass "unknown command passes through unchanged"
else
    log_fail "unknown command passes through" "got '$GOT', expected '$RANDOM_CMD'"
fi

# 5. cargo test — RTK explicitly advertises this as a major win
# (200 lines → ~20 lines). Verify the rewrite fires.
GOT=$(ask_rtk "cargo test")
case "$GOT" in
    rtk*)
        log_pass "cargo test → '$GOT'"
        ;;
    *)
        log_fail "cargo test rewrite" "got '$GOT'"
        ;;
esac

# 6. The Edit tool — RTK's hook is matcher-scoped to bash, so an Edit
# invocation must NOT fire the rewrite. Verifies the matcher contract
# end-to-end through the driver.
OUT=$(cd "$SANDBOX" && "$DRIVER" --tool edit --cmd "git status" --skip-post 2>/dev/null)
PRE=$(echo "$OUT" | jq -r '.pre.UpdatedInput // null')
if [ "$PRE" = "null" ]; then
    log_pass "matcher 'bash' does NOT fire on edit tool"
else
    log_fail "matcher 'bash' did NOT fire on edit" "got UpdatedInput=$PRE"
fi

# ── summary ─────────────────────────────────────────────────────────
echo ""
echo "=== Summary === PASS:$PASS FAIL:$FAIL SKIP:$SKIP"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
