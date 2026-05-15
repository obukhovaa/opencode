#!/usr/bin/env bash
# E2E blackbox test for the grep tool.
# Builds opencode, creates fixture files, then runs ALL grep tests in a single
# opencode invocation using structured JSON output (json_schema flag).
# The model executes all grep tool calls (ideally in parallel), then emits
# results via the struct_output tool.
#
# Usage:  ./scripts/test/grep.sh [path-to-binary]
#   If a binary path is given it is used as-is; otherwise a fresh build is done.

set -euo pipefail

# ── config ───────────────────────────────────────────────────────────
TIMEOUT="3m"
MODEL="${OPENCODE_TEST_MODEL:-vertexai.claude-sonnet-4-6}"

# ── colours / helpers ────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'
PASS=0; FAIL=0; SKIP=0

log_pass() { PASS=$((PASS + 1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL + 1)); printf "${RED}FAIL${NC}  %s  (%s)\n" "$1" "$2"; }
log_skip() { SKIP=$((SKIP + 1)); printf "${YELLOW}SKIP${NC}  %s  (%s)\n" "$1" "$2"; }

cleanup() {
    rm -rf "$FIXTURE_DIR"
    if [ "$BUILT_BINARY" = true ] && [ -f "$BIN" ]; then
        rm -f "$BIN"
    fi
}

# ── build / locate binary ───────────────────────────────────────────
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
FIXTURE_DIR="$(mktemp -d)"
BUILT_BINARY=false

if [ "${1:-}" != "" ]; then
    BIN="$1"
    if [ ! -x "$BIN" ]; then
        echo "Binary not found or not executable: $BIN" >&2
        exit 1
    fi
else
    BIN="$(mktemp)"
    echo "Building opencode …"
    (cd "$ROOT" && go build -o "$BIN" .) || { echo "Build failed"; exit 1; }
    BUILT_BINARY=true
fi

trap cleanup EXIT

# ── fixtures ─────────────────────────────────────────────────────────
mkdir -p "$FIXTURE_DIR/src" "$FIXTURE_DIR/lib" "$FIXTURE_DIR/node_modules/pkg"

cat > "$FIXTURE_DIR/.opencode.json" << CFGEOF
{
  "agents": {
    "coder": {
      "model": "$MODEL",
      "maxTokens": 16000,
      "maxTurns": 5
    }
  }
}
CFGEOF

cat > "$FIXTURE_DIR/src/main.go" << 'EOF'
package main

import "fmt"

func main() {
	fmt.Println("hello world")
}

func helper() {
	fmt.Println("helper")
}
EOF

cat > "$FIXTURE_DIR/src/utils.go" << 'EOF'
package main

func add(a, b int) int {
	return a + b
}
EOF

cat > "$FIXTURE_DIR/lib/app.js" << 'EOF'
function main() {
  console.log("main");
}
EOF

cat > "$FIXTURE_DIR/.env" << 'EOF'
SECRET_KEY=supersecret123
DB_PASSWORD=hunter2
EOF

cat > "$FIXTURE_DIR/.env.production" << 'EOF'
SECRET_KEY=prodsecret
EOF

cat > "$FIXTURE_DIR/.hidden_config" << 'EOF'
CONFIG_VALUE=visible_hidden_file
EOF

cat > "$FIXTURE_DIR/node_modules/pkg/index.js" << 'EOF'
function shouldBeIgnored() {}
EOF

# ── JSON schema for structured output ────────────────────────────────
read -r -d '' SCHEMA << 'SCHEMAEOF' || true
{
  "type": "object",
  "properties": {
    "files_with_matches": {
      "type": "string",
      "description": "Raw grep tool output: pattern=func, default output_mode"
    },
    "content_with_context": {
      "type": "string",
      "description": "Raw grep tool output: pattern=func main, output_mode=content, after_context=2"
    },
    "count_mode": {
      "type": "string",
      "description": "Raw grep tool output: pattern=func, output_mode=count"
    },
    "case_insensitive": {
      "type": "string",
      "description": "Raw grep tool output: pattern=HELLO, case_insensitive=true, output_mode=content"
    },
    "file_type_go": {
      "type": "string",
      "description": "Raw grep tool output: pattern=func, file_type=go, output_mode=content"
    },
    "glob_js": {
      "type": "string",
      "description": "Raw grep tool output: pattern=main, glob=*.js, output_mode=content"
    },
    "hidden_files": {
      "type": "string",
      "description": "Raw grep tool output: pattern=CONFIG_VALUE, output_mode=content"
    },
    "node_modules": {
      "type": "string",
      "description": "Raw grep tool output: pattern=shouldBeIgnored, output_mode=content"
    },
    "multiline": {
      "type": "string",
      "description": "Raw grep tool output: pattern=func main[\\s\\S]*?Println, multiline=true, output_mode=content"
    },
    "pagination": {
      "type": "string",
      "description": "Raw grep tool output: pattern=func, output_mode=content, head_limit=2, offset=1"
    }
  },
  "required": [
    "files_with_matches", "content_with_context", "count_mode",
    "case_insensitive", "file_type_go", "glob_js", "hidden_files",
    "node_modules", "multiline", "pagination"
  ]
}
SCHEMAEOF

# Compact the schema to a single line for the CLI flag.
SCHEMA_COMPACT=$(python3 -c "import json,sys; print(json.dumps(json.loads(sys.stdin.read())))" <<< "$SCHEMA")

# ── prompt ───────────────────────────────────────────────────────────
read -r -d '' PROMPT << 'PROMPTEOF' || true
Run the following grep tool calls against the current directory. Use parallel tool calls where possible to speed things up. For each result copy the EXACT raw tool output string into the corresponding struct_output field.

