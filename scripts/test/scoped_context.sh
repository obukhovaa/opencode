#!/usr/bin/env bash
# scoped_context.sh — hermetic e2e for scoped context resolution and
# progressive context disclosure (openspec change: scoped-context-files).
#
# Drives cmd/context-e2e — a Go driver that runs the REAL pipeline
# (.opencode.json → viper → config → registry → prompt assembly →
# agent.NewToolSet → read tool) inside mktemp sandboxes and emits a JSON
# verdict per scenario:
#
#   1. backcompat  — no context config ⇒ the context section is
#                    byte-identical to the pre-feature construction and no
#                    manifest appears; two driver PROCESSES produce the
#                    same prompt sha256 (cross-process determinism).
#   2. manifest    — nested services/*/AGENTS.md are listed (with labels)
#                    in a byte-stable manifest while their bodies stay out
#                    of the prompt.
#   3. disclosure  — the first read into services/auth/ injects the nested
#                    body via <system-reminder> in the tool result; a
#                    second read does NOT re-inject; the system prompt is
#                    unchanged by activation.
#   4. override    — agents.coder.context {paths: [AGENTS.runtime.md],
#                    mode: replace} in .opencode.json excludes the root
#                    AGENTS.md content from the prompt.
#
# Every driver invocation overrides HOME + XDG_CONFIG_HOME to the sandbox
# so the developer's ~/.opencode.json can never leak into the prompts
# (same convention as hooks.sh).
#
# Usage: ./scripts/test/scoped_context.sh

set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
PASS=0; FAIL=0

log_pass() { PASS=$((PASS + 1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL + 1)); printf "${RED}FAIL${NC}  %s  (%s)\n" "$1" "${2:-}"; }

for cmd in jq; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "Required tool not found: $cmd" >&2
        exit 1
    fi
done

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="$(mktemp -d)"
DRIVER="$WORK/context-e2e"
trap 'rm -rf "$WORK"' EXIT

echo "── scoped_context.sh ──"
echo "Building cmd/context-e2e …"
(cd "$ROOT" && go build -o "$DRIVER" ./cmd/context-e2e) || { echo "Build failed"; exit 1; }

# run_driver <sandbox> <check> — driver JSON lands on stdout; logs on stderr.
run_driver() {
    local sandbox="$1" check="$2"
    (cd "$sandbox" && env HOME="$sandbox" XDG_CONFIG_HOME="$sandbox/xdg" \
        "$DRIVER" -check "$check" 2>"$sandbox/stderr.log") || true
}

# assert_checks <name> <json> <check...> — every named check must be in .checks.
assert_checks() {
    local name="$1" json="$2"; shift 2
    if [ -z "$json" ]; then
        log_fail "$name" "driver produced no output"
        return
    fi
    local c
    for c in "$@"; do
        if echo "$json" | jq -e --arg c "$c" '.checks | index($c)' >/dev/null; then
            log_pass "$name: $c"
        else
            log_fail "$name: $c" "$(echo "$json" | jq -r '.errors // [] | join("; ")')"
        fi
    done
}

# ── 1. backcompat ────────────────────────────────────────────────────
SB="$WORK/backcompat"
mkdir -p "$SB"
cat > "$SB/AGENTS.md" << 'EOF'
# Sandbox instructions

Follow the sandbox rules exactly.
EOF

OUT="$(run_driver "$SB" backcompat)"
assert_checks "backcompat" "$OUT" \
    prompt_stable_across_turns \
    context_block_matches_pre_feature_bytes \
    no_manifest_without_nested_files

# Cross-process determinism: a second driver process (fresh memoization
# caches) must assemble the byte-identical prompt.
OUT2="$(run_driver "$SB" backcompat)"
SHA1="$(echo "$OUT" | jq -r '.prompt_sha256 // empty')"
SHA2="$(echo "$OUT2" | jq -r '.prompt_sha256 // empty')"
if [ -n "$SHA1" ] && [ "$SHA1" = "$SHA2" ]; then
    log_pass "backcompat: prompt_identical_across_processes"
else
    log_fail "backcompat: prompt_identical_across_processes" "sha A=$SHA1 B=$SHA2"
fi

# ── 2. manifest ──────────────────────────────────────────────────────
SB="$WORK/manifest"
mkdir -p "$SB/services/auth" "$SB/services/billing"
printf '# Root rules\n' > "$SB/AGENTS.md"
cat > "$SB/services/auth/AGENTS.md" << 'EOF'
# Auth service rules

NESTED-AUTH-BODY: never log tokens.
EOF
cat > "$SB/services/billing/AGENTS.md" << 'EOF'
---
description: Billing invariants for the e2e fixture
---

NESTED-BILLING-BODY: amounts are integer cents.
EOF

OUT="$(run_driver "$SB" manifest)"
assert_checks "manifest" "$OUT" \
    manifest_present \
    manifest_lists_auth_with_heading_label \
    manifest_lists_billing_with_frontmatter_label \
    manifest_byte_stable \
    root_context_still_inline \
    nested_bodies_not_inlined

# ── 3. disclosure ────────────────────────────────────────────────────
SB="$WORK/disclosure"
mkdir -p "$SB/services/auth"
printf '# Root rules\n' > "$SB/AGENTS.md"
cat > "$SB/services/auth/AGENTS.md" << 'EOF'
# Auth service rules

NESTED-AUTH-BODY: never log tokens.
EOF
printf 'package auth\n\nfunc Handle() {}\n' > "$SB/services/auth/handler.go"
printf 'package auth\n\nfunc Util() {}\n' > "$SB/services/auth/util.go"

OUT="$(run_driver "$SB" disclosure)"
assert_checks "disclosure" "$OUT" \
    first_read_injects_nested_body \
    second_touch_does_not_reinject \
    system_prompt_unchanged_by_activation

# ── 4. override ──────────────────────────────────────────────────────
SB="$WORK/override"
mkdir -p "$SB"
printf '# Root rules\n\nROOT-MARKER: root instructions.\n' > "$SB/AGENTS.md"
printf '# Runtime rules\n\nRUNTIME-MARKER: runtime instructions.\n' > "$SB/AGENTS.runtime.md"
cat > "$SB/.opencode.json" << 'EOF'
{
  "agents": {
    "coder": {
      "context": {
        "paths": ["AGENTS.runtime.md"],
        "mode": "replace"
      }
    }
  }
}
EOF

OUT="$(run_driver "$SB" override)"
assert_checks "override" "$OUT" \
    override_includes_runtime_context \
    override_excludes_root_context

printf "Results: %d passed, %d failed\n" "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
