#!/usr/bin/env bash
# E2E blackbox test for `opencode serve --pool-mode` — the pod-pool
# runtime contract from the agent-pod-pool-runtime openspec change.
#
# Everything here is cross-process behaviour that unit tests cannot
# reach: the CLI flag surface, the boot-time bound-workspace derivation
# (including the $AGENT_WORKSPACE_GIT_URL path a real pod entrypoint
# produces), the sentinel file the pod writes for its own next
# boot, the exit-for-respawn cycle, and the fact that per-Job pods 404 on
# the pool routes.
#
# The full-lifecycle case is driven the way the orchestrator drives it:
#   unbound boot -> POST /flow rejected -> POST /pool/bind -> process
#   exits -> the entrypoint clones and re-execs -> pod reports bound ->
#   POST /pool/bind is idempotent -> POST /flow/recycle drains, clears
#   the binding, and exits 0.
#
# Usage:  ./scripts/test/pool_mode.sh [path-to-binary]

set -euo pipefail

# ── colours / helpers ────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'
PASS=0; FAIL=0; SKIP=0

log_pass() { PASS=$((PASS + 1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL + 1)); printf "${RED}FAIL${NC}  %s  (%s)\n" "$1" "$2"; }
log_skip() { SKIP=$((SKIP + 1)); printf "${YELLOW}SKIP${NC}  %s  (%s)\n" "$1" "$2"; }

for cmd in curl jq git; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "Required tool not found: $cmd" >&2
        exit 1
    fi
done

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
BUILT_BINARY=false
SERVER_PID=""

if [ "${1:-}" != "" ]; then
    BIN="$1"
    [ -x "$BIN" ] || { echo "Binary not found or not executable: $BIN" >&2; exit 1; }
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

# The pod's working directory (the entrypoint's /workspace) and the
# emptyDir the bind sentinel lives on (its /pool-state).
POD_CWD="$WORKDIR/workspace"
POOL_STATE="$WORKDIR/pool-state"
SENTINEL="$POOL_STATE/bind"
mkdir -p "$POD_CWD" "$POOL_STATE"

# Isolate HOME: config.Load merges ~/.opencode.json, and a developer's
# real one very often has bridge channels enabled — which would trip the
# pool-mode inbound boot guard and fail every case below for a reason
# that has nothing to do with the code under test.
export HOME="$WORKDIR/home"
export XDG_CONFIG_HOME="$WORKDIR/home/.config"
mkdir -p "$HOME" "$XDG_CONFIG_HOME"

WORKSPACE_URL="https://git.example.com/acme/agents/developer"
OTHER_URL="https://git.example.com/acme/agents/other"
export WORKSPACE_GIT_URLS_ALLOWLIST="$WORKSPACE_URL,$OTHER_URL"

# A provider key is required for any agent to be constructed at all, and
# HOME is isolated above so the developer's real credentials are not in
# play. The key is never used — nothing in this script performs
# inference.
cat > "$POD_CWD/.opencode.json" << 'EOF'
{
  "providers": {
    "anthropic": {"apiKey": "e2e-placeholder-not-a-real-key"}
  },
  "agents": {
    "coder": {
      "model": "anthropic.claude-sonnet-4-6",
      "maxTokens": 1000
    }
  }
}
EOF

free_port() {
    python3 -c "import socket; s=socket.socket(); s.bind(('',0)); print(s.getsockname()[1]); s.close()"
}

# start_pod [extra serve args...] — launches the pod with the current
# AGENT_WORKSPACE_GIT_URL (empty unless the caller exported one, exactly
# like the pod entrypoint) and waits for it to answer /global/health.
start_pod() {
    PORT=$(free_port)
    BASE="http://127.0.0.1:$PORT"
    LOG="$WORKDIR/pod-$PORT.log"
    (cd "$POD_CWD" && "$BIN" serve \
        --pool-mode \
        --port "$PORT" \
        --hostname 127.0.0.1 \
        --pool-bind-sentinel-path "$SENTINEL" \
        --pool-bind-exit-grace 300ms \
        --pool-drain-grace 300ms \
        --flow-idle-reset-grace 1s \
        "$@") >"$LOG" 2>&1 &
    SERVER_PID=$!
    for _ in $(seq 1 40); do
        if curl -sf "$BASE/global/health" >/dev/null 2>&1; then
            return 0
        fi
        if ! kill -0 "$SERVER_PID" 2>/dev/null; then
            echo "pod exited during startup; log:" >&2
            cat "$LOG" >&2
            return 1
        fi
        sleep 0.5
    done
    echo "pod did not become ready; log:" >&2
    cat "$LOG" >&2
    return 1
}

