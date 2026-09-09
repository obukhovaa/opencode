#!/usr/bin/env bash
# E2E blackbox test for the persistent shell's terminal isolation.
#
# The invariant under test is what keeps the TUI from being corrupted: the
# shell that backs both the bash tool and the TUI's `!` mode runs in its own
# session, so /dev/tty cannot resolve to the terminal opencode renders on.
#
# This has to be an e2e rather than a unit test because `go test` normally runs
# with no controlling terminal — in that environment "the shell cannot reach a
# terminal" is true whether or not the isolation works, and the check passes
# vacuously. Here the driver is launched under a real pty via `script`, so the
# terminal genuinely exists and the assertion has teeth.
#
# Usage:  ./scripts/test/shell_isolation.sh
set -euo pipefail

# ── colours / helpers ────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'
PASS=0; FAIL=0; SKIP=0

log_pass() { PASS=$((PASS + 1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL + 1)); printf "${RED}FAIL${NC}  %s  (%s)\n" "$1" "$2"; }
log_skip() { SKIP=$((SKIP + 1)); printf "${YELLOW}SKIP${NC}  %s  (%s)\n" "$1" "$2"; }

for cmd in jq script; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "Required tool not found: $cmd" >&2
        exit 1
    fi
done

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
DRIVER="$(mktemp)"

cleanup() {
    rm -rf "$WORKDIR"
    rm -f "$DRIVER" "$DRIVER.out"
}
trap cleanup EXIT

echo "Building cmd/shell-isolation-e2e …"
(cd "$ROOT" && go build -o "$DRIVER" ./cmd/shell-isolation-e2e) || { echo "Build failed"; exit 1; }

# Minimal sandbox config. Bash with -c only (no -l) so the test does not source
# the running user's profile.
cat > "$WORKDIR/.opencode.json" <<'JSON'
{
  "shell": {
    "path": "/bin/bash",
    "args": ["-s"]
  }
}
JSON

# ── run the driver under a pty ───────────────────────────────────────
# `script` allocates a real controlling terminal. Its argument order differs
# between BSD (macOS) and util-linux.
run_under_pty() {
    if script -q /dev/null true >/dev/null 2>&1; then
        (cd "$WORKDIR" && script -q /dev/null "$DRIVER") # BSD / macOS
    else
        (cd "$WORKDIR" && script -q -e -c "$DRIVER" /dev/null) # util-linux
    fi
}

RAW="$(run_under_pty || true)"
# `script` echoes terminal control bytes around the payload; keep the JSON only.
OUT="$(printf '%s' "$RAW" | tr -d '\r' | sed -n '/^{/,/^}/p')"

if ! printf '%s' "$OUT" | jq empty 2>/dev/null; then
    echo "Driver produced no parseable JSON. Raw output:" >&2
    printf '%s\n' "$RAW" >&2
    exit 1
fi

field() { printf '%s' "$OUT" | jq -r "$1"; }

# ── assertions ───────────────────────────────────────────────────────

name="driver has a controlling terminal"
if [ "$(field '.has_controlling_terminal')" = "true" ]; then
    log_pass "$name"
    HAS_TTY=true
else
    log_skip "$name" "no pty available; the isolation assertions would be vacuous"
    HAS_TTY=false
fi

name="shell command cannot write to /dev/tty"
if [ "$HAS_TTY" != "true" ]; then
    log_skip "$name" "no controlling terminal to leak to"
elif [ "$(field '.tty_write_exit_code')" != "0" ]; then
    log_pass "$name"
else
    log_fail "$name" "the write succeeded — the shell is still attached to our terminal"
fi

name="leaked text never reached the terminal"
if [ "$HAS_TTY" != "true" ]; then
    log_skip "$name" "no controlling terminal to leak to"
elif ! printf '%s' "$RAW" | grep -q "LEAKED-TO-TERMINAL"; then
    log_pass "$name"
else
    log_fail "$name" "the marker appeared in the terminal transcript"
fi

name="shell streams are not a tty"
if [ "$(field '.isatty_exit_code')" != "0" ]; then
    log_pass "$name"
else
    log_fail "$name" "stdin/stdout are a terminal inside the shell"
fi

name="git terminal prompting is disabled"
if [ "$(field '.git_terminal_prompt')" = "0" ]; then
    log_pass "$name"
else
    log_fail "$name" "GIT_TERMINAL_PROMPT=$(field '.git_terminal_prompt')"
fi

name="ordinary command is unaffected"
if [ "$(field '.echo_stdout')" = "hello-from-shell" ] && [ "$(field '.echo_exit_code')" = "0" ]; then
    log_pass "$name"
else
    log_fail "$name" "stdout=$(field '.echo_stdout') exit=$(field '.echo_exit_code')"
fi

name="cd persists across commands"
if [ "$(field '.cwd_after_cd')" = "/" ]; then
    log_pass "$name"
else
    log_fail "$name" "cwd=$(field '.cwd_after_cd')"
fi

name="interactive classification"
if [ "$(field '.classified["sudo -v"]')" = "true" ] &&
   [ "$(field '.classified["ls -la"]')" = "false" ] &&
   [ "$(field '.classified["docker ps"]')" = "false" ] &&
   [ "$(field '.classified["docker exec -it web sh"]')" = "true" ] &&
   [ "$(field '.classified["my-tool (configured)"]')" = "true" ]; then
    log_pass "$name"
else
    log_fail "$name" "$(field '.classified')"
fi

# ── summary ──────────────────────────────────────────────────────────
echo ""
printf "=== Results: ${GREEN}%d passed${NC}, ${RED}%d failed${NC}, ${YELLOW}%d skipped${NC} ===\n" "$PASS" "$FAIL" "$SKIP"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
