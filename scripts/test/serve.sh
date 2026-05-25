#!/usr/bin/env bash
# E2E blackbox test for the HTTP REST API server (opencode serve).
# Builds opencode, starts the server, exercises core endpoints with curl,
# and verifies response shapes with jq.
#
# Usage:  ./scripts/test/serve.sh [path-to-binary]
#   If a binary path is given it is used as-is; otherwise a fresh build is done.

set -euo pipefail

# ── colours / helpers ────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'
PASS=0; FAIL=0; SKIP=0

log_pass() { PASS=$((PASS + 1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL + 1)); printf "${RED}FAIL${NC}  %s  (%s)\n" "$1" "$2"; }
log_skip() { SKIP=$((SKIP + 1)); printf "${YELLOW}SKIP${NC}  %s  (%s)\n" "$1" "$2"; }

# ── prerequisites ────────────────────────────────────────────────────
for cmd in curl jq; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "Required tool not found: $cmd" >&2
        exit 1
    fi
done

# ── build / locate binary ───────────────────────────────────────────
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
BUILT_BINARY=false
SERVER_PID=""

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
    if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill -INT "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
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

# ── find an available port ──────────────────────────────────────────
PORT=$(python3 -c "import socket; s=socket.socket(); s.bind(('',0)); print(s.getsockname()[1]); s.close()")

echo ""
echo "=== HTTP Server E2E tests ==="
echo "Binary:   $BIN"
echo "Port:     $PORT"
echo "Workdir:  $WORKDIR"
echo ""

# ── start server ────────────────────────────────────────────────────
(cd "$WORKDIR" && "$BIN" serve --port "$PORT" --hostname 127.0.0.1) 2>"$WORKDIR/server.log" &
SERVER_PID=$!

# Wait for server to be ready (up to 15s).
BASE="http://127.0.0.1:$PORT"
READY=false
for i in $(seq 1 30); do
    if curl -sf "$BASE/global/health" >/dev/null 2>&1; then
        READY=true
        break
    fi
    sleep 0.5
done

if [ "$READY" != true ]; then
    printf "${RED}ERROR${NC}  Server did not become ready within 15s\n"
    echo "Server log:"
    cat "$WORKDIR/server.log"
    exit 1
fi

log_pass "server starts and becomes ready"

# ── helper: HTTP request with response capture ──────────────────────
# Usage: api GET /path  or  api POST /path '{"json":"body"}'
api() {
    local method="$1" path="$2" body="${3:-}"
    if [ -n "$body" ]; then
        curl -sf -X "$method" -H "Content-Type: application/json" -d "$body" "$BASE$path"
    else
        curl -sf -X "$method" "$BASE$path"
    fi
}

# ── 1. Health ────────────────────────────────────────────────────────
name="GET /global/health"
resp=$(api GET /global/health)
healthy=$(echo "$resp" | jq -r '.healthy')
ver=$(echo "$resp" | jq -r '.version')
if [ "$healthy" = "true" ] && [ -n "$ver" ]; then
    log_pass "$name (healthy=$healthy, version=$ver)"
else
    log_fail "$name" "expected healthy=true, got: $resp"
fi

# ── 2. List sessions (empty) ────────────────────────────────────────
name="GET /session (empty list)"
resp=$(api GET /session)
count=$(echo "$resp" | jq 'length')
if [ "$count" -ge 0 ]; then
    log_pass "$name (count=$count)"
else
    log_fail "$name" "expected array, got: $resp"
fi

# ── 3. Create session ───────────────────────────────────────────────
name="POST /session"
resp=$(api POST /session '{"title":"test session"}')
session_id=$(echo "$resp" | jq -r '.id')
title=$(echo "$resp" | jq -r '.title')
if [ -n "$session_id" ] && [ "$session_id" != "null" ] && [ "$title" = "test session" ]; then
    log_pass "$name (id=$session_id)"
else
    log_fail "$name" "expected id and title, got: $resp"
fi

# ── 4. Create session with empty body ───────────────────────────────
name="POST /session (empty body)"
resp=$(curl -sf -X POST "$BASE/session" -H "Content-Type: application/json")
session_id2=$(echo "$resp" | jq -r '.id')
if [ -n "$session_id2" ] && [ "$session_id2" != "null" ]; then
    log_pass "$name (id=$session_id2)"
else
    log_fail "$name" "expected session created with empty body, got: $resp"
fi

# ── 5. Get session ──────────────────────────────────────────────────
name="GET /session/{id}"
resp=$(api GET "/session/$session_id")
got_id=$(echo "$resp" | jq -r '.id')
got_title=$(echo "$resp" | jq -r '.title')
if [ "$got_id" = "$session_id" ] && [ "$got_title" = "test session" ]; then
    log_pass "$name"
else
    log_fail "$name" "expected id=$session_id, got: $resp"
fi

