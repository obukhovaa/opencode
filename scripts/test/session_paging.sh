#!/usr/bin/env bash
# Real databases/transports and connector rendering, isolated from user config.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SANDBOX="$(mktemp -d)"
trap 'rm -rf "$SANDBOX"' EXIT
cd "$ROOT"
for package in app session api acp tui bridge/service bridge/slack bridge/telegram bridge/mattermost; do
  name="${package//\//-}"
  go test -c -o "$SANDBOX/$name.test" "./internal/$package"
done
mkdir -p "$SANDBOX/home" "$SANDBOX/xdg"
for binary in "$SANDBOX/"*.test; do
  env HOME="$SANDBOX/home" XDG_CONFIG_HOME="$SANDBOX/xdg" "$binary" \
    -test.run 'Test(StoredFlowSessionPageMetadata|SessionPag|SessionCleanupWalks|SessionHTTPPagination|ACPConsumes|SessionPicker|BridgeSessionPaging|SessionTableRenders)' \
    -test.v -test.timeout 60s
done
