#!/usr/bin/env bash
# E2E test for telemetry secret redaction (GENAI-360).
#
# Drives cmd/redaction-e2e — a Go driver that runs the REAL pipeline
# (.opencode.json → viper → config.Load → langfuse client → gzipped OTLP
# export over HTTP → protobuf decode) inside mktemp sandboxes and emits a
# JSON verdict per scenario. Unit tests assert on in-memory spans; this
# asserts on the bytes that actually left the process, which is the only way
# to catch a config that survives json.Unmarshal but not the loader.
#
#   1. default_on   — no redaction config ⇒ built-ins apply: no credential
#                     reaches the wire, markers are present, and the known
#                     false positive is preserved untouched.
#   2. disabled     — redaction.enabled=false ⇒ payloads export unredacted
#                     (the documented escape hatch still works).
#   3. custom_rule  — a user rule survives the viper round-trip and redacts
#                     a shape no built-in knows.
#   4. strict_mode  — mode=strict ⇒ markers carry no fingerprint.
#
# No network: the driver serves its own OTLP endpoint on 127.0.0.1.
set -u

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
PASS=0; FAIL=0
log_pass() { PASS=$((PASS+1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL+1)); printf "${RED}FAIL${NC}  %s (%s)\n" "$1" "${2:-}"; }

for cmd in jq; do
  command -v "$cmd" >/dev/null || { echo "Required tool not found: $cmd" >&2; exit 1; }
done

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
DRIVER="$WORKDIR/redaction-e2e"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT

if ! (cd "$ROOT" && go build -o "$DRIVER" ./cmd/redaction-e2e); then
  log_fail "build redaction-e2e driver"
  printf "Results: %d passed, %d failed\n" "$PASS" "$FAIL"
  exit 1
fi

# make_sandbox <name> <.opencode.json contents; empty for none>
make_sandbox() {
  local name="$1" cfg="${2:-}"
  local dir="$WORKDIR/$name"
  mkdir -p "$dir"
  if [ -n "$cfg" ]; then printf '%s' "$cfg" > "$dir/.opencode.json"; fi
  echo "$dir"
}

# run_scenario <name> <config> -> writes JSON verdict to $WORKDIR/<name>.json
run_scenario() {
  local name="$1" cfg="${2:-}"
  local dir; dir="$(make_sandbox "$name" "$cfg")"
  # The driver is one process per scenario on purpose: config.Load is
  # load-once, so a second scenario in the same process would silently reuse
  # the first one's config — which is exactly the bug this guards against.
  "$DRIVER" -dir "$dir" > "$WORKDIR/$name.json" 2> "$WORKDIR/$name.err"
  return 0
}

jqv() { jq -r "$2" < "$WORKDIR/$1.json" 2>/dev/null; }

# ── 1. default_on ────────────────────────────────────────────────────
run_scenario default_on ''
if [ "$(jqv default_on '.ok')" = "true" ]; then
  log_pass "default_on: no credential reached the wire"
else
  log_fail "default_on: credentials leaked" "$(jqv default_on '.leaked | join(\",\")')"
fi
if [ "$(jqv default_on '.markers | length')" -ge 3 ] 2>/dev/null; then
  log_pass "default_on: redaction markers present ($(jqv default_on '.markers | length'))"
else
  log_fail "default_on: too few markers" "$(jqv default_on '.markers | join(\",\")')"
fi
if [ "$(jqv default_on '.false_positive_preserved')" = "true" ]; then
  log_pass "default_on: known false positive preserved"
else
  log_fail "default_on: false positive was mangled" "sk-clusters-fork-ebs-csi-metrics missing"
fi

# ── 2. disabled ──────────────────────────────────────────────────────
run_scenario disabled '{"telemetry":{"redaction":{"enabled":false}}}'
if [ "$(jqv disabled '.leaked | length')" -gt 0 ]; then
  log_pass "disabled: enabled=false exports unredacted (escape hatch works)"
else
  log_fail "disabled: redaction still ran despite enabled=false" "$(jqv disabled '.markers | join(\",\")')"
fi

# ── 3. custom_rule ───────────────────────────────────────────────────
run_scenario custom_rule '{"telemetry":{"redaction":{"rules":[{"name":"PianoInternalID","pattern":"PI-[0-9]{12}"}]}}}'
if jqv custom_rule '.markers | join(",")' | grep -q 'REDACTED:PianoInternalID'; then
  log_pass "custom_rule: user rule survived viper and fired"
else
  log_fail "custom_rule: rule did not apply" "$(jqv custom_rule '.markers | join(\",\")')"
fi

# ── 4. strict_mode ───────────────────────────────────────────────────
run_scenario strict_mode '{"telemetry":{"redaction":{"mode":"strict"}}}'
if jqv strict_mode '.markers | join(",")' | grep -qE 'REDACTED:[a-z-]+\]' && \
   ! jqv strict_mode '.markers | join(",")' | grep -qE 'REDACTED:[a-z-]+:[0-9a-f]{6}\]'; then
  log_pass "strict_mode: markers carry no fingerprint"
else
  log_fail "strict_mode: fingerprints still present" "$(jqv strict_mode '.markers | join(\",\")')"
fi

printf "\nResults: %d passed, %d failed\n" "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
