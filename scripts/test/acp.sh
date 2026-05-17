#!/usr/bin/env bash
# E2E blackbox test for the ACP server (opencode acp).
# Builds opencode, starts the ACP process, sends JSON-RPC messages over
# stdin, and verifies responses on stdout.
#
# Usage:  ./scripts/test/acp.sh [path-to-binary]
#   If a binary path is given it is used as-is; otherwise a fresh build is done.

set -euo pipefail

# ── colours / helpers ────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'
PASS=0; FAIL=0; SKIP=0

log_pass() { PASS=$((PASS + 1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL + 1)); printf "${RED}FAIL${NC}  %s  (%s)\n" "$1" "$2"; }
log_skip() { SKIP=$((SKIP + 1)); printf "${YELLOW}SKIP${NC}  %s  (%s)\n" "$1" "$2"; }

# ── prerequisites ────────────────────────────────────────────────────
for cmd in jq; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "Required tool not found: $cmd" >&2
        exit 1
    fi
done

# ── build / locate binary ───────────────────────────────────────────
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
BUILT_BINARY=false
ACP_PID=""

if [ "${1:-}" != "" ]; then
    BIN="$1"
    if [ ! -x "$BIN" ]; then
        echo "Binary not found or not executable: $BIN" >&2
        exit 1
    fi
else
    BIN="$(mktemp)"
    echo "Building opencode …"
    (cd "$ROOT" && go build -o "$BIN" .) || { echo "Build failed"; exit 1; }
    BUILT_BINARY=true
fi

cleanup() {
    if [ -n "$ACP_PID" ] && kill -0 "$ACP_PID" 2>/dev/null; then
        kill -INT "$ACP_PID" 2>/dev/null || true
        wait "$ACP_PID" 2>/dev/null || true
    fi
    # Close fd 3 if still open.
    exec 3>&- 2>/dev/null || true
    rm -rf "$WORKDIR"
    if [ "$BUILT_BINARY" = true ] && [ -f "$BIN" ]; then
        rm -f "$BIN"
    fi
}
trap cleanup EXIT

# ── minimal project config ──────────────────────────────────────────
cat > "$WORKDIR/.opencode.json" << 'EOF'
{
  "agents": {
    "coder": {
      "model": "anthropic.claude-sonnet-4-6",
      "maxTokens": 1000
    }
  }
}
EOF

echo ""
echo "=== ACP Server E2E tests ==="
echo "Binary:   $BIN"
echo "Workdir:  $WORKDIR"
echo ""

# ── start ACP server ────────────────────────────────────────────────
# Use a named pipe for stdin only. Stdout goes to a regular file so
# that redirections never block (FIFOs block until both ends are open).
ACP_IN="$WORKDIR/acp_in"
ACP_STDOUT="$WORKDIR/acp_stdout"
mkfifo "$ACP_IN"
touch "$ACP_STDOUT"

(cd "$WORKDIR" && "$BIN" acp) < "$ACP_IN" >> "$ACP_STDOUT" 2>"$WORKDIR/acp.log" &
ACP_PID=$!

# Open the write end of the input pipe (keeps it open across sends).
exec 3>"$ACP_IN"

# ── helpers ──────────────────────────────────────────────────────────
# POS_FILE and ID_FILE track state across subshell invocations.
# send_request runs inside $(...) which forks a subshell, so normal
# shell variables don't propagate back to the parent.
POS_FILE="$WORKDIR/stdout_pos"
ID_FILE="$WORKDIR/msg_id"
echo "0" > "$POS_FILE"
echo "0" > "$ID_FILE"

# send_request: send a JSON-RPC request and read the response.
# Usage: resp=$(send_request "method" '{"key":"value"}')
send_request() {
    local method="$1" params="${2:-null}"
    local msg_id
    msg_id=$(( $(cat "$ID_FILE") + 1 ))
    echo "$msg_id" > "$ID_FILE"

    local msg
    msg=$(jq -cn --arg m "$method" --argjson p "$params" --argjson id "$msg_id" \
        '{jsonrpc:"2.0", id:$id, method:$m, params:$p}')

    # Send the message.
    echo "$msg" >&3

    # Poll the stdout file for a response with our id (up to 10s).
    local pos deadline
    pos=$(cat "$POS_FILE")
    deadline=$((SECONDS + 10))
    while [ "$SECONDS" -lt "$deadline" ]; do
        local total_lines
        total_lines=$(wc -l < "$ACP_STDOUT" 2>/dev/null | tr -d ' ')
        if [ "$total_lines" -gt "$pos" ]; then
            # Read lines from pos+1 onwards.
            local new_lines
            new_lines=$(tail -n +$((pos + 1)) "$ACP_STDOUT")
            while IFS= read -r line; do
                pos=$((pos + 1))
                [ -z "$line" ] && continue

                local resp_id
                resp_id=$(echo "$line" | jq -r '.id // empty' 2>/dev/null) || continue
                if [ "$resp_id" = "$msg_id" ]; then
                    echo "$pos" > "$POS_FILE"
                    echo "$line"
                    return 0
                fi
            done <<< "$new_lines"
        fi
        sleep 0.1
    done

    echo '{"error":"timeout reading response"}'
    return 1
}

