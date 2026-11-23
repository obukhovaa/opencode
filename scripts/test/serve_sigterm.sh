#!/usr/bin/env bash
# E2E: `opencode serve --flow` exit status on SIGTERM (GENAI-709).
#
# A per-Job agent pod runs opencode as PID 1, so a pod deletion / node drain
# delivers SIGTERM to it. The orchestrator's killed-step recovery needs, when
# a step was in flight: exit 143, and the step's flow_states row still
# `running` (it resumes only running rows). With nothing in flight the exit
# stays 0.
#
# The step's LLM call goes to a local HTTP server that accepts the request
# and never answers, so the step is genuinely mid-call when the signal lands.
# No real provider is contacted.
#
# Usage:  ./scripts/test/serve_sigterm.sh [path-to-binary]
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
PASS=0; FAIL=0
log_pass() { PASS=$((PASS + 1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL + 1)); printf "${RED}FAIL${NC}  %s  (%s)\n" "$1" "$2"; }

for cmd in curl python3; do
    command -v "$cmd" >/dev/null || { echo "Required tool not found: $cmd" >&2; exit 1; }
done

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SANDBOX="$(mktemp -d)"
SERVER_PID=""
HANG_PID=""
if [ "${1:-}" != "" ]; then
    BIN="$1"
else
    BIN="$SANDBOX/opencode"
    (cd "$ROOT" && go build -o "$BIN" .)
fi

cleanup() {
    for pid in "$SERVER_PID" "$HANG_PID"; do
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            kill -KILL "$pid" 2>/dev/null || true
            wait "$pid" 2>/dev/null || true
        fi
    done
    rm -rf "$SANDBOX"
}
trap cleanup EXIT

free_port() { python3 -c "import socket; s=socket.socket(); s.bind(('127.0.0.1',0)); print(s.getsockname()[1]); s.close()"; }

# ── a provider endpoint that hangs ───────────────────────────────────
HANG_PORT=$(free_port)
cat >"$SANDBOX/hang.py" <<'EOF'
import http.server, sys, time
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        open(sys.argv[2], "a").write(self.path + "\n")
        time.sleep(3600)
    def log_message(self, *a):
        pass
http.server.ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
EOF
python3 "$SANDBOX/hang.py" "$HANG_PORT" "$SANDBOX/requests.log" &
HANG_PID=$!

# ── workspace: config + one-step flow ────────────────────────────────
WORKDIR="$SANDBOX/work"
mkdir -p "$WORKDIR/.agents/flows" "$SANDBOX/home"
cat >"$WORKDIR/.opencode.json" <<EOF
{
  "providers": {"anthropic": {"apiKey": "test", "baseURL": "http://127.0.0.1:$HANG_PORT"}},
  "agents": {"coder": {"model": "claude-5-sonnet", "maxTokens": 1000}}
}
EOF
cat >"$WORKDIR/.agents/flows/sigterm-probe.yaml" <<'EOF'
name: SIGTERM probe
flow:
  steps:
    - id: probe
      agent: coder
      prompt: say hi
EOF

start_server() {
    PORT=$(free_port)
    (cd "$WORKDIR" && HOME="$SANDBOX/home" XDG_CONFIG_HOME="$SANDBOX/home/.config" \
        exec "$BIN" serve --port "$PORT" --hostname 127.0.0.1 "$@") >>"$SANDBOX/server.log" 2>&1 &
    SERVER_PID=$!
    for _ in $(seq 1 60); do
        curl -sf "http://127.0.0.1:$PORT/global/health" >/dev/null 2>&1 && return 0
        sleep 0.25
    done
    echo "server did not become ready"; cat "$SANDBOX/server.log"; exit 1
}

term_and_wait() {
    kill -TERM "$SERVER_PID"
    set +e
    wait "$SERVER_PID"
    EXIT_CODE=$?
    set -e
    SERVER_PID=""
}

# ── 1. idle server ───────────────────────────────────────────────────
start_server
term_and_wait
if [ "$EXIT_CODE" -eq 0 ]; then
    log_pass "SIGTERM with no flow in flight exits 0"
else
    log_fail "SIGTERM with no flow in flight exits 0" "exit $EXIT_CODE"
fi

# ── 2. step in flight ────────────────────────────────────────────────
start_server --flow sigterm-probe --flow-exit
for _ in $(seq 1 80); do
    [ -s "$SANDBOX/requests.log" ] && break
    sleep 0.25
done
if [ ! -s "$SANDBOX/requests.log" ]; then
    log_fail "flow step reached the provider" "no request seen"
    sed 's/^/    /' "$SANDBOX/server.log" | tail -40
else
    log_pass "flow step is mid LLM call"
    term_and_wait
    if [ "$EXIT_CODE" -eq 143 ]; then
        log_pass "SIGTERM mid-step exits 143"
    else
        log_fail "SIGTERM mid-step exits 143" "exit $EXIT_CODE"
        sed 's/^/    /' "$SANDBOX/server.log" | tail -40
    fi
    DB=$(find "$WORKDIR" "$SANDBOX/home" -name '*.db' 2>/dev/null | head -1)
    STATUS=$(python3 - "$DB" <<'EOF'
import sqlite3, sys
rows = sqlite3.connect(sys.argv[1]).execute(
    "select status from flow_states where step_id = 'probe'").fetchall()
print(",".join(r[0] for r in rows))
EOF
)
    if [ "$STATUS" = "running" ]; then
        log_pass "interrupted step row stays running"
    else
        log_fail "interrupted step row stays running" "status '$STATUS' in $DB"
    fi
fi

echo ""
printf "Results: %d passed, %d failed\n" "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
