#!/usr/bin/env bash
# E2E test for the Claude-Code-compatible hooks system.
#
# Hooks fire during tool dispatch; that path normally requires an LLM
# call, which we can't do in CI. Instead we exercise the FULL config-
# load pipeline (.opencode.json → viper → Config.Hooks → hooks.Registry)
# against a temporary sandbox and assert that PreToolUse and PostToolUse
# subprocesses actually run, see the expected env / stdin payload, and
# their JSON decisions are applied by the registry.
#
# This catches the class of bugs that bypass unit tests — most notably
# viper's key case-folding, which silently mangles "PreToolUse" to
# "pretooluse" during JSON ingestion and would otherwise make every
# real-world hook never fire.
#
# Usage: ./scripts/test/hooks.sh [path-to-driver-binary]
#   Without an argument, builds cmd/hooks-e2e fresh into a tempfile.

set -euo pipefail

# ── colours / helpers ────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'
PASS=0; FAIL=0

log_pass() { PASS=$((PASS + 1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL + 1)); printf "${RED}FAIL${NC}  %s  (%s)\n" "$1" "$2"; }

for cmd in jq python3; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "Required tool not found: $cmd" >&2
        exit 1
    fi
done

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SANDBOX="$(mktemp -d)"
BUILT_DRIVER=false
DRIVER="${1:-}"

cleanup() {
    rm -rf "$SANDBOX"
    if [ "$BUILT_DRIVER" = true ] && [ -n "$DRIVER" ] && [ -f "$DRIVER" ]; then
        rm -f "$DRIVER"
    fi
}
trap cleanup EXIT

if [ -z "$DRIVER" ]; then
    DRIVER="$(mktemp)"
    echo "Building cmd/hooks-e2e …"
    (cd "$ROOT" && go build -o "$DRIVER" ./cmd/hooks-e2e) || { echo "Build failed"; exit 1; }
    BUILT_DRIVER=true
fi

# ── sandbox: hook scripts ────────────────────────────────────────────
# PreToolUse hook: rewrites `git status` to `rtk git status` (RTK-style).
# Verifies it received the expected stdin payload, expected env vars,
# and logs its invocation to a file in the sandbox so the script can
# confirm the hook actually ran.
cat > "$SANDBOX/pre.sh" <<'EOF'
#!/bin/sh
set -e
input=$(cat)
echo "pre invoked with: $input" >> "$SANDBOX_LOG"
# Verify OPENCODE_PROJECT_DIR + CLAUDE_PROJECT_DIR are set.
if [ -z "${OPENCODE_PROJECT_DIR:-}" ]; then
    echo "OPENCODE_PROJECT_DIR not set" >&2
    exit 1
fi
if [ -z "${CLAUDE_PROJECT_DIR:-}" ]; then
    echo "CLAUDE_PROJECT_DIR not set" >&2
    exit 1
fi
# Verify stdin carried the expected JSON shape.
case "$input" in
    *'"hook_event_name":"PreToolUse"'*) ;;
    *) echo "hook_event_name missing or wrong: $input" >&2; exit 1 ;;
esac
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"command":"rtk git status"},"additionalContext":"prefix:rtk"}}'
EOF
chmod +x "$SANDBOX/pre.sh"

# PostToolUse hook: replaces verbose tool output with a 1-line summary.
# Also asserts the stdin payload shape.
cat > "$SANDBOX/post.sh" <<'EOF'
#!/bin/sh
set -e
input=$(cat)
echo "post invoked with: $input" >> "$SANDBOX_LOG"
case "$input" in
    *'"hook_event_name":"PostToolUse"'*) ;;
    *) echo "hook_event_name missing or wrong: $input" >&2; exit 1 ;;
esac
case "$input" in
    *'"tool_output":"200 lines of noisy output"'*) ;;
    *) echo "tool_output not propagated; got: $input" >&2; exit 1 ;;