stop_pod() {
    if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill -INT "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    SERVER_PID=""
}

# status_of METHOD PATH [BODY] — HTTP status code only.
status_of() {
    local method="$1" path="$2" body="${3:-}"
    if [ -n "$body" ]; then
        curl -s -o /dev/null -w '%{http_code}' -X "$method" \
            -H "Content-Type: application/json" -d "$body" "$BASE$path"
    else
        curl -s -o /dev/null -w '%{http_code}' -X "$method" "$BASE$path"
    fi
}

# body_of METHOD PATH [BODY] — response body regardless of status.
body_of() {
    local method="$1" path="$2" body="${3:-}"
    if [ -n "$body" ]; then
        curl -s -X "$method" -H "Content-Type: application/json" -d "$body" "$BASE$path"
    else
        curl -s -X "$method" "$BASE$path"
    fi
}

expect_status() {
    local name="$1" want="$2" got="$3" extra="${4:-}"
    if [ "$got" = "$want" ]; then
        log_pass "$name ($got)"
    else
        log_fail "$name" "want $want, got $got${extra:+ — $extra}"
    fi
}

echo ""
echo "=== pool-mode E2E tests ==="
echo "Binary:   $BIN"
echo "Workdir:  $WORKDIR"
echo ""

# ── 0. flag validation (process must refuse to start) ───────────────
name="--pool-mode with --flow is rejected"
if (cd "$POD_CWD" && "$BIN" serve --pool-mode --flow some-flow --port 1 >"$WORKDIR/mutex.log" 2>&1); then
    log_fail "$name" "process started anyway"
else
    if grep -q "mutually exclusive" "$WORKDIR/mutex.log"; then
        log_pass "$name"
    else
        log_fail "$name" "exited non-zero but without the mutual-exclusion error: $(head -3 "$WORKDIR/mutex.log")"
    fi
fi

name="--flow-idle-reset-grace above its ceiling is rejected"
if (cd "$POD_CWD" && "$BIN" serve --pool-mode --flow-idle-reset-grace 60s --port 1 >"$WORKDIR/grace.log" 2>&1); then
    log_fail "$name" "process started anyway"
else
    grep -q "flow-idle-reset-grace" "$WORKDIR/grace.log" \
        && log_pass "$name" \
        || log_fail "$name" "$(head -3 "$WORKDIR/grace.log")"
fi

# ── 1. unbound boot ─────────────────────────────────────────────────
unset AGENT_WORKSPACE_GIT_URL || true
start_pod || exit 1

name="unbound pool pod reports pool.boundWorkspace = null"
resp=$(body_of GET /global/health)
bound=$(echo "$resp" | jq -r '.pool.boundWorkspace')
mode=$(echo "$resp" | jq -r '.pool.mode')
if [ "$bound" = "null" ] && [ "$mode" = "available" ]; then
    log_pass "$name (mode=$mode)"
else
    log_fail "$name" "boundWorkspace=$bound mode=$mode — $resp"
fi

name="GET /pool/bind on an unbound pod"
resp=$(body_of GET /pool/bind)
if [ "$(echo "$resp" | jq -r '.boundWorkspace')" = "null" ]; then
    log_pass "$name"
else
    log_fail "$name" "$resp"
fi

expect_status "POST /flow before bind is refused" 400 \
    "$(status_of POST /flow '{"flowID":"anything"}')"

expect_status "POST /pool/bind rejects a workspace outside the allowlist" 403 \
    "$(status_of POST /pool/bind '{"workspace":"https://gitlab.com/evil/repo"}')"

name="a rejected bind writes no sentinel"
if [ -f "$SENTINEL" ]; then
    log_fail "$name" "sentinel exists: $(cat "$SENTINEL")"
else
    log_pass "$name"
fi

# ── 2. bind → exit for respawn ──────────────────────────────────────
name="POST /pool/bind accepts an allowlisted workspace"
resp=$(body_of POST /pool/bind "{\"workspace\":\"$WORKSPACE_URL.git\"}")
code=$(status_of POST /pool/bind "{\"workspace\":\"$WORKSPACE_URL\"}" || true)
if [ "$(echo "$resp" | jq -r '.binding')" = "$WORKSPACE_URL" ]; then
    log_pass "$name (trailing .git normalised away)"
