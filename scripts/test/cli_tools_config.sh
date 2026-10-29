#!/usr/bin/env bash
# E2E: tools list/serve need no runtime credentials, but retain CLI policy.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT
BIN="${1:-$WORKDIR/opencode}"
if [ ! -x "$BIN" ]; then
  (cd "$ROOT" && go build -o "$BIN" .)
fi
python3 - "$BIN" "$WORKDIR" <<'PY'
import json, os, pathlib, subprocess, sys
binary, root = sys.argv[1], pathlib.Path(sys.argv[2])
home, ws, scratch = root/'home', root/'ws', root/'scratch'
profile = home/'.config/opencode-test'
tools = ws/'.agents/tools'
for directory in (profile, tools, scratch):
    directory.mkdir(parents=True)
# These deliberately cannot initialize the agent runtime.
(profile/'.opencode.json').write_text(json.dumps({
    'providers': {'local': {'apiKey': '${UNSET_LLM_CREDENTIAL}'}},
    'agents': {'coder': {'model': 'invalid.invalid'}},
    'mcpServers': {'unused': {'url': '${UNSET_MCP_ENDPOINT}'}},
    'telemetry': {'enabled': True, 'secretKey': '${UNSET_TELEMETRY_KEY}'},
    'sessionProvider': {'type': 'mysql'},
    'cliTools': {'timeout': '17s', 'maxOutputBytes': 128},
    'toolEnv': {'deny': ['CLI_SCOPE_PRIVATE']},
}))
(ws/'.opencode.json').write_text(json.dumps({
    'router': {'token': '${UNSET_ROUTER_TOKEN}'},
    'cliTools': {'maxTimeout': '19s'},
}))
(tools/'say.yaml').write_text('''name: say
description: Scoped loader echo fixture.
command: /bin/echo
args:
  deny: ["--forbidden*"]
''')
(tools/'private-env.yaml').write_text('''name: private-env
description: Check that the configured deny list reaches the child.
command: /usr/bin/printenv
prefixArgs: [CLI_SCOPE_PRIVATE]
''')
# Model/service credentials are absent even if the invoking shell has them.
env = {'HOME': str(home), 'PATH': os.defpath, 'TMPDIR': str(scratch),
       'OPENCODE_PROFILE': 'test', 'CLI_SCOPE_PRIVATE': 'fixture-secret'}
listing = subprocess.run([binary, 'tools', 'list', '--cwd', str(ws), '--json', '--strict'],
                         env=env, text=True, capture_output=True, timeout=20)
assert listing.returncode == 0, listing.stderr
report = json.loads(listing.stdout)
assert report['defaults']['timeout'] == '17s', report
assert report['defaults']['maxTimeout'] == '19s', report
assert report['defaults']['maxOutputBytes'] == 128, report
print('PASS: credential-free list preserves profile/project CLI limits')
messages = [
    {'id': 1, 'method': 'initialize', 'params': {'protocolVersion': '2024-11-05', 'capabilities': {}, 'clientInfo': {'name': 'e2e', 'version': '1'}}},
    {'method': 'notifications/initialized'},
    {'id': 2, 'method': 'tools/list', 'params': {}},
    {'id': 3, 'method': 'tools/call', 'params': {'name': 'say', 'arguments': {'args': ['hello']}}},
    {'id': 4, 'method': 'tools/call', 'params': {'name': 'say', 'arguments': {'args': ['--forbidden']}}},
    {'id': 5, 'method': 'tools/call', 'params': {'name': 'private-env', 'arguments': {'args': []}}},
    {'id': 6, 'method': 'tools/call', 'params': {'name': 'say', 'arguments': {'args': ['x'*2048]}}},
]
wire = ''.join(json.dumps(dict(jsonrpc='2.0', **m))+'\n' for m in messages)
served = subprocess.run([binary, 'tools', 'serve', '--cwd', str(ws)], input=wire,
                        env=env, text=True, capture_output=True, timeout=20)
assert served.returncode == 0, served.stderr
responses = {r['id']: r for r in map(json.loads, served.stdout.splitlines())}
assert responses[1]['result']['serverInfo']['name'] == 'opencode-cli-tools', responses
assert {t['name'] for t in responses[2]['result']['tools']} == {'say', 'private-env'}
assert responses[3]['result']['content'][0]['text'].startswith('hello\nexit status 0')
assert responses[4]['result']['isError'] is True
assert responses[5]['result']['isError'] is True
assert 'fixture-secret' not in served.stdout
assert 'output truncated' in responses[6]['result']['content'][0]['text']
print('PASS: credential-free MCP initialize, list and call')
print('PASS: argument policy, configured environment deny list and output cap')
# An agent audit still needs the full loader; do not silently audit defaults.
audit = subprocess.run([binary, 'tools', 'list', '--cwd', str(ws), '--agent', 'coder'],
                       env=env, text=True, capture_output=True, timeout=20)
assert audit.returncode != 0 and 'UNSET_' in audit.stderr, audit.stderr
print('PASS: --agent still loads runtime configuration')
# Removing missing refs still must not trigger model/database validation.
(profile/'.opencode.json').write_text(json.dumps({
    'agents': {'coder': {'model': 'invalid.invalid'}},
    'sessionProvider': {'type': 'mysql'},
}))
plain = subprocess.run([binary, 'tools', 'list', '--cwd', str(ws), '--json'],
                       env=env, text=True, capture_output=True, timeout=20)
assert plain.returncode == 0, plain.stderr
print('PASS: unrelated runtime validation is skipped')
# Used settings remain strict; never silently disable environment filtering.
(ws/'.opencode.json').write_text('{"toolEnv":{"deny":["${UNSET_DENY_NAME}"]}}')
invalid = subprocess.run([binary, 'tools', 'serve', '--cwd', str(ws)], input='',
                         env=env, text=True, capture_output=True, timeout=20)
assert invalid.returncode != 0 and 'UNSET_DENY_NAME' in invalid.stderr
assert not invalid.stdout
print('PASS: required CLI configuration references still fail closed')
PY