esac
echo '{"hookSpecificOutput":{"hookEventName":"PostToolUse","updatedToolOutput":"compacted: 3 lines","additionalContext":"summary:done"}}'
EOF
chmod +x "$SANDBOX/post.sh"

# sandbox config — note the PascalCase event keys, exactly as users
# would paste from a Claude Code config. Viper will lowercase them
# during ingestion; the registry's case-insensitive lookup must still
# resolve to these matcher groups.
cat > "$SANDBOX/.opencode.json" <<EOF
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "bash",
        "hooks": [
          {"type": "command", "command": "$SANDBOX/pre.sh"}
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "bash|read",
        "hooks": [
          {"type": "command", "command": "$SANDBOX/post.sh"}
        ]
      }
    ]
  }
}
EOF

# Hook scripts write to this log so the test can assert they actually ran.
SANDBOX_LOG="$SANDBOX/hook.log"
: > "$SANDBOX_LOG"
export SANDBOX_LOG

echo ""
echo "=== Hooks E2E tests ==="
echo "Driver:    $DRIVER"
echo "Sandbox:   $SANDBOX"
echo ""

# Every driver invocation overrides HOME + XDG_CONFIG_HOME to point at
# the sandbox. The viper loader inside the driver searches `$HOME`,
# `$XDG_CONFIG_HOME/opencode`, and `$HOME/.config/opencode` for global
# `.opencode.json` — without this override, the developer's real
# `~/.opencode.json` deep-merges into the sandbox's project config and
# spurious global hooks fire when the test expects "no hooks block".
# We keep the developer's real config untouched on disk; we just lie
# about where their home is for the duration of each driver call.

# ── run 1: PreToolUse + PostToolUse fire and mutate ─────────────────
OUTPUT=$(cd "$SANDBOX" && env HOME="$SANDBOX" XDG_CONFIG_HOME="$SANDBOX/xdg" "$DRIVER" --tool bash --cmd "git status" --output "200 lines of noisy output")
PRE_UPDATED=$(echo "$OUTPUT" | jq -r '.pre.UpdatedInput.command // ""')
PRE_CTX=$(echo "$OUTPUT" | jq -r '.pre.AdditionalContext // ""')
POST_UPDATED=$(echo "$OUTPUT" | jq -r '.post.UpdatedOutput // ""')
POST_CTX=$(echo "$OUTPUT" | jq -r '.post.AdditionalContext // ""')
HOOKS_KEYS=$(echo "$OUTPUT" | jq -r '.hooks_keys | join(",")')

if [ "$PRE_UPDATED" = "rtk git status" ]; then
    log_pass "PreToolUse rewrote tool_input.command"
else
    log_fail "PreToolUse rewrote tool_input.command" "got '$PRE_UPDATED', want 'rtk git status' (full output: $OUTPUT)"
fi

if [ "$PRE_CTX" = "prefix:rtk" ]; then
    log_pass "PreToolUse additionalContext propagated"
else
    log_fail "PreToolUse additionalContext propagated" "got '$PRE_CTX', want 'prefix:rtk'"
fi

if [ "$POST_UPDATED" = "compacted: 3 lines" ]; then
    log_pass "PostToolUse rewrote tool_output"
else
    log_fail "PostToolUse rewrote tool_output" "got '$POST_UPDATED', want 'compacted: 3 lines'"
fi

if [ "$POST_CTX" = "summary:done" ]; then
    log_pass "PostToolUse additionalContext propagated"
else
    log_fail "PostToolUse additionalContext propagated" "got '$POST_CTX', want 'summary:done'"
fi

# Assert the hook subprocesses actually executed (sentinel log file).
if grep -q "pre invoked with" "$SANDBOX_LOG"; then
    log_pass "PreToolUse subprocess executed"
else
    log_fail "PreToolUse subprocess executed" "log empty: $(cat $SANDBOX_LOG)"
fi
if grep -q "post invoked with" "$SANDBOX_LOG"; then
    log_pass "PostToolUse subprocess executed"
