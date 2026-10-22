#!/usr/bin/env bash
# memory.sh — hermetic e2e for project memory (openspec change:
# project-memory-tool).
#
# Drives cmd/memory-e2e — a Go driver that runs the REAL pipeline
# (.opencode.json → viper → config → AGENTS_MEMORY_HOME → git origin →
# project identity → registry → prompt assembly → index snapshot) inside
# mktemp sandboxes and emits a JSON report per run:
#
#   1. config    — memory.enabled + memory.directory from .opencode.json:
#                  the project id comes from the sandbox's git origin, its
#                  key equals an independent sha256 (the mem-sync contract),
#                  the coder prompt carries the reminder and the snapshot
#                  loads that store's MEMORY.md.
#   2. env       — AGENTS_MEMORY_HOME set in the PROCESS environment wins
#                  over memory.directory, and the snapshot is the env
#                  store's index.
#   3. disabled  — no memory config ⇒ no reminder and no snapshot.
#
# Every driver invocation overrides HOME + XDG_CONFIG_HOME to the sandbox
# so the developer's ~/.opencode.json and ~/.agents/memory never leak in.
#
# Usage: ./scripts/test/memory.sh

set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
PASS=0; FAIL=0

log_pass() { PASS=$((PASS + 1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL + 1)); printf "${RED}FAIL${NC}  %s  (%s)\n" "$1" "${2:-}"; }

for cmd in jq git; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "Required tool not found: $cmd" >&2
        exit 1
    fi
done

sha256() {
    if command -v sha256sum &>/dev/null; then sha256sum | cut -d' ' -f1; else shasum -a 256 | cut -d' ' -f1; fi
}

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="$(mktemp -d)"
DRIVER="$WORK/memory-e2e"
trap 'rm -rf "$WORK"' EXIT
# Resolve /var → /private/var on macOS so paths compare with what Go reports.
WORK="$(cd "$WORK" && pwd -P)"

echo "── memory.sh ──"
echo "Building cmd/memory-e2e …"
(cd "$ROOT" && go build -o "$DRIVER" ./cmd/memory-e2e) || { echo "Build failed"; exit 1; }

# run_driver <sandbox> [env...] — driver JSON lands on stdout; logs on stderr.
run_driver() {
    local sandbox="$1"; shift
    (cd "$sandbox" && env -u AGENTS_MEMORY_HOME HOME="$sandbox" XDG_CONFIG_HOME="$sandbox/xdg" "$@" \
        "$DRIVER" 2>"$sandbox/stderr.log") || true
}

# expect <name> <json> <jq filter> — the filter must evaluate to true.
expect() {
    local name="$1" json="$2" filter="$3"
    if [ -z "$json" ]; then
        log_fail "$name" "driver produced no output: $(tail -3 "$SB/stderr.log" 2>/dev/null)"
    elif echo "$json" | jq -e "$filter" >/dev/null; then
        log_pass "$name"
    else
        log_fail "$name" "$(echo "$json" | jq -c 'del(.snapshot)')"
    fi
}

# seed_index <store> <key> <marker> — a project's MEMORY.md as mem-sync lays it out.
seed_index() {
    local dir="$1/projects/$2/memory"
    mkdir -p "$dir"
    printf -- '- [Deploy](deploy.md) — %s; updatedAt: 2026-10-22T00:00:00Z\n' "$3" > "$dir/MEMORY.md"
}

SB="$WORK/repo"
mkdir -p "$SB"
git -C "$SB" init -q
git -C "$SB" remote add origin git@gitlab.example.com:Team/Repo.git
ID="git:gitlab.example.com/Team/Repo"
KEY="$(printf %s "$ID" | sha256)"
seed_index "$SB/store-config" "$KEY" "CONFIG-INDEX-MARKER"
seed_index "$SB/store-env" "$KEY" "ENV-INDEX-MARKER"

# ── 1. config ────────────────────────────────────────────────────────
cat > "$SB/.opencode.json" << 'EOF'
{"memory": {"enabled": true, "directory": "store-config"}}
EOF
OUT="$(run_driver "$SB")"
expect "config: root from memory.directory" "$OUT" ".root == \"$SB/store-config\""
expect "config: project id from git origin" "$OUT" ".project_id == \"$ID\""
expect "config: project key matches sha256 of the id" "$OUT" ".project_key == \"$KEY\""
expect "config: coder can read and write memory" "$OUT" '.can_read and .can_write'
expect "config: reminder names the project directory" "$OUT" '.reminder and .reminder_names_directory'
expect "config: system prompt stable across builds" "$OUT" '.prompt_stable'
expect "config: snapshot loads the store index" "$OUT" '.snapshot_has_marker and (.snapshot | contains("CONFIG-INDEX-MARKER"))'

# ── 2. env ───────────────────────────────────────────────────────────
OUT="$(run_driver "$SB" AGENTS_MEMORY_HOME="$SB/store-env")"
expect "env: AGENTS_MEMORY_HOME overrides memory.directory" "$OUT" ".root == \"$SB/store-env\""
expect "env: snapshot comes from the env store" "$OUT" '(.snapshot | contains("ENV-INDEX-MARKER")) and (.snapshot | contains("CONFIG-INDEX-MARKER") | not)'

# ── 3. disabled ──────────────────────────────────────────────────────
rm "$SB/.opencode.json"
OUT="$(run_driver "$SB" AGENTS_MEMORY_HOME="$SB/store-env")"
expect "disabled: no reminder and no snapshot" "$OUT" '(.reminder | not) and .snapshot == "" and .root == ""'

printf "Results: %d passed, %d failed\n" "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
