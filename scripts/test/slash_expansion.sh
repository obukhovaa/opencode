#!/usr/bin/env bash
# slash_expansion.sh — hermetic e2e for non-interactive slash-command expansion
# (openspec change: deferred-slash-command-expansion).
#
# Drives cmd/slashexpand-e2e, a Go driver that runs the real pipeline used by
# the `--prompt` paths inside an mktemp sandbox: config.Load → project skill
# discovery (.opencode/skills/**/SKILL.md) → custom command discovery
# (.opencode/commands/*.md) → slashcmd.Expand.
#
# Scenarios:
#   1. multi        — one prompt carrying two invocations plus prose expands
#                     both in place and keeps the prose between them.
#   2. args         — a discovered skill binds $ARGUMENTS; a discovered custom
#                     command binds its named placeholders positionally.
#   3. passthrough  — an unresolved /token and a fenced /review are untouched.
#   4. tui_only     — /compact is rejected with an interactive-only error.
#
# HOME + XDG_CONFIG_HOME are overridden to the sandbox so the developer's own
# ~/.opencode.json, skills, and commands can never leak into the expansion
# (same convention as hooks.sh / scoped_context.sh).
#
# Usage: ./scripts/test/slash_expansion.sh

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
DRIVER="$WORK/slashexpand-e2e"
SANDBOX="$WORK/sandbox"
trap 'rm -rf "$WORK"' EXIT

echo "── slash_expansion.sh ──"
echo "Building cmd/slashexpand-e2e …"
(cd "$ROOT" && go build -o "$DRIVER" ./cmd/slashexpand-e2e) || { echo "Build failed"; exit 1; }

# ---- sandbox ---------------------------------------------------------------
mkdir -p "$SANDBOX/xdg" "$SANDBOX/.opencode/skills/reviewer" "$SANDBOX/.opencode/commands"
echo '{}' >"$SANDBOX/.opencode.json"

cat >"$SANDBOX/.opencode/skills/reviewer/SKILL.md" <<'SKILL'
---
name: reviewer
description: Review a diff for correctness
---

Review the diff at $ARGUMENTS.
Report only real defects.
SKILL

cat >"$SANDBOX/.opencode/commands/scope.md" <<'CMD'
---
title: Scope
description: Look at a target inside a scope
argument-hint: "[target] [scope]"
---

Look at $TARGET inside $SCOPE.
CMD

# run_driver <prompt> — driver JSON on stdout, logs on stderr.
run_driver() {
    (cd "$SANDBOX" && env HOME="$SANDBOX" XDG_CONFIG_HOME="$SANDBOX/xdg" \
        "$DRIVER" -prompt "$1" 2>"$SANDBOX/stderr.log") || true
}

# ---- 1. multi --------------------------------------------------------------
OUT="$(run_driver '/skill:reviewer HEAD~1
mind the tests
/scope HEAD~3 src/')"

if [ "$(jq -r '.ok' <<<"$OUT")" != "true" ]; then
    log_fail "multi: expansion succeeded" "$(jq -r '.error' <<<"$OUT")"
else
    PROMPT="$(jq -r '.prompt' <<<"$OUT")"
    if grep -q '<skill_content name="reviewer">' <<<"$PROMPT"; then
        log_pass "multi: the discovered skill expanded into a skill_content block"
    else
        log_fail "multi: skill block present" "$PROMPT"
    fi
    if grep -q 'Look at HEAD~3 inside src/\.' <<<"$PROMPT"; then
        log_pass "multi: the discovered custom command expanded"
    else
        log_fail "multi: command expansion present" "$PROMPT"
    fi
    # Prose must stay between the two expansions, not be hoisted or dropped.
    if [ "$(grep -n 'mind the tests' <<<"$PROMPT" | cut -d: -f1)" \
         -gt "$(grep -n '<skill_content name="reviewer">' <<<"$PROMPT" | cut -d: -f1)" ] &&
       [ "$(grep -n 'mind the tests' <<<"$PROMPT" | cut -d: -f1)" \
         -lt "$(grep -n 'Look at HEAD~3 inside src/\.' <<<"$PROMPT" | cut -d: -f1)" ]; then
        log_pass "multi: prose kept its position between the two expansions"
    else
        log_fail "multi: prose position preserved" "$PROMPT"
    fi
fi

# ---- 2. args ---------------------------------------------------------------
OUT="$(run_driver '/skill:reviewer origin/main')"
PROMPT="$(jq -r '.prompt' <<<"$OUT")"
if grep -q 'Review the diff at origin/main\.' <<<"$PROMPT"; then
    log_pass "args: \$ARGUMENTS bound in a discovered skill"
else
    log_fail "args: \$ARGUMENTS bound" "$PROMPT"
fi

OUT="$(run_driver '/scope HEAD~3 "src/internal tools"')"
PROMPT="$(jq -r '.prompt' <<<"$OUT")"
if grep -q 'Look at HEAD~3 inside src/internal tools\.' <<<"$PROMPT"; then
    log_pass "args: quoted positional stayed one argument"
else
    log_fail "args: quoted positional" "$PROMPT"
fi

# ---- 3. passthrough --------------------------------------------------------
OUT="$(run_driver '/notacommand do a thing
```
/scope a b
```')"
PROMPT="$(jq -r '.prompt' <<<"$OUT")"
if grep -q '^/notacommand do a thing$' <<<"$PROMPT"; then
    log_pass "passthrough: an unresolved invocation is left verbatim"
else
    log_fail "passthrough: unresolved left verbatim" "$PROMPT"
fi
if grep -q '^/scope a b$' <<<"$PROMPT"; then
    log_pass "passthrough: a fenced invocation is not expanded"
else
    log_fail "passthrough: fenced not expanded" "$PROMPT"
fi

# ---- 4. tui_only -----------------------------------------------------------
OUT="$(run_driver '/compact')"
if [ "$(jq -r '.ok' <<<"$OUT")" = "false" ] &&
   grep -q 'interactive' <<<"$(jq -r '.error' <<<"$OUT")"; then
    log_pass "tui_only: /compact rejected outside the TUI"
else
    log_fail "tui_only: /compact rejected" "$OUT"
fi

# ---- summary ---------------------------------------------------------------
echo
echo "passed: $PASS  failed: $FAIL"
[ "$FAIL" -eq 0 ]
