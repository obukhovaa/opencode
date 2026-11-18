#!/usr/bin/env bash
# Real binary, local scripted Anthropic API and MCP transport; no live model.
# Usage: mcp_composition.sh [binary]. Set MCP_COMPOSITION_ARTIFACT_DIR to retain captures.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SANDBOX="$(mktemp -d)"
cleanup() {
  status=$?
  if [[ -n "${MCP_COMPOSITION_ARTIFACT_DIR:-}" && -d "$SANDBOX/work" ]]; then
    mkdir -p "$MCP_COMPOSITION_ARTIFACT_DIR"
    cp -R "$SANDBOX/work/." "$MCP_COMPOSITION_ARTIFACT_DIR/"
  fi
  if [[ "$status" -eq 0 ]]; then
    rm -rf "$SANDBOX"
  else
    echo "Acceptance failure artifacts: $SANDBOX" >&2
  fi
}
trap cleanup EXIT
if [[ -n "${1:-}" ]]; then
  BIN="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
else
  BIN="$SANDBOX/opencode"
  (cd "$ROOT" && go build -o "$BIN" .)
fi
[[ -x "$BIN" ]] || { echo "Binary is not executable: $BIN" >&2; exit 1; }
python3 "$ROOT/scripts/test/mcp_composition.py" "$BIN" "$SANDBOX"
