#!/usr/bin/env bash
# webfetch_output_limit.sh — hermetic e2e for the webfetch output cap
# (openspec change: webfetch-output-limit).
#
# Drives cmd/webfetch-e2e — a Go driver that runs the REAL pipeline
# (.opencode.json → viper → config → agent.NewToolSet → webfetch/grep/read
# tools → a local HTTP server → the process scratch dir) inside mktemp
# sandboxes and emits a JSON verdict per scenario:
#
#   1. default_cap    — no webFetch config ⇒ the built-in 50KB cap applies:
#                       a large page returns a small reply, the full page is
#                       on disk, and a needle buried mid-page is ABSENT from
#                       the reply yet recoverable through the agent's own
#                       grep and read tools at the advertised path.
#   2. configured_cap — webFetch.maxOutputBytes: 4096 survives the viper
#                       round-trip, bounds the reply, and its spill (within
#                       the read tool's size ceiling) opens with read.
#   3. unbounded      — webFetch.maxOutputBytes: -1 returns the whole page
#                       inline with no spill file (the escape hatch).
#   4. under_cap      — a page within the cap is byte-identical to the
#                       pre-feature conversion; no header, no file.
#
# No network: the driver serves its own fixture over 127.0.0.1.
set -u

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
PASS=0; FAIL=0
log_pass() { PASS=$((PASS+1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL+1)); printf "${RED}FAIL${NC}  %s (%s)\n" "$1" "${2:-}"; }

cd "$(dirname "$0")/../.."
REPO="$PWD"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT
DRIVER="$WORKDIR/webfetch-e2e"

echo "── webfetch_output_limit.sh ──"
if ! go build -o "$DRIVER" ./cmd/webfetch-e2e; then
  log_fail "build webfetch-e2e driver"
  printf "Results: %d passed, %d failed\n" "$PASS" "$FAIL"
  exit 1
fi

# make_sandbox <name> <opencode.json contents; empty for none>
make_sandbox() {
  local name="$1" cfg="${2:-}"
  local dir="$WORKDIR/$name"
  mkdir -p "$dir"
  [ -n "$cfg" ] && printf '%s\n' "$cfg" >"$dir/.opencode.json"
  echo "$dir"
}

# run_check <scenario> <sandbox dir> <expected check names...>
run_check() {
  local scenario="$1" dir="$2"; shift 2
  local out
  out="$(cd "$dir" && "$DRIVER" -check "$scenario" 2>"$WORKDIR/$scenario.stderr")" || true
  if [ -z "$out" ]; then
    log_fail "$scenario: driver produced no output" "$(tail -3 "$WORKDIR/$scenario.stderr" | tr '\n' ' ')"
    return
  fi
  local errs
  errs="$(echo "$out" | jq -r '.errors // [] | join("; ")')"
  for check in "$@"; do
    if echo "$out" | jq -e --arg c "$check" '.checks | index($c)' >/dev/null; then
      log_pass "$scenario/$check"
    else
      log_fail "$scenario/$check" "$errs"
    fi
  done
  # Surface the numbers the reviewer actually cares about.
  echo "$out" | jq -r '"      reply=\(.reply_bytes // 0)B saved=\(.saved_bytes // 0)B"'
}

# 1. Default cap: the incident scenario, configured by nobody.
run_check default_cap "$(make_sandbox default_cap '')" \
  large_page_reply_is_small \
  mid_page_content_absent_from_reply \
  full_page_preserved_on_disk \
  grep_tool_finds_needle_in_spill_file \
  oversized_spill_is_beyond_read_as_documented \
  header_points_at_the_recovery_tools

# 2. Configured cap: the public .opencode.json contract.
run_check configured_cap "$(make_sandbox configured_cap '{"webFetch": {"maxOutputBytes": 4096}}')" \
  config_field_round_trips_through_viper \
  configured_cap_bounds_the_reply \
  configured_cap_still_spills \
  read_tool_opens_spill_file

# 3. Escape hatch.
run_check unbounded "$(make_sandbox unbounded '{"webFetch": {"maxOutputBytes": -1}}')" \
  no_spill_when_cap_disabled \
  whole_page_returned_inline

# 4. Back-compat for everything under the cap.
run_check under_cap "$(make_sandbox under_cap '')" \
  small_page_bytes_identical_to_pre_feature \
  no_file_written_under_the_cap

# The published schema is part of the contract this feature adds a field to.
if jq -e '.properties.webFetch.properties.maxOutputBytes.type == "integer"' \
     "$REPO/opencode-schema.json" >/dev/null; then
  log_pass "schema/webFetch.maxOutputBytes published"
else
  log_fail "schema/webFetch.maxOutputBytes published" "regenerate with make schema"
fi

printf "Results: %d passed, %d failed\n" "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
