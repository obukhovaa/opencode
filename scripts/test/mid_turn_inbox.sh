#!/usr/bin/env bash
# E2E gate for the cd-5480 mid-turn inbox on interactive flow steps.
#
# Reviewer input that arrives while an interactive step's agent works
# crosses two subsystems that never import each other, joined only by the
# process-wide inbox (internal/inbox):
#
#   Bridge  — POST /router/inbound with a messages batch goes through the
#             real handler and inbound loop into the inbox, and a drain is
#             acknowledged by one `kind: "delivered"` frame POSTed by the
#             real external adapter to a relay; a re-sent id is skipped and
#             acknowledged again.
#   Agent   — the real processGeneration loop (scripted provider, no live
#             LLM) stores two messages that arrived during a tool call as
#             consecutive user messages right after the tool result, holds
#             the run open at a natural end turn and after struct_output,
#             and cannot be kept alive past max_turns.
#
# Like struct_output.sh this wraps `go test` on the real subsystems: the
# agent loop has no exported seam to inject a fake provider from another
# package, so the agent half needs the in-package scripted provider.
#
# Usage: ./scripts/test/mid_turn_inbox.sh
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
PASS=0; FAIL=0

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

# Isolate HOME: config.Load merges ~/.opencode.json. Pin Go's own paths
# first, since they default to locations under HOME.
export GOPATH="$(go env GOPATH)" GOMODCACHE="$(go env GOMODCACHE)" GOCACHE="$(go env GOCACHE)"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT
export HOME="$WORKDIR/home" XDG_CONFIG_HOME="$WORKDIR/home/.config"
mkdir -p "$XDG_CONFIG_HOME"
# config.Load needs one provider to validate the default agents; no call
# ever reaches it.
export ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-e2e-dummy}"

run_group() {
    local name="$1" pkg="$2" pattern="$3"
    local out
    out="$(mktemp)"
    if go test "$pkg" -run "$pattern" -count=1 -race >"$out" 2>&1; then
        printf "${GREEN}PASS${NC}  %s\n" "$name"
        PASS=$((PASS + 1))
    else
        printf "${RED}FAIL${NC}  %s\n" "$name"
        sed 's/^/    /' "$out"
        FAIL=$((FAIL + 1))
    fi
    rm -f "$out"
}

echo ""
echo "=== mid-turn inbox for interactive steps E2E (cd-5480) ==="
echo ""

run_group "1. inbox: ordering, atomic batches, overflow, concurrent push/drain" \
    ./internal/inbox/ '.'
run_group "2. bridge: inbound batch -> inbox -> one delivered frame; repeat re-acked" \
    ./internal/bridge/service/ 'TestMidTurnInbox_InboundToDeliveredFrame'
run_group "3. bridge: unanswered close, plain answers, id memory, leftovers" \
    ./internal/bridge/service/ 'TestEnqueueBatch|TestHandleNewRequest_(QueuedInput|PlainHead)|TestPlainTextStillAnswers|TestDeliveredIDs|TestUnbind_Leftovers|TestOnInboxDelivered'
run_group "4. agent: drain after tool results, holds, max_turns bound" \
    ./internal/llm/agent/ 'TestInbox_'
run_group "5. external relay: delivered frame shape" \
    ./internal/bridge/external/ 'TestSendDelivered'

echo ""
printf "Results: %d passed, %d failed\n" "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
