#!/usr/bin/env bash
# Real SQLite + flow engine + scripted agents; no provider credentials or network.
# Build before isolating HOME so Go retains its normal module/build caches.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SANDBOX="$(mktemp -d)"
trap 'rm -rf "$SANDBOX"' EXIT
cd "$ROOT"
go test -c -o "$SANDBOX/app.test" ./internal/app
go test -c -o "$SANDBOX/page.test" ./internal/tui/page
go test -c -o "$SANDBOX/tui.test" ./internal/tui
mkdir -p "$SANDBOX/home" "$SANDBOX/xdg"
env HOME="$SANDBOX/home" XDG_CONFIG_HOME="$SANDBOX/xdg" "$SANDBOX/app.test" -test.run '^TestDynamicFlowLifecycleIntegration$' -test.v -test.timeout 45s
env HOME="$SANDBOX/home" XDG_CONFIG_HOME="$SANDBOX/xdg" "$SANDBOX/page.test" -test.run 'Test(Activity|FlowGoal|FlowPage)' -test.v -test.timeout 30s
env HOME="$SANDBOX/home" XDG_CONFIG_HOME="$SANDBOX/xdg" "$SANDBOX/tui.test" -test.run 'Test.*(Question|Permission)' -test.v -test.timeout 30s
