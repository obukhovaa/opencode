#!/usr/bin/env bash
# E2E blackbox test for the tool environment deny list.
#
# The invariant: a command the model runs through the bash tool's shell cannot
# see opencode's own credentials, nor names an operator denied in
# .opencode.json or OPENCODE_TOOL_ENV_DENY, while every other variable still
# passes. On Linux it also cannot read them back from opencode's
# /proc/<pid>/environ.
#
# Unit tests cover each spawn site; this one drives the real config file →
# viper → toolenv path in a separate process, which they cannot.
#
# Usage:  ./scripts/test/tool_env.sh
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'
PASS=0; FAIL=0; SKIP=0
log_pass() { PASS=$((PASS + 1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL + 1)); printf "${RED}FAIL${NC}  %s  (%s)\n" "$1" "$2"; }
log_skip() { SKIP=$((SKIP + 1)); printf "${YELLOW}SKIP${NC}  %s  (%s)\n" "$1" "$2"; }

if ! command -v jq &>/dev/null; then
    echo "Required tool not found: jq" >&2
    exit 1
fi

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
DRIVER="$(mktemp)"
cleanup() { rm -rf "$WORKDIR"; rm -f "$DRIVER"; }
trap cleanup EXIT

echo "Building cmd/tool-env-e2e …"
(cd "$ROOT" && go build -o "$DRIVER" ./cmd/tool-env-e2e) || { echo "Build failed"; exit 1; }

# `-s` keeps the shell from sourcing the user's profile, which could export
# anything and make the run non-hermetic.
cat > "$WORKDIR/.opencode.json" <<'JSON'
{
  "shell": { "path": "/bin/bash", "args": ["-s"] },
  "toolEnv": { "deny": ["E2E_CONFIG_DENIED", "E2E_GLOB_*"] }
}
JSON

OUT="$(cd "$WORKDIR" && env \
    OPENCODE_SERVER_PASSWORD=e2e-api-secret \
    OPENCODE_BRIDGE_REGISTRAR_PASSWORD=e2e-registrar-secret \
    OPENCODE_MYSQL_DSN=e2e-dsn-secret \
    E2E_CONFIG_DENIED=e2e-config-secret \
    E2E_GLOB_TOKEN=e2e-glob-secret \
    E2E_ENV_DENIED=e2e-env-secret \
    OPENCODE_TOOL_ENV_DENY=E2E_ENV_DENIED \
    E2E_KEPT=e2e-kept-value \
    ANTHROPIC_API_KEY=sk-ant-e2e \
    "$DRIVER")"

if ! printf '%s' "$OUT" | jq -e 'type == "object"' >/dev/null 2>&1; then
    echo "Driver result is not a JSON object:" >&2
    printf '%s\n' "$OUT" >&2
    exit 1
fi
field() { printf '%s' "$OUT" | jq -r "$1"; }
ENV_OUT="$(field '.env')"

for secret in e2e-api-secret e2e-registrar-secret e2e-dsn-secret e2e-config-secret e2e-glob-secret e2e-env-secret; do
    name="bash shell does not see $secret"
    if printf '%s' "$ENV_OUT" | grep -q "$secret"; then
        log_fail "$name" "it appears in env output"
    else
        log_pass "$name"
    fi
done

name="undenied variable still passes"
if printf '%s' "$ENV_OUT" | grep -qx "E2E_KEPT=e2e-kept-value"; then
    log_pass "$name"
else
    log_fail "$name" "E2E_KEPT missing from env output"
fi

name="opencode's /proc environ is unreadable from the shell"
if [ "$(field '.proc_checked')" != "true" ]; then
    log_skip "$name" "no /proc on this platform"
elif [ "$(field '.hardened')" = "true" ] && [ "$(field '.proc_environ_readable')" = "false" ]; then
    log_pass "$name"
else
    log_fail "$name" "hardened=$(field '.hardened') readable=$(field '.proc_environ_readable')"
fi

echo
printf "passed=%d failed=%d skipped=%d\n" "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
