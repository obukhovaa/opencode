#!/usr/bin/env bash
# E2E: workspace-defined CLI tools (docs/cli-tools.md).
#
# Builds the binary, writes a sandbox workspace with an argv-mode manifest
# (echo-backed), a structured manifest, a broken manifest and a shadowed one,
# then asserts `opencode tools list --json`, `--strict` failure, and a
# `opencode tools serve` JSON-RPC round trip including a policy rejection.
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

WS="$WORKDIR/ws"
mkdir -p "$WS/.agents/tools" "$WS/.opencode/tools" "$WS/.git" "$WS/sub"
export HOME="$WORKDIR/home" # keep the real ~/.agents/tools out of the run
mkdir -p "$HOME"
cat > "$WS/.opencode.json" <<JSON
{"agents": {"explorer": {"tools": {"say": true}, "deferredTools": {"say": true}}}}
JSON
cat > "$WS/.opencode/tools/say.yaml" <<YAML
name: say
description: Echo wrapper used by the e2e test.
command: /bin/echo
prefixArgs: ["-n"]
args:
  deny: ["-x", "--danger*"]
permission:
  "*": allow
  "secret *": deny
YAML
cat > "$WS/.agents/tools/greet.yaml" <<YAML
name: greet
description: Structured echo.
command: /bin/echo
mode: structured
parameters:
  who: {type: string, enum: [world, team]}
  loud: {type: boolean}
required: [who]
argv: ["hello", "{who}", ["--loud", "{loud}"]]
YAML
cat > "$WS/.agents/tools/broken.yaml" <<YAML
name: broken
description: has a typo in a security field
command: /bin/echo
args:
  alow: ["*"]
YAML
cat > "$WS/.agents/tools/say.yaml" <<YAML
name: say
description: shadowed copy — .opencode/tools wins over .agents/tools
command: /bin/echo
YAML

# --- list ---------------------------------------------------------------
LIST_JSON="$WORKDIR/list.json"
"$BIN" tools list --cwd "$WS" --json > "$LIST_JSON" 2>"$WORKDIR/list.err"
check "tools list --json exits 0" "[ $? -eq 0 ]"
check "lists greet and say only" "[ \"\$(jq -r '[.tools[].name] | join(\",\")' \"$LIST_JSON\")\" = 'greet,say' ]"
check ".opencode/tools wins over .agents/tools" "jq -e '.tools[] | select(.name==\"say\") | .file | endswith(\".opencode/tools/say.yaml\")' \"$LIST_JSON\" >/dev/null"
check "broken manifest reported invalid" "[ \"\$(jq -r '.invalid' \"$LIST_JSON\")\" = 1 ] && jq -e '.diagnostics[] | select(.shadowed|not) | .reason | contains(\"alow\")' \"$LIST_JSON\" >/dev/null"
check "shadowed manifest reported" "[ \"\$(jq -r '.shadowed' \"$LIST_JSON\")\" = 1 ]"
check "binary resolved" "jq -e '.tools[] | select(.name==\"greet\") | .found' \"$LIST_JSON\" >/dev/null"
check "structured parameters listed" "[ \"\$(jq -r '.tools[] | select(.name==\"greet\") | .parameters | join(\",\")' \"$LIST_JSON\")\" = 'loud,who' ]"

"$BIN" tools list --cwd "$WS" --strict >/dev/null 2>&1
check "--strict exits 1 on an invalid manifest" "[ $? -ne 0 ]"

AGENT_JSON="$WORKDIR/agent.json"
"$BIN" tools list --cwd "$WS" --json --agent explorer > "$AGENT_JSON" 2>/dev/null
check "--agent shows explicit grant + deferral" "jq -e '.tools[] | select(.name==\"say\") | .agent.granted and .agent.deferred' \"$AGENT_JSON\" >/dev/null"
check "--agent shows greet not granted" "jq -e '.tools[] | select(.name==\"greet\") | .agent.granted | not' \"$AGENT_JSON\" >/dev/null"

# --- serve (JSON-RPC over stdio) ----------------------------------------
SERVE_OUT="$WORKDIR/serve.out"
python3 - "$BIN" "$WS" "$SERVE_OUT" <<'PY'
import json, subprocess, sys
binary, ws, out = sys.argv[1:4]
p = subprocess.Popen([binary, "tools", "serve", "--cwd", ws], stdin=subprocess.PIPE,
                     stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
def send(msg):
    p.stdin.write(json.dumps(msg) + "\n"); p.stdin.flush()
def recv():
    line = p.stdout.readline()
    return json.loads(line) if line else None
results = {}
send({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}})
results["init"] = recv()
send({"jsonrpc":"2.0","method":"notifications/initialized","params":{}})
send({"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}})
results["list"] = recv()
send({"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"say","arguments":{"args":["hi","there"]}}})
results["call"] = recv()
send({"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"say","arguments":{"args":["ok","-x"]}}})
results["denied"] = recv()
send({"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"say","arguments":{"args":["secret","x"]}}})
results["permdenied"] = recv()
send({"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"greet","arguments":{"who":"team","loud":True}}})
results["structured"] = recv()
send({"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"greet","arguments":{"who":"nobody"}}})
results["enum"] = recv()
p.stdin.close(); p.wait(timeout=10)
json.dump(results, open(out, "w"))
PY
check "serve: initialize answered" "jq -e '.init.result.serverInfo.name == \"opencode-cli-tools\"' \"$SERVE_OUT\" >/dev/null"
check "serve: tools/list has greet and say" "[ \"\$(jq -r '[.list.result.tools[].name] | sort | join(\",\")' \"$SERVE_OUT\")\" = 'greet,say' ]"
check "serve: call runs the binary argv-only" "jq -e '.call.result.content[0].text | startswith(\"hi there\\nexit status 0\")' \"$SERVE_OUT\" >/dev/null"
check "serve: deny pattern enforced" "jq -e '.denied.result.isError == true and (.denied.result.content[0].text | contains(\"deny pattern\"))' \"$SERVE_OUT\" >/dev/null"
check "serve: manifest default deny enforced" "jq -e '.permdenied.result.isError == true and (.permdenied.result.content[0].text | contains(\"default permission\"))' \"$SERVE_OUT\" >/dev/null"
check "serve: structured template rendered" "jq -e '.structured.result.content[0].text | startswith(\"hello team --loud\\n\")' \"$SERVE_OUT\" >/dev/null"
check "serve: enum violation rejected" "jq -e '.enum.result.isError == true and (.enum.result.content[0].text | contains(\"must be one of\"))' \"$SERVE_OUT\" >/dev/null"

printf "Results: %d passed, %d failed\n" "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