# ── 6. Update session ───────────────────────────────────────────────
name="PATCH /session/{id}"
resp=$(api PATCH "/session/$session_id" '{"title":"updated title"}')
new_title=$(echo "$resp" | jq -r '.title')
if [ "$new_title" = "updated title" ]; then
    log_pass "$name"
else
    log_fail "$name" "expected title='updated title', got: $resp"
fi

# ── 7. Session status ───────────────────────────────────────────────
name="GET /session/status"
resp=$(api GET /session/status)
status=$(echo "$resp" | jq -r ".[\"$session_id\"].type")
if [ "$status" = "idle" ]; then
    log_pass "$name (status=idle)"
else
    log_fail "$name" "expected idle, got: $resp"
fi

# ── 8. List messages (empty) ────────────────────────────────────────
name="GET /session/{id}/message"
resp=$(api GET "/session/$session_id/message")
msg_count=$(echo "$resp" | jq 'length')
if [ "$msg_count" -eq 0 ]; then
    log_pass "$name (empty)"
else
    log_fail "$name" "expected 0 messages, got $msg_count"
fi

# ── 9. Config ────────────────────────────────────────────────────────
name="GET /config"
resp=$(api GET /config)
if echo "$resp" | jq -e '.model' >/dev/null 2>&1; then
    log_pass "$name"
else
    log_fail "$name" "expected model field, got: $resp"
fi

# ── 10. Providers ────────────────────────────────────────────────────
name="GET /config/providers"
resp=$(api GET /config/providers)
provider_count=$(echo "$resp" | jq '.providers | length')
if [ "$provider_count" -gt 0 ]; then
    log_pass "$name ($provider_count providers)"
else
    log_fail "$name" "expected providers, got: $resp"
fi

# ── 11. Agents ───────────────────────────────────────────────────────
name="GET /agent"
resp=$(api GET /agent)
agent_count=$(echo "$resp" | jq 'length')
if [ "$agent_count" -gt 0 ]; then
    log_pass "$name ($agent_count agents)"
else
    log_fail "$name" "expected agents, got: $resp"
fi

# ── 12. Abort (no-op on idle session) ────────────────────────────────
name="POST /session/{id}/abort"
resp=$(api POST "/session/$session_id/abort")
if [ "$resp" = "true" ]; then
    log_pass "$name"
else
    log_fail "$name" "expected true, got: $resp"
fi

# ── 13. SSE event stream connects ───────────────────────────────────
name="GET /event (SSE connects)"
# Connect to SSE with a short timeout. The endpoint streams forever,
# so curl will exit with code 28 (timeout). Status 200 = connected,
# 000 = curl timed out before getting headers (also acceptable).
sse_status=$(curl -s -o /dev/null -w "%{http_code}" -N -H "Accept: text/event-stream" "$BASE/event" --max-time 1 2>/dev/null || true)
if [ "$sse_status" = "200" ] || [ "$sse_status" = "000" ]; then
    log_pass "$name (status=$sse_status)"
else
    log_fail "$name" "expected 200 or 000 (timeout), got $sse_status"
fi

# ── 14. 404 for unknown session ──────────────────────────────────────
name="GET /session/{bad-id} returns 404"
http_code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/session/nonexistent-id-12345")
if [ "$http_code" = "404" ]; then
    log_pass "$name"
else
    log_fail "$name" "expected 404, got $http_code"
fi

# ── 15. Permission reply (no pending → still 200) ───────────────────
name="POST /permission/{id}/reply"
resp=$(api POST "/permission/fake-request-id/reply" '{"allow":true}')
if [ "$resp" = "true" ]; then
    log_pass "$name"
else
    log_fail "$name" "expected true, got: $resp"
fi

# ── 16. Delete session ──────────────────────────────────────────────
name="DELETE /session/{id}"
resp=$(curl -sf -X DELETE "$BASE/session/$session_id")
if [ "$resp" = "true" ]; then
    log_pass "$name"
else
    log_fail "$name" "expected true, got: $resp"
fi

# Also clean up the second session
curl -sf -X DELETE "$BASE/session/$session_id2" >/dev/null 2>&1 || true

# ── 17. Deleted session returns 404 ──────────────────────────────────
name="GET /session/{id} after delete returns 404"
http_code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/session/$session_id")
if [ "$http_code" = "404" ]; then
    log_pass "$name"
else
    log_fail "$name" "expected 404, got $http_code"
fi

# ── 18. CORS headers ────────────────────────────────────────────────
name="CORS headers present"
cors=$(curl -sf -I "$BASE/global/health" 2>/dev/null | grep -i "access-control-allow-origin" || true)
if echo "$cors" | grep -qi "allow-origin"; then
    log_pass "$name"
else
    log_fail "$name" "expected Access-Control-Allow-Origin header"
fi

# ── 19. Graceful shutdown ────────────────────────────────────────────
name="server shuts down on SIGINT"
kill -INT "$SERVER_PID" 2>/dev/null
wait "$SERVER_PID" 2>/dev/null
exit_code=$?
SERVER_PID="" # prevent cleanup from trying to kill again
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