else
    log_fail "PostToolUse subprocess executed" "log empty: $(cat $SANDBOX_LOG)"
fi

# Document the viper key-folding behavior for diagnostic value.
# Either "PreToolUse" or "pretooluse" is acceptable — the registry's
# case-insensitive lookup handles both. We just record what landed.
echo "  hooks_keys after viper: $HOOKS_KEYS"

# ── run 2: matcher case-insensitivity (PascalCase matcher) ──────────
# Drop a second config that uses PascalCase matcher names (as a Claude
# Code config would). The lowercased `bash` tool name must still match
# the PascalCase matcher in the config — that's the spec D5 contract.
cat > "$SANDBOX/.opencode.json" <<EOF
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {"type": "command", "command": "$SANDBOX/pre.sh"}
        ]
      }
    ]
  }
}
EOF
: > "$SANDBOX_LOG"
OUTPUT=$(cd "$SANDBOX" && env HOME="$SANDBOX" XDG_CONFIG_HOME="$SANDBOX/xdg" "$DRIVER" --tool bash --cmd "git status" --skip-post)
PRE_UPDATED=$(echo "$OUTPUT" | jq -r '.pre.UpdatedInput.command // ""')
if [ "$PRE_UPDATED" = "rtk git status" ]; then
    log_pass "PascalCase matcher 'Bash' matches lowercase tool 'bash'"
else
    log_fail "PascalCase matcher 'Bash' matches lowercase tool 'bash'" "got '$PRE_UPDATED' (full: $OUTPUT)"
fi

# ── run 3: no hooks block → no-op ───────────────────────────────────
cat > "$SANDBOX/.opencode.json" <<EOF
{}
EOF
OUTPUT=$(cd "$SANDBOX" && env HOME="$SANDBOX" XDG_CONFIG_HOME="$SANDBOX/xdg" "$DRIVER" --tool bash --cmd "git status" --skip-post)
PRE_UPDATED=$(echo "$OUTPUT" | jq -r '.pre.UpdatedInput // null')
PRE_BLOCK=$(echo "$OUTPUT" | jq -r '.pre.Block // false')
if [ "$PRE_UPDATED" = "null" ] && [ "$PRE_BLOCK" = "false" ]; then
    log_pass "absent hooks block leaves agent unchanged"
else
    log_fail "absent hooks block leaves agent unchanged" "got UpdatedInput=$PRE_UPDATED, Block=$PRE_BLOCK"
fi

# ── run 4: deny via exit 2 ──────────────────────────────────────────
cat > "$SANDBOX/deny.sh" <<'EOF'
#!/bin/sh
cat > /dev/null
echo "rm -rf is forbidden" >&2
exit 2
EOF
chmod +x "$SANDBOX/deny.sh"
cat > "$SANDBOX/.opencode.json" <<EOF
{
  "hooks": {
    "PreToolUse": [
      {"matcher": "bash", "hooks": [{"type": "command", "command": "$SANDBOX/deny.sh"}]}
    ]
  }
}
EOF
OUTPUT=$(cd "$SANDBOX" && env HOME="$SANDBOX" XDG_CONFIG_HOME="$SANDBOX/xdg" "$DRIVER" --tool bash --cmd "rm -rf /" --skip-post)
PRE_BLOCK=$(echo "$OUTPUT" | jq -r '.pre.Block // false')
PRE_REASON=$(echo "$OUTPUT" | jq -r '.pre.BlockReason // ""')
if [ "$PRE_BLOCK" = "true" ] && [ "$PRE_REASON" = "rm -rf is forbidden" ]; then
    log_pass "exit 2 blocks with stderr text as BlockReason"
else
    log_fail "exit 2 blocks with stderr text as BlockReason" "Block=$PRE_BLOCK, Reason='$PRE_REASON'"
fi

# ── summary ─────────────────────────────────────────────────────────
echo ""
echo "=== Summary ==="
echo "PASS:  $PASS"
echo "FAIL:  $FAIL"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