else
    log_fail "$name" "$resp"
fi

name="bind writes the sentinel for the next boot"
if [ -f "$SENTINEL" ] && [ "$(cat "$SENTINEL")" = "$WORKSPACE_URL" ]; then
    log_pass "$name"
else
    log_fail "$name" "sentinel=$( [ -f "$SENTINEL" ] && cat "$SENTINEL" || echo MISSING)"
fi

name="the pod exits by itself after the bind grace"
exited=false
for _ in $(seq 1 40); do
    if ! kill -0 "$SERVER_PID" 2>/dev/null; then
        exited=true
        break
    fi
    sleep 0.25
done
if [ "$exited" = true ]; then
    wait "$SERVER_PID" 2>/dev/null && rc=0 || rc=$?
    SERVER_PID=""
    if [ "${rc:-0}" -eq 0 ]; then
        log_pass "$name (exit 0)"
    else
        log_fail "$name" "exit code $rc"
    fi
else
    log_fail "$name" "still running after 10s"
    stop_pod
fi

# ── 3. respawn: the entrypoint's bootstrap, then a bound pod ────────
# This mirrors a real pod entrypoint exactly: read the sentinel, clone into
# a TEMP dir, overlay only the agent subset into the working directory —
# so the working directory never becomes a git checkout — and export
# AGENT_WORKSPACE_GIT_URL. Deriving the binding from a `.git` in the
# working directory alone is what made every pool pod boot unbound
# forever, so this is the shape the derivation has to survive.
BIND_URL="$(cat "$SENTINEL")"
CLONE_SRC="$WORKDIR/clone-src"
mkdir -p "$CLONE_SRC/.agents"
echo "# workspace" > "$CLONE_SRC/AGENTS.md"
cp -R "$CLONE_SRC/.agents" "$POD_CWD/.agents"
cp "$CLONE_SRC/AGENTS.md" "$POD_CWD/AGENTS.md"
export AGENT_WORKSPACE_GIT_URL="$BIND_URL"

name="working directory is deliberately NOT a git checkout"
if [ -d "$POD_CWD/.git" ]; then
    log_fail "$name" "the fixture no longer reproduces the entrypoint's overlay bootstrap"
else
    log_pass "$name"
fi

start_pod || exit 1

name="respawned pod reports the bound workspace"
resp=$(body_of GET /global/health)
bound=$(echo "$resp" | jq -r '.pool.boundWorkspace')
if [ "$bound" = "$WORKSPACE_URL" ]; then
    log_pass "$name ($bound)"
else
    log_fail "$name" "boundWorkspace=$bound — $resp"
fi

name="GET /pool/bind reports the binding with a since timestamp"
resp=$(body_of GET /pool/bind)
if [ "$(echo "$resp" | jq -r '.boundWorkspace')" = "$WORKSPACE_URL" ] \
    && [ "$(echo "$resp" | jq -r '.since')" != "null" ]; then
    log_pass "$name"
else
    log_fail "$name" "$resp"
fi

name="re-binding the same workspace is an idempotent 200"
resp=$(body_of POST /pool/bind "{\"workspace\":\"$WORKSPACE_URL\"}")
code=$(status_of POST /pool/bind "{\"workspace\":\"$WORKSPACE_URL\"}")
if [ "$code" = "200" ] && [ "$(echo "$resp" | jq -r '.alreadyBound')" = "true" ]; then
    log_pass "$name"
else
    log_fail "$name" "status=$code body=$resp"
fi

expect_status "binding to a different workspace is refused" 409 \
    "$(status_of POST /pool/bind "{\"workspace\":\"$OTHER_URL\"}")"

expect_status "POST /flow asserting a mismatched workspace is refused" 409 \
    "$(status_of POST /flow "{\"flowID\":\"x\",\"workspace\":\"$OTHER_URL\"}")"

name="POST /flow with mcpAuth but no mcpAuthServer is refused"
code=$(status_of POST /flow '{"flowID":"x","mcpAuth":"T1"}')
body=$(body_of POST /flow '{"flowID":"x","mcpAuth":"T1"}')
if [ "$code" = "400" ] && [ "$(echo "$body" | jq -r '.error')" = "mcpAuthServer required when mcpAuth is set" ]; then
    log_pass "$name"
