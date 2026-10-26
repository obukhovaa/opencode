#!/usr/bin/env bash
# Hermetic process e2e: global + project JSON -> Viper -> native Kimi HTTP
# request -> typed config writeback, using only an in-process HTTP fixture.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
(cd "$ROOT" && go build -o "$WORK/driver" ./cmd/config-env-e2e)
mkdir -p "$WORK/home" "$WORK/project"
cat > "$WORK/home/.opencode.json" <<'JSON'
{
 "providers":{"kimi":{"apiKey":"${E2E_KEY}","baseURL":"${GLOBAL_BASE:-https://global.test/anthropic}"}},
 "maxTurns":"${E2E_TURNS:-12}",
 "autoCompact":"${E2E_COMPACT:-true}",
 "future":{"big":9007199254740993,"token":"${E2E_KEY}"}
}
JSON
cat > "$WORK/project/.opencode.json" <<'JSON'
{
 "providers":{"kimi":{"baseURL":"${E2E_KIMI_BASE}"}},
 "agents":{"coder":{"model":"${E2E_MODEL}","maxTokens":"${E2E_TOKENS:-2048}"}},
 "autoCompact":"${PROJECT_COMPACT:-false}",
 "mcpServers":{"example":{"type":"http","url":"${MCP_URL:-https://mcp.test}","headers":{"X-Secret":"${E2E_DATA}"},"env":["TOKEN=${E2E_DATA}"]}},
 "contextPaths":["AGENTS.${agent}.${flow.step}.md"],
 "shell":{"path":"/bin/bash","args":["$${E2E_LITERAL}"]}
}
JSON
cp "$WORK/home/.opencode.json" "$WORK/original.json"
for model in kimi.kimi-k3.1-pro kimi.kimi-k3.1-pro-highspeed; do
 cp "$WORK/original.json" "$WORK/home/.opencode.json"
 (cd "$WORK/project" && env -i PATH="$PATH" HOME="$WORK/home" XDG_CONFIG_HOME="$WORK/xdg" \
  E2E_KEY=fake-key E2E_DATA=$'fake-data-"-\\-\n-${NO_RECURSION}' E2E_MODEL="$model" "$WORK/driver")
 jq -e '.providers.kimi.apiKey == "${E2E_KEY}" and .providers.kimi.baseURL == "${GLOBAL_BASE:-https://global.test/anthropic}" and .future.token == "${E2E_KEY}" and .autoCompact == false and .maxTurns == 0 and .tui.theme == "e2e-theme"' "$WORK/home/.opencode.json" >/dev/null
 # jq uses double precision, so check the retained integer as text too.
 grep -q '9007199254740993' "$WORK/home/.opencode.json"
done
# An unresolved project reference must stop loading, not use a global secret.
printf '%s\n' '{"maxTurns":"${E2E_REQUIRED_MISSING}"}' > "$WORK/project/.opencode.json"
if (cd "$WORK/project" && env -i PATH="$PATH" HOME="$WORK/home" XDG_CONFIG_HOME="$WORK/xdg" E2E_KEY=fake-key "$WORK/driver") >"$WORK/output" 2>"$WORK/error"; then
 echo "FAIL: missing reference was accepted" >&2; exit 1
fi
grep -q 'E2E_REQUIRED_MISSING' "$WORK/error"
if grep -q 'fake-key' "$WORK/error"; then echo 'FAIL: error exposed a secret' >&2; exit 1; fi
echo 'PASS missing variable fails without exposing values'
