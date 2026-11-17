#!/usr/bin/env bash
# Flow journal -> fenced durable card -> native API payload, with local HTTP
# fixtures only. No real chat messages or provider requests are sent.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SANDBOX="$(mktemp -d)"
trap 'rm -rf "$SANDBOX"' EXIT
cd "$ROOT"
for package in flowrun bridge bridge/service bridge/slack bridge/mattermost bridge/telegram; do
  name="${package//\//-}"
  go test -c -o "$SANDBOX/$name.test" "./internal/$package"
done
mkdir -p "$SANDBOX/home" "$SANDBOX/xdg"
for testbin in "$SANDBOX/"*.test; do
  env HOME="$SANDBOX/home" XDG_CONFIG_HOME="$SANDBOX/xdg" ANTHROPIC_API_KEY=test OPENAI_API_KEY=test \
    "$testbin" -test.run 'TestFlowCard|TestFlowProgress|TestFlow.*(Slack|Mattermost|Telegram)|TestSessionFlowAdapterError' -test.v -test.timeout 90s
done