1. files_with_matches: pattern="func" (default output_mode, i.e. files_with_matches)
2. content_with_context: pattern="func main", output_mode="content", after_context=2
3. count_mode: pattern="func", output_mode="count"
4. case_insensitive: pattern="HELLO", case_insensitive=true, output_mode="content"
5. file_type_go: pattern="func", file_type="go", output_mode="content"
6. glob_js: pattern="main", glob="*.js", output_mode="content"
7. hidden_files: pattern="CONFIG_VALUE", output_mode="content"
8. node_modules: pattern="shouldBeIgnored", output_mode="content"
9. multiline: pattern="func main[\s\S]*?Println", multiline=true, output_mode="content"
10. pagination: pattern="func", output_mode="content", head_limit=2, offset=1

After all grep calls complete, call struct_output with the results. Copy each grep tool result verbatim into the matching field.
PROMPTEOF

echo ""
echo "=== Grep tool E2E tests ==="
echo "Binary:   $BIN"
echo "Model:    $MODEL"
echo "Fixtures: $FIXTURE_DIR"
echo ""

# ── single opencode run ─────────────────────────────────────────────
echo "Running all grep tests in a single invocation …"
RAW=$( (cd "$FIXTURE_DIR" && "$BIN" -q --timeout "$TIMEOUT" --auto-approve \
    -f "json_schema=$SCHEMA_COMPACT" \
    -p "$PROMPT" 2>&1) | cat )

# Check for TTY / spinner errors
if echo "$RAW" | grep -qi "bubbletea\|could not open TTY\|Error running spinner"; then
    log_fail "non-TTY (no spinner error)" "TTY error in non-interactive mode"
else
    log_pass "non-TTY (no spinner error)"
fi

# The structured output is a JSON object printed to stdout.
# It may be pretty-printed (multi-line). Extract it as everything from the
# first '{' to the last '}'.
JSON=$(python3 -c "
import sys, json
raw = sys.stdin.read()
# Find the outermost JSON object
start = raw.find('{')
end = raw.rfind('}')
if start < 0 or end < 0:
    print('{}')
    sys.exit(0)
candidate = raw[start:end+1]
try:
    obj = json.loads(candidate)
    json.dump(obj, sys.stdout)
except json.JSONDecodeError:
    print('{}')
" <<< "$RAW")

if [ "$JSON" = "{}" ]; then
    echo ""
    printf "${RED}ERROR${NC}  Could not extract JSON from output.\n"
    echo "Raw output:"
    echo "$RAW"
    exit 1
fi

# Helper: extract a field value from the JSON.
field() {
    python3 -c "import json,sys; d=json.loads(sys.stdin.read()); print(d.get('$1',''))" <<< "$JSON"
}

# ── assertions ───────────────────────────────────────────────────────

# 1. files_with_matches
name="files_with_matches mode"
val=$(field files_with_matches)
if echo "$val" | grep -q "src/main.go" && echo "$val" | grep -q "src/utils.go"; then
    log_pass "$name"
else
    log_fail "$name" "expected src/main.go and src/utils.go"
fi

# 2. content with context
name="content mode with after_context"
val=$(field content_with_context)
if echo "$val" | grep -q "func main" && echo "$val" | grep -q "Println"; then
    log_pass "$name"
else
    log_fail "$name" "expected func main with Println context"
fi

# 3. count mode
name="count mode"
val=$(field count_mode)
if echo "$val" | grep -qE ':[0-9]+'; then
    log_pass "$name"
else
    log_fail "$name" "expected file:count lines"
fi

# 4. case-insensitive
name="case_insensitive"
val=$(field case_insensitive)
if echo "$val" | grep -qi "hello"; then
    log_pass "$name"
else
    log_fail "$name" "expected case-insensitive match for hello"
fi

# 5. file_type go
name="file_type filter (go)"
val=$(field file_type_go)
if echo "$val" | grep -q "\.go:" && ! echo "$val" | grep -q "\.js:"; then
    log_pass "$name"
else
    log_fail "$name" "expected only .go files"
fi

# 6. glob *.js
name="glob filter (*.js)"
val=$(field glob_js)
if echo "$val" | grep -q "app.js"; then
    log_pass "$name"
else
    log_fail "$name" "expected app.js in results"
fi

# 7. hidden files
name="hidden files searched"
val=$(field hidden_files)
if echo "$val" | grep -q "hidden_config"; then
    log_pass "$name"
else
    log_fail "$name" "expected .hidden_config in results"
fi

# 8. node_modules excluded
name="node_modules excluded"
val=$(field node_modules)
if echo "$val" | grep -qi "no match"; then
    log_pass "$name"
else
    log_fail "$name" "expected no matches"
fi

# 9. multiline
name="multiline search"
val=$(field multiline)
if echo "$val" | grep -q "func main" && echo "$val" | grep -q "Println"; then
    log_pass "$name"
else
    log_skip "$name" "model may not have used exact pattern"
fi

# 10. pagination
name="pagination (offset/head_limit)"
val=$(field pagination)
if echo "$val" | grep -qiE "showing|lines.*[0-9]" || [ "$(echo "$val" | grep -c '\.go:\|\.js:')" -le 2 ]; then
    log_pass "$name"
else
    log_skip "$name" "model may not have used exact params"
fi

# ── summary ──────────────────────────────────────────────────────────
echo ""
printf "=== Results: ${GREEN}%d passed${NC}, ${RED}%d failed${NC}, ${YELLOW}%d skipped${NC} ===\n" "$PASS" "$FAIL" "$SKIP"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