# Wait for ACP to be ready (up to 15s). Poll for the "ACP server started"
# log line and verify the process is still alive.
READY=false
for i in $(seq 1 30); do
    if ! kill -0 "$ACP_PID" 2>/dev/null; then
        printf "${RED}ERROR${NC}  ACP process died during startup\n"
        echo "ACP log:"
        cat "$WORKDIR/acp.log" 2>/dev/null || echo "(no log)"
        exit 1
    fi
    if grep -q "ACP server started" "$WORKDIR/acp.log" 2>/dev/null; then
        READY=true
        break
    fi
    sleep 0.5
done

if [ "$READY" != true ]; then
    printf "${RED}ERROR${NC}  ACP did not become ready within 15s\n"
    echo "ACP log:"
    cat "$WORKDIR/acp.log" 2>/dev/null || echo "(no log)"
    exit 1
fi
log_pass "ACP process starts"

# ── 1. Initialize ───────────────────────────────────────────────────
name="initialize"
resp=$(send_request "initialize" '{"protocolVersion":1}')
proto=$(echo "$resp" | jq -r '.result.protocolVersion')
agent_name=$(echo "$resp" | jq -r '.result.agentInfo.name')
load_session=$(echo "$resp" | jq -r '.result.agentCapabilities.loadSession')
if [ "$proto" = "1" ] && [ "$agent_name" = "OpenCode" ] && [ "$load_session" = "true" ]; then
    log_pass "$name (protocol=$proto, agent=$agent_name)"
else
    log_fail "$name" "got: $resp"
fi

# ── 2. Session new ──────────────────────────────────────────────────
name="session/new"
resp=$(send_request "session/new" "{\"cwd\":\"$WORKDIR\"}")
session_id=$(echo "$resp" | jq -r '.result.sessionId')
has_models=$(echo "$resp" | jq -e '.result.models' >/dev/null 2>&1 && echo "true" || echo "false")
if [ -n "$session_id" ] && [ "$session_id" != "null" ]; then
    log_pass "$name (id=$session_id, models=$has_models)"
else
    log_fail "$name" "got: $resp"
fi

# ── 3. Session list ─────────────────────────────────────────────────
name="session/list"
resp=$(send_request "session/list" "{\"cwd\":\"$WORKDIR\"}")
session_count=$(echo "$resp" | jq '.result.sessions | length')
found=$(echo "$resp" | jq -r ".result.sessions[] | select(.sessionId==\"$session_id\") | .sessionId")
if [ "$session_count" -gt 0 ] && [ "$found" = "$session_id" ]; then
    log_pass "$name ($session_count sessions, found ours)"
else
    log_fail "$name" "got: $resp"
fi

# ── 4. Session load ─────────────────────────────────────────────────
name="session/load"
resp=$(send_request "session/load" "{\"sessionId\":\"$session_id\",\"cwd\":\"$WORKDIR\"}")
loaded_id=$(echo "$resp" | jq -r '.result.sessionId')
if [ "$loaded_id" = "$session_id" ]; then
    log_pass "$name"
else
    log_fail "$name" "got: $resp"
fi

# ── 5. Session resume ───────────────────────────────────────────────
name="session/resume"
resp=$(send_request "session/resume" "{\"sessionId\":\"$session_id\",\"cwd\":\"$WORKDIR\"}")
resumed_id=$(echo "$resp" | jq -r '.result.sessionId')
if [ "$resumed_id" = "$session_id" ]; then
    log_pass "$name"
else
    log_fail "$name" "got: $resp"
fi

# ── 6. Session close ────────────────────────────────────────────────
name="session/close"
resp=$(send_request "session/close" "{\"sessionId\":\"$session_id\"}")
has_result=$(echo "$resp" | jq -e '.result' >/dev/null 2>&1 && echo "true" || echo "false")
if [ "$has_result" = "true" ]; then
    log_pass "$name"
else
    log_fail "$name" "got: $resp"
fi

# ── 7. Unknown method returns error ─────────────────────────────────
name="unknown method → error"
resp=$(send_request "bogus/method" '{}')
err_code=$(echo "$resp" | jq -r '.error.code')
if [ "$err_code" = "-32601" ]; then
    log_pass "$name (code=$err_code)"
else
    log_fail "$name" "expected -32601, got: $resp"
fi

# ── 8. Invalid params ───────────────────────────────────────────────
name="session/load with bad session → error"
resp=$(send_request "session/load" '{"sessionId":"nonexistent-00000","cwd":"/tmp"}')
has_error=$(echo "$resp" | jq -e '.error' >/dev/null 2>&1 && echo "true" || echo "false")
if [ "$has_error" = "true" ]; then
    log_pass "$name"
else
    log_fail "$name" "expected error, got: $resp"
fi

# ── 9. Clean shutdown ───────────────────────────────────────────────
name="clean shutdown on pipe close"
exec 3>&- # close the write end of the input pipe
wait "$ACP_PID" 2>/dev/null
exit_code=$?
ACP_PID="" # prevent cleanup from trying to kill again
if [ "$exit_code" -eq 0 ]; then
    log_pass "$name (exit code 0)"
else
    log_fail "$name" "expected exit 0, got $exit_code"
fi

# ── summary ──────────────────────────────────────────────────────────
echo ""
printf "=== Results: ${GREEN}%d passed${NC}, ${RED}%d failed${NC}, ${YELLOW}%d skipped${NC} ===\n" "$PASS" "$FAIL" "$SKIP"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