else
    log_fail "$name" "status=$code body=$body"
fi

# A bound pod with a matching workspace gets past every pool gate; the
# flow itself does not exist, so 404 is the proof that the gates passed.
expect_status "a matching workspace passes the pool gates" 404 \
    "$(status_of POST /flow "{\"flowID\":\"no-such-flow\",\"workspace\":\"$WORKSPACE_URL\"}")"

# ── per-run LLM + telemetry identity (design D10) ───────────────────
# The pool pod boots with the SHARED endpoint key and a static telemetry
# identity, because no team is known until a job arrives. These fields
# carry the per-run values so a pooled run bills and attributes exactly
# as the per-Job pod it replaced would have.
#
# The flow does not exist, so the gates answering 404 (rather than 400)
# is the proof the fields parsed and were accepted; the unit tests cover
# what the pod then does with them. What e2e adds here is the thing unit
# tests cannot see: that a real HTTP round-trip with a credential in the
# body never echoes it back.
expect_status "POST /flow accepts the per-run LLM and telemetry identity" 404 \
    "$(status_of POST /flow "{\"flowID\":\"no-such-flow\",\"llmApiKey\":\"sk-e2e-team-key\",\"telemetryUserId\":\"acme-dev\",\"telemetryTeam\":\"acme\"}")"

name="the per-run LLM key is never echoed back to the caller"
leak=$(body_of POST /flow '{"flowID":"no-such-flow","llmApiKey":"sk-e2e-team-key"}')
leak="$leak$(body_of GET /flow/status)$(body_of GET /global/health)"
if echo "$leak" | grep -q "sk-e2e-team-key"; then
    log_fail "$name" "the key appears in a response body: $leak"
else
    log_pass "$name"
fi

name="the per-run LLM key never reaches the pod log"
if grep -q "sk-e2e-team-key" "$LOG"; then
    log_fail "$name" "the key appears in $LOG"
else
    log_pass "$name"
fi

name="GET /flow/status for an unknown runID is 404, not idle"
code=$(status_of GET "/flow/status?runID=nope")
idle=$(body_of GET /flow/status | jq -r '.status')
if [ "$code" = "404" ] && [ "$idle" = "idle" ]; then
    log_pass "$name (unqualified read still reports idle)"
else
    log_fail "$name" "runID status=$code, unqualified=$idle"
fi

# ── 4. recycle drains, unbinds and exits ────────────────────────────
name="POST /flow/recycle is accepted when idle"
resp=$(body_of POST /flow/recycle '{"reason":"e2e"}')
if [ "$(echo "$resp" | jq -r '.draining')" = "true" ]; then
    log_pass "$name"
else
    log_fail "$name" "$resp"
fi

name="health reports draining and new work is refused"
health=$(body_of GET /global/health)
flow_code=$(status_of POST /flow "{\"flowID\":\"x\",\"workspace\":\"$WORKSPACE_URL\"}")
bind_code=$(status_of POST /pool/bind "{\"workspace\":\"$WORKSPACE_URL\"}")
if [ "$(echo "$health" | jq -r '.pool.mode')" = "draining" ] \
    && [ "$(echo "$health" | jq -r '.pool.draining')" = "true" ] \
    && [ "$flow_code" = "503" ] && [ "$bind_code" = "503" ]; then
    log_pass "$name (reads stay open, writes 503)"
else
    log_fail "$name" "mode=$(echo "$health" | jq -r '.pool.mode') flow=$flow_code bind=$bind_code"
fi

name="recycle clears the binding so the respawn comes back empty"
if [ -f "$SENTINEL" ]; then
    log_fail "$name" "sentinel survived recycle: $(cat "$SENTINEL")"
else
    log_pass "$name"
fi

name="the pod exits 0 after the drain grace"
exited=false
for _ in $(seq 1 40); do
    if ! kill -0 "$SERVER_PID" 2>/dev/null; then
        exited=true
        break
    fi
    sleep 0.25
done
if [ "$exited" = true ]; then
    wait "$SERVER_PID" 2>/dev/null && rc=0 || rc=$?
    SERVER_PID=""
    [ "${rc:-0}" -eq 0 ] && log_pass "$name (exit 0)" || log_fail "$name" "exit code $rc"
