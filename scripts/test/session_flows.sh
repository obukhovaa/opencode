#!/usr/bin/env bash
# Durable session-flow integration: real SQLite, flow engine, message/question
# stores, bridge routing and HTTP admission. Model responses/adapters are local
# fixtures. MySQL and the coordinated c2-agent binary test run as separate gates.
set -euo pipefail
export LC_ALL=C
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SANDBOX="$(mktemp -d)"
trap 'rm -rf "$SANDBOX"' EXIT
cd "$ROOT"
for package in flowrun app api; do
    go test -c -o "$SANDBOX/$package.test" "./internal/$package"
done
go test -c -o "$SANDBOX/bridge.test" ./internal/bridge/service
mkdir -p "$SANDBOX/home" "$SANDBOX/xdg"
env HOME="$SANDBOX/home" XDG_CONFIG_HOME="$SANDBOX/xdg" "$SANDBOX/flowrun.test" -test.run 'TestStoreSQLite|TestSimultaneousCreate|TestRunIdentifiers' -test.v -test.timeout 60s
env HOME="$SANDBOX/home" XDG_CONFIG_HOME="$SANDBOX/xdg" "$SANDBOX/app.test" -test.run '^TestStored|TestNewAppSharesDerivedProject' -test.v -test.timeout 90s
env HOME="$SANDBOX/home" XDG_CONFIG_HOME="$SANDBOX/xdg" "$SANDBOX/api.test" -test.run '^TestSessionFlow' -test.v -test.timeout 30s
env HOME="$SANDBOX/home" XDG_CONFIG_HOME="$SANDBOX/xdg" "$SANDBOX/bridge.test" -test.run '^TestSessionFlow' -test.v -test.timeout 60s
