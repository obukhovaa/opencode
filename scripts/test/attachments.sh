#!/usr/bin/env bash
# E2E blackbox test for binary attachments and thinking-content replay
# against the REAL provider API (same convention as grep.sh).
#
# Covers three paths that unit tests cannot exercise end-to-end:
#   1. image/png attachment    → must reach the model as an image block
#   2. application/pdf         → must reach the model as a document/file
#                                block (NOT an image block — the regression
#                                that poisoned Telegram-bridge sessions)
#   3. thinking + tool loop    → with extended thinking enabled, the API
#                                requires the assistant's signed thinking
#                                blocks to be replayed verbatim alongside
#                                tool_use on the next request; if replay is
#                                broken the second request 400s and the run
#                                never produces the codeword.
#
# Attachments are sent through `opencode serve` (POST /session/{id}/message
# with a data-URL file part) because the CLI has no attachment flag; the
# thinking test uses the plain non-interactive CLI.
#
# Fixtures are generated at runtime with stdlib-only python3: a 64x64 solid
# red PNG (~100 bytes) and a valid single-page PDF (~600 bytes) containing a
# codeword that does not appear anywhere in the prompt.
#
# Requires real provider credentials (default model is vertexai — ambient
# gcloud ADC), plus curl, jq, python3. The model must support attachments.
#
# Usage:  ./scripts/test/attachments.sh [path-to-binary]
#   If a binary path is given it is used as-is; otherwise a fresh build is done.

set -euo pipefail

# ── config ───────────────────────────────────────────────────────────
TIMEOUT="3m"
MODEL="${OPENCODE_TEST_MODEL:-vertexai.claude-sonnet-4-6}"
PDF_CODEWORD="ZEBRA-PLUTO-4271"
FILE_CODEWORD="AURORA-HADDOCK-9931"

# ── colours / helpers ────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'
PASS=0; FAIL=0; SKIP=0

log_pass() { PASS=$((PASS + 1)); printf "${GREEN}PASS${NC}  %s\n" "$1"; }
log_fail() { FAIL=$((FAIL + 1)); printf "${RED}FAIL${NC}  %s  (%s)\n" "$1" "$2"; }
log_skip() { SKIP=$((SKIP + 1)); printf "${YELLOW}SKIP${NC}  %s  (%s)\n" "$1" "$2"; }

# ── prerequisites ────────────────────────────────────────────────────
for cmd in curl jq python3; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "Required tool not found: $cmd" >&2
        exit 1
    fi
done

# ── build / locate binary ───────────────────────────────────────────
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORKDIR="$(mktemp -d)"
BUILT_BINARY=false
SERVER_PID=""

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

cleanup() {
    if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill -INT "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    rm -rf "$WORKDIR"
    if [ "$BUILT_BINARY" = true ] && [ -f "$BIN" ]; then
        rm -f "$BIN"
    fi
}
trap cleanup EXIT

# ── project config ───────────────────────────────────────────────────
cat > "$WORKDIR/.opencode.json" << CFGEOF
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

# ── fixtures (stdlib-only python3) ───────────────────────────────────
python3 - "$WORKDIR" "$PDF_CODEWORD" << 'PYEOF'
import struct, sys, zlib
from binascii import crc32

workdir, codeword = sys.argv[1], sys.argv[2]

# 64x64 solid-red PNG, hand-assembled (no PIL).
def chunk(typ, data):
    return struct.pack('>I', len(data)) + typ + data + struct.pack('>I', crc32(typ + data) & 0xFFFFFFFF)

w = h = 64
raw = b''.join(b'\x00' + b'\xff\x00\x00' * w for _ in range(h))
png = (b'\x89PNG\r\n\x1a\n'
       + chunk(b'IHDR', struct.pack('>IIBBBBB', w, h, 8, 2, 0, 0, 0))
       + chunk(b'IDAT', zlib.compress(raw))
       + chunk(b'IEND', b''))
with open(workdir + '/red.png', 'wb') as f:
    f.write(png)

# Minimal valid single-page PDF with the codeword as its only text.
# Offsets in the xref table are computed, so strict parsers accept it.
stream = ('BT /F1 24 Tf 72 700 Td (The codeword is %s) Tj ET' % codeword).encode()
objs = [
    b'<< /Type /Catalog /Pages 2 0 R >>',
    b'<< /Type /Pages /Kids [3 0 R] /Count 1 >>',
    b'<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] '
    b'/Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>',
    b'<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>',
    b'<< /Length ' + str(len(stream)).encode() + b' >>\nstream\n' + stream + b'\nendstream',
]
out = bytearray(b'%PDF-1.4\n')
offsets = []
for i, obj in enumerate(objs, 1):
    offsets.append(len(out))
    out += str(i).encode() + b' 0 obj\n' + obj + b'\nendobj\n'
xref = len(out)
out += b'xref\n0 ' + str(len(objs) + 1).encode() + b'\n0000000000 65535 f \n'
for off in offsets:
    out += ('%010d 00000 n \n' % off).encode()
out += (b'trailer\n<< /Size ' + str(len(objs) + 1).encode() + b' /Root 1 0 R >>\n'
        b'startxref\n' + str(xref).encode() + b'\n%%EOF\n')
with open(workdir + '/codeword.pdf', 'wb') as f:
    f.write(bytes(out))
PYEOF

echo "$FILE_CODEWORD" > "$WORKDIR/secret.txt"

