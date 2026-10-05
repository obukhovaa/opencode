#!/usr/bin/env bash
# E2E: the shared limit knobs of workspace CLI tools (docs/cli-tools.md,
# "Limits"). One set of defaults — cliTools.timeout / maxTimeout /
# maxOutputBytes in .opencode.json, overridden by OPENCODE_CLI_TOOLS_* —
# reaches every manifest that leaves the field unset, on BOTH surfaces:
#
#   - the native tool in an agent's toolset (cmd/clitool-e2e: the real
#     config.Load → agent.NewToolSet → tool.Run pipeline), and
#   - `opencode tools serve` (JSON-RPC over stdio against the same sandbox).
#
# Cases: config cap and config timeout apply on both surfaces; a manifest's
# own field beats the config default; the environment beats the config
# block; `tools list --json` reports the resolved defaults with their source;
# an invalid knob is a warning (next layer applies) and fails --strict.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
PASS=0
FAIL=0
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT

log_pass() { printf '\033[32mPASS\033[0m %s\n' "$1"; PASS=$((PASS + 1)); }
log_fail() { printf '\033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
check() { if eval "$2"; then log_pass "$1"; else log_fail "$1"; fi; }

for dep in jq python3; do
  command -v "$dep" >/dev/null 2>&1 || { echo "missing dependency: $dep"; exit 1; }
done

if [ "${1:-}" != "" ] && [ -x "${1:-}" ]; then
  BIN="$1"
else
  BIN="$WORKDIR/opencode"
  (cd "$ROOT" && go build -o "$BIN" .) || { echo "build failed"; exit 1; }
fi
DRIVER="$WORKDIR/clitool-e2e"
(cd "$ROOT" && go build -o "$DRIVER" ./cmd/clitool-e2e) || { echo "driver build failed"; exit 1; }

# A developer's shell must not steer the run.
unset OPENCODE_CLI_TOOLS_TIMEOUT OPENCODE_CLI_TOOLS_MAX_TIMEOUT OPENCODE_CLI_TOOLS_MAX_OUTPUT_BYTES
export HOME="$WORKDIR/home" # keep the real ~/.agents/tools out of the run
mkdir -p "$HOME"

WS="$WORKDIR/ws"
mkdir -p "$WS/.agents/tools" "$WS/bin" "$WS/.git"
# spew prints ~20KB; nap sleeps past every timeout under test; nap_long
# sleeps 2s and carries its own 10s timeout.
printf '#!/bin/sh\ni=0; while [ $i -lt 400 ]; do echo "line $i of a long report that keeps going and going"; i=$((i+1)); done\n' > "$WS/bin/spew.sh"
printf '#!/bin/sh\nsleep 5\n' > "$WS/bin/nap.sh"
printf '#!/bin/sh\nsleep 2\necho done\n' > "$WS/bin/nap_long.sh"
chmod +x "$WS/bin/"*.sh
cat > "$WS/.opencode.json" <<JSON
{
  "cliTools": {"timeout": "1s", "maxOutputBytes": 1024},
  "agents": {"explorer": {"tools": {"spew": true, "nap": true, "nap_long": true}}}
}
JSON
cat > "$WS/.agents/tools/spew.yaml" <<YAML
name: spew
description: Prints a long report (inherits the shared output cap).
command: ./bin/spew.sh
permission: allow
YAML
cat > "$WS/.agents/tools/nap.yaml" <<YAML
name: nap
description: Sleeps 5s (inherits the shared timeout).
command: ./bin/nap.sh
permission: allow
YAML
cat > "$WS/.agents/tools/nap_long.yaml" <<YAML
name: nap_long
description: Sleeps 2s under its own 10s timeout (manifest field wins).
command: ./bin/nap_long.sh
timeout: 10s
permission: allow
YAML

# --- list: resolved defaults and their source ---------------------------
LIST_JSON="$WORKDIR/list.json"
"$BIN" tools list --cwd "$WS" --json > "$LIST_JSON" 2>/dev/null
check "list: config timeout is the default, from config" "jq -e '.defaults.timeout == \"1s\" and .defaults.timeoutSource == \"config\"' \"$LIST_JSON\" >/dev/null"
check "list: config output cap is the default, from config" "jq -e '.defaults.maxOutputBytes == 1024 and .defaults.maxOutputBytesSource == \"config\"' \"$LIST_JSON\" >/dev/null"
check "list: untouched maxTimeout stays builtin" "jq -e '.defaults.maxTimeoutSource == \"builtin\" and .defaults.maxTimeout == \"10m0s\"' \"$LIST_JSON\" >/dev/null"
check "list: tool without fields inherits both knobs" "jq -e '.tools[] | select(.name==\"spew\") | .timeout == \"1s\" and .maxOutputBytes == 1024' \"$LIST_JSON\" >/dev/null"
check "list: manifest timeout wins over config" "jq -e '.tools[] | select(.name==\"nap_long\") | .timeout == \"10s\"' \"$LIST_JSON\" >/dev/null"
check "list: no warnings for valid knobs" "[ \"\$(jq -r '.warnings | length' \"$LIST_JSON\")\" = 0 ]"

# --- serve: the same limits over MCP ------------------------------------
serve_calls() { # <out file> ; env of the caller applies to the server
  python3 - "$BIN" "$WS" "$1" <<'PY'
import json, subprocess, sys
binary, ws, out = sys.argv[1:4]
p = subprocess.Popen([binary, "tools", "serve", "--cwd", ws], stdin=subprocess.PIPE,
                     stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
def send(msg):
    p.stdin.write(json.dumps(msg) + "\n"); p.stdin.flush()
def recv():
    line = p.stdout.readline()
    return json.loads(line) if line else None
send({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}})
recv()
send({"jsonrpc":"2.0","method":"notifications/initialized","params":{}})
results = {}
for i, name in enumerate(["spew", "nap", "nap_long"], start=2):
    send({"jsonrpc":"2.0","id":i,"method":"tools/call","params":{"name":name,"arguments":{"args":[]}}})
    results[name] = recv()
p.stdin.close(); p.wait(timeout=10)
json.dump(results, open(out, "w"))
PY
}
SERVE_OUT="$WORKDIR/serve.json"
serve_calls "$SERVE_OUT"
check "serve: config output cap applies over MCP" "jq -e '.spew.result | (.isError != true) and (.content[0].text | contains(\"<spew output truncated:\") and contains(\"Full output saved to:\") and (length < 4096))' \"$SERVE_OUT\" >/dev/null"
check "serve: config timeout applies over MCP" "jq -e '.nap.result | .isError == true and (.content[0].text | contains(\"timed out after 1s\"))' \"$SERVE_OUT\" >/dev/null"
check "serve: manifest timeout wins over MCP" "jq -e '.nap_long.result | (.isError != true) and (.content[0].text | contains(\"exit status 0\"))' \"$SERVE_OUT\" >/dev/null"

# --- native: the same limits through agent.NewToolSet → tool.Run ----------
driver_check() { # <scenario> <expected check names...>
  local scenario="$1"; shift
  local out
  out="$(cd "$WS" && "$DRIVER" -check "$scenario" 2>"$WORKDIR/$scenario.stderr")" || true
  if [ -z "$out" ]; then
    log_fail "native/$scenario: driver produced no output ($(tail -2 "$WORKDIR/$scenario.stderr" | tr '\n' ' '))"
    return
  fi
  local errs
  errs="$(echo "$out" | jq -r '.errors // [] | join("; ")')"
  for c in "$@"; do
    if echo "$out" | jq -e --arg c "$c" '.checks | index($c)' >/dev/null; then
      log_pass "native/$scenario/$c"
    else
      log_fail "native/$scenario/$c ($errs)"
    fi
  done
}
driver_check configured_cap config_cap_round_trips_to_native_tool native_reply_bounded_by_config_cap spill_file_holds_full_output metadata_names_spill_file
driver_check configured_timeout config_timeout_round_trips_to_native_tool native_call_killed_at_config_timeout metadata_reports_timeout
driver_check manifest_wins manifest_timeout_beats_config_default

# --- environment beats the config block, on both surfaces -----------------
ENV_LIST="$WORKDIR/env-list.json"
OPENCODE_CLI_TOOLS_TIMEOUT=2s OPENCODE_CLI_TOOLS_MAX_OUTPUT_BYTES=-1 "$BIN" tools list --cwd "$WS" --json > "$ENV_LIST" 2>/dev/null
check "env: list reports env as the source" "jq -e '.defaults.timeout == \"2s\" and .defaults.timeoutSource == \"env\" and .defaults.maxOutputBytes == -1 and .defaults.maxOutputBytesSource == \"env\"' \"$ENV_LIST\" >/dev/null"
ENV_SERVE="$WORKDIR/env-serve.json"
OPENCODE_CLI_TOOLS_TIMEOUT=2s OPENCODE_CLI_TOOLS_MAX_OUTPUT_BYTES=-1 serve_calls "$ENV_SERVE"
check "env: unbounded cap over MCP" "jq -e '.spew.result | (.isError != true) and (.content[0].text | (contains(\"output truncated\") | not) and (length > 10000))' \"$ENV_SERVE\" >/dev/null"
check "env: env timeout over MCP" "jq -e '.nap.result | .isError == true and (.content[0].text | contains(\"timed out after 2s\"))' \"$ENV_SERVE\" >/dev/null"
OPENCODE_CLI_TOOLS_MAX_OUTPUT_BYTES=-1 driver_check env_override env_unbounded_overrides_config_cap

# --- an invalid knob is a warning, not a broken workspace ----------------
BAD_LIST="$WORKDIR/bad-list.json"
OPENCODE_CLI_TOOLS_TIMEOUT=soon "$BIN" tools list --cwd "$WS" --json > "$BAD_LIST" 2>/dev/null
check "invalid env: warning names the variable, config value kept" "jq -e '(.warnings | length) == 1 and (.warnings[0] | contains(\"OPENCODE_CLI_TOOLS_TIMEOUT\")) and .defaults.timeout == \"1s\" and .defaults.timeoutSource == \"config\"' \"$BAD_LIST\" >/dev/null"
check "invalid env: every manifest still loads" "[ \"\$(jq -r '[.tools[].name] | join(\",\")' \"$BAD_LIST\")\" = 'nap,nap_long,spew' ]"
OPENCODE_CLI_TOOLS_TIMEOUT=soon "$BIN" tools list --cwd "$WS" --strict >/dev/null 2>&1
check "invalid env: --strict exits 1" "[ $? -ne 0 ]"

# --- the published schema carries the three keys --------------------------
check "schema: cliTools limit keys published" "jq -e '.properties.cliTools.properties | (.timeout and .maxTimeout and (.maxOutputBytes.type == \"integer\"))' \"$ROOT/opencode-schema.json\" >/dev/null"

printf "Results: %d passed, %d failed\n" "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
