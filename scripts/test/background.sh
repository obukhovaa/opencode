#!/usr/bin/env bash
# E2E test for the background-tasks subsystem.
#
# Exercises the full code path of the new background-task tools — bash
# `run_in_background`, monitor, tasklist, taskstop — plus the cleanup /
# orphan-sweep / sandbox-containment guarantees the spec mandates. Uses
# the cmd/background-e2e driver to avoid needing an LLM; the driver
# wires the message service, task registry, and tool helpers against a
# temp sandbox.
#
# The script overrides HOME + XDG_CONFIG_HOME for the same reason
# hooks.sh does: the developer's real `~/.opencode.json` would otherwise
# deep-merge into the driver's loaded config (notably
# `sessionProvider.type: "mysql"`) and the driver would try to run
# MySQL queries against an in-process SQLite. We isolate the run by
# pointing the driver's home at the sandbox.
#
# Usage: ./scripts/test/background.sh [opencode-binary]

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
SANDBOX="$(mktemp -d)"
DRIVER="$(mktemp)"

cleanup() {
    rm -rf "$SANDBOX"
    [ -f "$DRIVER" ] && rm -f "$DRIVER"
}
trap cleanup EXIT

echo "Building cmd/background-e2e …"
(cd "$ROOT" && go build -o "$DRIVER" ./cmd/background-e2e) || { echo "Build failed"; exit 1; }

echo ""
echo "=== Background tasks E2E ==="
echo "Driver:  $DRIVER"
echo "Sandbox: $SANDBOX"
echo ""

# Sandbox HOME + XDG_CONFIG_HOME so the developer's global .opencode.json
# does not influence the driver — see header comment.
OUTPUT=$(env HOME="$SANDBOX" XDG_CONFIG_HOME="$SANDBOX/xdg" "$DRIVER" --sandbox="$SANDBOX")

# Parse the JSON output. Surface anything in `errors` first so failures
# point at the right line.
ERR_COUNT=$(echo "$OUTPUT" | jq -r '.errors | length // 0')
if [ "$ERR_COUNT" -gt 0 ]; then
    echo "${YELLOW}Driver reported errors:${NC}"
    echo "$OUTPUT" | jq -r '.errors[]' | sed 's/^/  /'
fi
WRITE_ERR=$(echo "$OUTPUT" | jq -r '.write_err // ""')
if [ -n "$WRITE_ERR" ]; then
    echo "${YELLOW}WritePair returned error:${NC} $WRITE_ERR"
fi

bash_ack=$(echo "$OUTPUT" | jq -r '.bash_ack // ""')
bash_task_id=$(echo "$OUTPUT" | jq -r '.bash_task_id // ""')
bash_under_sandbox=$(echo "$OUTPUT" | jq -r '.bash_output_under_sandbox')
bash_in_db=$(echo "$OUTPUT" | jq -r '.bash_completion_in_db')
bash_synthetic=$(echo "$OUTPUT" | jq -r '.bash_completion_synthetic')
bash_content=$(echo "$OUTPUT" | jq -r '.bash_completion_content // ""')
bash_strips=$(echo "$OUTPUT" | jq -r '.bash_synthetic_strips_flag')
monitor_event=$(echo "$OUTPUT" | jq -r '.monitor_event_received')
monitor_terminal=$(echo "$OUTPUT" | jq -r '.monitor_terminal_status // ""')
tasklist_has_bash=$(echo "$OUTPUT" | jq -r '.tasklist_contains_bash')
tasklist_leak=$(echo "$OUTPUT" | jq -r '.tasklist_cross_session_leak')
taskstop_killed=$(echo "$OUTPUT" | jq -r '.taskstop_killed_received')
orphan_sweep=$(echo "$OUTPUT" | jq -r '.orphan_sweep_removed')
sandbox_leak=$(echo "$OUTPUT" | jq -r '.sandbox_leak')

# ── 1. bash run_in_background ack ────────────────────────────────────
if [[ "$bash_ack" == *"Background task started"* && "$bash_ack" == *"task_id: shell_"* ]]; then
    log_pass "bash run_in_background returns ack with task_id"
else
    log_fail "bash run_in_background returns ack with task_id" "ack: $bash_ack"
fi

# ── 2. task ID format ────────────────────────────────────────────────
if [[ "$bash_task_id" =~ ^shell_[A-Z2-7]+$ ]]; then
    log_pass "bash task_id matches shell_<base32> format"
else
    log_fail "bash task_id matches shell_<base32> format" "got: $bash_task_id"
fi

# ── 3. output file lives in sandbox ──────────────────────────────────
if [ "$bash_under_sandbox" = "true" ]; then
    log_pass "bash output file is under <sandbox>/.opencode/tasks/"
else
    log_fail "bash output file is under <sandbox>/.opencode/tasks/" "leaked outside"
fi

