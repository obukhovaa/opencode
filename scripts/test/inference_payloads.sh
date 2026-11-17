#!/usr/bin/env bash
# Local HTTP providers, SQLite restart and real shell output; no model service.
set -euo pipefail
export LC_ALL=C
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SANDBOX="$(mktemp -d)"
trap 'rm -rf "$SANDBOX"' EXIT
cd "$ROOT"
go test -c -o "$SANDBOX/provider.test" ./internal/llm/provider
go test -c -o "$SANDBOX/tools.test" ./internal/llm/tools
mkdir -p "$SANDBOX/home" "$SANDBOX/xdg"
env HOME="$SANDBOX/home" XDG_CONFIG_HOME="$SANDBOX/xdg" "$SANDBOX/provider.test" \
  -test.run '^TestScalarReasoning|^TestNativeClaudeReasoningPersistedWireUnchanged$' -test.v -test.timeout 60s
env HOME="$SANDBOX/home" XDG_CONFIG_HOME="$SANDBOX/xdg" "$SANDBOX/tools.test" \
  -test.run '^TestBashOutput|^TestBashRunBoundedOutputMetadata$' -test.v -test.timeout 60s