else
    log_fail "$name" "still running after 10s"
    stop_pod
fi

# ── 5. non-pool pods must not expose the pool surface ───────────────
unset AGENT_WORKSPACE_GIT_URL || true
PORT=$(free_port)
BASE="http://127.0.0.1:$PORT"
(cd "$POD_CWD" && "$BIN" serve --port "$PORT" --hostname 127.0.0.1) >"$WORKDIR/plain.log" 2>&1 &
SERVER_PID=$!
READY=false
for _ in $(seq 1 40); do
    curl -sf "$BASE/global/health" >/dev/null 2>&1 && { READY=true; break; }
    sleep 0.5
done
if [ "$READY" != true ]; then
    log_fail "plain serve becomes ready" "$(tail -5 "$WORKDIR/plain.log")"
else
    name="a non-pool pod omits the pool health block"
    if [ "$(body_of GET /global/health | jq 'has("pool")')" = "false" ]; then
        log_pass "$name"
    else
        log_fail "$name" "$(body_of GET /global/health)"
    fi

    expect_status "a non-pool pod 404s POST /pool/bind" 404 "$(status_of POST /pool/bind '{"workspace":"x"}')"
    expect_status "a non-pool pod 404s GET /pool/bind" 404 "$(status_of GET /pool/bind)"
    expect_status "a non-pool pod 404s POST /flow/recycle" 404 "$(status_of POST /flow/recycle '{}')"
fi
stop_pod

# ── 6. boot guard: pool mode requires inbound:disabled ──────────────
GUARD_DIR="$WORKDIR/guard"
mkdir -p "$GUARD_DIR"
cat > "$GUARD_DIR/.opencode.json" << 'EOF'
{
  "providers": {"anthropic": {"apiKey": "e2e-placeholder-not-a-real-key"}},
  "router": {
    "channels": {
      "slack": {
        "enabled": true,
        "apps": [
          {"id": "default", "enabled": true, "inbound": "enabled",
           "botToken": "xoxb-test", "appToken": "xapp-test"}
        ]
      }
    }
  }
}
EOF
name="pool mode refuses to boot with an inbound-enabled bridge identity"
if (cd "$GUARD_DIR" && "$BIN" serve --pool-mode --port "$(free_port)" --hostname 127.0.0.1 >"$WORKDIR/guard.log" 2>&1); then
    log_fail "$name" "process started anyway"
else
    if grep -q "inbound:disabled" "$WORKDIR/guard.log"; then
        log_pass "$name"
    else
        log_fail "$name" "exited without the boot-guard error: $(tail -3 "$WORKDIR/guard.log")"
    fi
fi

cat > "$GUARD_DIR/.opencode.json" << 'EOF'
{
  "providers": {"anthropic": {"apiKey": "e2e-placeholder-not-a-real-key"}},
  "router": {
    "channels": {
      "slack": {
        "enabled": true,
        "apps": [
          {"id": "default", "enabled": true, "inbound": "disabled",
           "botToken": "xoxb-test", "appToken": "xapp-test"}
        ]
      }
    }
  }
}
EOF
name="pool mode boots with inbound:disabled"
PORT=$(free_port)
BASE="http://127.0.0.1:$PORT"
(cd "$GUARD_DIR" && "$BIN" serve --pool-mode --port "$PORT" --hostname 127.0.0.1 \
    --pool-bind-sentinel-path "$WORKDIR/guard-sentinel") >"$WORKDIR/guard-ok.log" 2>&1 &
SERVER_PID=$!
READY=false
for _ in $(seq 1 40); do
    curl -sf "$BASE/global/health" >/dev/null 2>&1 && { READY=true; break; }
    kill -0 "$SERVER_PID" 2>/dev/null || break
    sleep 0.5
done
if [ "$READY" = true ]; then
    log_pass "$name"
else
    log_fail "$name" "$(tail -5 "$WORKDIR/guard-ok.log")"
fi
stop_pod

# ── summary ─────────────────────────────────────────────────────────
echo ""
echo "──────────────────────────────────"
printf "Passed: ${GREEN}%d${NC}   Failed: ${RED}%d${NC}   Skipped: ${YELLOW}%d${NC}\n" "$PASS" "$FAIL" "$SKIP"
echo "──────────────────────────────────"
[ "$FAIL" -eq 0 ]