# ── 4. completion lands in DB with synthetic=true ───────────────────
if [ "$bash_in_db" = "true" ]; then
    log_pass "bash synthetic completion in messages table"
else
    log_fail "bash synthetic completion in messages table" "not found in DB"
fi
if [ "$bash_synthetic" = "true" ]; then
    log_pass "bash completion Assistant message has synthetic=true"
else
    log_fail "bash completion Assistant message has synthetic=true" "flag missing"
fi
if [[ "$bash_content" == *"hello-bg-output"* ]]; then
    log_pass "bash completion content carries subprocess output"
else
    log_fail "bash completion content carries subprocess output" "got: $bash_content"
fi
if [ "$bash_strips" = "true" ]; then
    log_pass "bash synthetic ToolCall.Input strips run_in_background flag"
else
    log_fail "bash synthetic ToolCall.Input strips run_in_background flag" "flag leaked"
fi

# ── 5. monitor coalesce + terminal completion ───────────────────────
if [ "$monitor_event" = "true" ]; then
    log_pass "monitor emits coalesced monitor-event with matched lines"
else
    log_fail "monitor emits coalesced monitor-event with matched lines" "no event arrived"
fi
case "$monitor_terminal" in
    completed|failed|killed-cap)
        log_pass "monitor fires terminal completion ($monitor_terminal)"
        ;;
    *)
        log_fail "monitor fires terminal completion" "got: '$monitor_terminal'"
        ;;
esac

# ── 6. tasklist scope ────────────────────────────────────────────────
if [ "$tasklist_has_bash" = "true" ]; then
    log_pass "tasklist returns the caller's bash task"
else
    log_fail "tasklist returns the caller's bash task" "task not listed"
fi
if [ "$tasklist_leak" = "false" ]; then
    log_pass "tasklist does NOT leak tasks from other sessions"
else
    log_fail "tasklist does NOT leak tasks from other sessions" "cross-session task visible"
fi

# ── 7. taskstop fires killed notification ───────────────────────────
if [ "$taskstop_killed" = "true" ]; then
    log_pass "taskstop kills task and fires killed notification"
else
    log_fail "taskstop kills task and fires killed notification" "killed flag missing"
fi

# ── 8. orphan sweep at boot ─────────────────────────────────────────
if [ "$orphan_sweep" = "true" ]; then
    log_pass "SweepOrphans removes stale .out files at boot"
else
    log_fail "SweepOrphans removes stale .out files at boot" "stale file persisted"
fi

# ── 9. sandbox containment ──────────────────────────────────────────
if [ "$sandbox_leak" = "false" ]; then
    log_pass "no .out files leaked outside <sandbox>/.opencode/tasks/"
else
    log_fail "no .out files leaked outside <sandbox>/.opencode/tasks/" "files outside detected"
fi

# ── 10. non-interactive wait primitive ──────────────────────────────
ni_wait_ok=$(echo "$OUTPUT" | jq -r '.non_interactive_wait_ok')
ni_wait_elapsed_ok=$(echo "$OUTPUT" | jq -r '.non_interactive_wait_elapsed_ok')
ni_ctx_timeout_ok=$(echo "$OUTPUT" | jq -r '.non_interactive_ctx_timeout_ok')

if [ "$ni_wait_ok" = "true" ]; then
    log_pass "WaitForActiveTasks returns nil when all pending tasks finish"
else
    log_fail "WaitForActiveTasks returns nil when all pending tasks finish" "wait did not return cleanly"
fi
if [ "$ni_wait_elapsed_ok" = "true" ]; then
    log_pass "WaitForActiveTasks unblocks within expected window (100-500ms)"
else
    log_fail "WaitForActiveTasks unblocks within expected window (100-500ms)" "elapsed time out of bounds"
fi
if [ "$ni_ctx_timeout_ok" = "true" ]; then
    log_pass "WaitForActiveTasks honors ctx deadline on hung task"
else
    log_fail "WaitForActiveTasks honors ctx deadline on hung task" "deadline did not trip"
fi

# ── 10. final cleanup verification ──────────────────────────────────
# The trap will rm -rf the sandbox — verify it works on a known artifact
# by listing the tasks directory before exit.
TASK_DIR="$SANDBOX/.opencode/tasks"
if [ -d "$TASK_DIR" ]; then
    REMAINING=$(ls -1 "$TASK_DIR" 2>/dev/null | wc -l | tr -d ' ')
    echo "  tasks dir at end of run: $REMAINING file(s)"
    # After the post-driver sweep, all live tasks' files should have
    # been swept. Some lingering files are OK (a task may have spawned
    # after the sweep), but we record the count.
fi

# ── result ──────────────────────────────────────────────────────────
echo ""
printf "Results: %d passed, %d failed\n" "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
    echo ""
    echo "Full driver output:"
    echo "$OUTPUT" | jq .
    exit 1
fi
exit 0