# build_prompt_body FILE MIME FILENAME PROMPT OUT — JSON body with a text
# part plus one data-URL file part, built by python3 to avoid shell quoting
# and base64 line-wrapping pitfalls.
build_prompt_body() {
    python3 - "$1" "$2" "$3" "$4" "$5" << 'PYEOF'
import base64, json, sys
path, mime, filename, prompt, out = sys.argv[1:6]
with open(path, 'rb') as f:
    data = base64.b64encode(f.read()).decode()
body = {"parts": [
    {"type": "text", "text": prompt},
    {"type": "file", "url": "data:%s;base64,%s" % (mime, data),
     "filename": filename, "mime": mime},
]}
with open(out, 'w') as f:
    json.dump(body, f)
PYEOF
}

echo ""
echo "=== Attachment & thinking E2E tests ==="
echo "Binary:  $BIN"
echo "Model:   $MODEL"
echo "Workdir: $WORKDIR"
echo ""

# ── start server ─────────────────────────────────────────────────────
PORT=$(python3 -c "import socket; s=socket.socket(); s.bind(('',0)); print(s.getsockname()[1]); s.close()")
(cd "$WORKDIR" && "$BIN" serve --port "$PORT" --hostname 127.0.0.1) 2>"$WORKDIR/server.log" &
SERVER_PID=$!

BASE="http://127.0.0.1:$PORT"
READY=false
for _ in $(seq 1 30); do
    if curl -sf "$BASE/global/health" >/dev/null 2>&1; then
        READY=true
        break
    fi
    sleep 0.5
done
if [ "$READY" != true ]; then
    printf "${RED}ERROR${NC}  Server did not become ready within 15s\n"
    cat "$WORKDIR/server.log"
    exit 1
fi

# new_session — create an auto-approved session, echo its id.
new_session() {
    curl -sf -X POST -H "Content-Type: application/json" \
        -d '{"permission":[{"permission":"*","pattern":"*","action":"allow"}]}' \
        "$BASE/session" | jq -r '.id'
}

# prompt_with_file NAME FILE MIME FILENAME PROMPT — run one attachment
# prompt in a fresh session, echo the assistant's text parts.
prompt_with_file() {
    local name="$1" file="$2" mime="$3" filename="$4" prompt="$5"
    local session_id body_file resp
    session_id=$(new_session)
    if [ -z "$session_id" ] || [ "$session_id" = "null" ]; then
        log_fail "$name" "failed to create session"
        return 1
    fi
    body_file="$WORKDIR/body-$filename.json"
    build_prompt_body "$file" "$mime" "$filename" "$prompt" "$body_file"
    resp=$(curl -s --max-time 180 -X POST -H "Content-Type: application/json" \
        -d @"$body_file" "$BASE/session/$session_id/message")
    echo "$resp" | jq -r '[.parts[]? | select(.type == "text") | .text] | join("\n")' 2>/dev/null
}

# ── 1. image/png attachment ──────────────────────────────────────────
name="image/png attachment processed by model"
answer=$(prompt_with_file "$name" "$WORKDIR/red.png" "image/png" "red.png" \
    "Look at the attached image. Reply with ONLY the dominant color of the image as a single lowercase English word, nothing else.") || answer=""
if echo "$answer" | grep -qi "red"; then
    log_pass "$name"
else
    log_fail "$name" "expected 'red' in answer, got: ${answer:-<empty>}"
fi

# ── 2. application/pdf attachment ────────────────────────────────────
# Before the MIME-routing fix PDFs were sent as image blocks, which the API
# rejects (Bedrock even resets the stream) — this asserts the document path
# works end-to-end and the model can actually read the PDF's content.
name="application/pdf attachment processed by model"
answer=$(prompt_with_file "$name" "$WORKDIR/codeword.pdf" "application/pdf" "codeword.pdf" \
    "Read the attached PDF document. It contains a codeword. Reply with ONLY that exact codeword, nothing else.") || answer=""
if echo "$answer" | grep -q "$PDF_CODEWORD"; then
    log_pass "$name"
else
    log_fail "$name" "expected codeword $PDF_CODEWORD in answer, got: ${answer:-<empty>}"
fi

# Server no longer needed; the thinking test uses the CLI.
kill -INT "$SERVER_PID" 2>/dev/null || true
wait "$SERVER_PID" 2>/dev/null || true
SERVER_PID=""

# ── 3. thinking content replayed across a tool round-trip ────────────
# "Think" in the prompt triggers extended thinking (DefaultShouldThinkFn);
# adaptive-thinking models think regardless. The read-tool call forces a
# second API request whose history must replay the first turn's signed
# thinking block ahead of its tool_use — the API rejects the request
# otherwise, so a broken replay path means the codeword never comes back.
name="thinking replay across tool use"
RAW=$( (cd "$WORKDIR" && "$BIN" -q --timeout "$TIMEOUT" --auto-approve \
    -p "Think carefully about how to approach this. Use the read tool to read the file secret.txt in the current directory, then reply with the exact codeword it contains.") 2>&1 | cat ) || true
if echo "$RAW" | grep -q "$FILE_CODEWORD"; then
    log_pass "$name"
else
    log_fail "$name" "expected codeword $FILE_CODEWORD in output, got: $(echo "$RAW" | tail -5)"
fi

# ── summary ──────────────────────────────────────────────────────────
echo ""
printf "=== Results: ${GREEN}%d passed${NC}, ${RED}%d failed${NC}, ${YELLOW}%d skipped${NC} ===\n" "$PASS" "$FAIL" "$SKIP"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
