#!/usr/bin/env bash
# probe_bedrock_tools.sh — two-turn tool-call round trip against Bedrock's
# InvokeModel route for the OpenAI-wire models the harness serves through the
# LiteLLM Bedrock passthrough (docs/providers.md, "GPT-6.1 on Bedrock").
#
# Turn 1 offers a get_weather tool and expects a tool call; turn 2 returns the
# tool result and expects a text answer. That is the shape every agent turn
# takes, and the one OpenAI documents only for the Responses API on GPT-6.1.
#
# Usage:
#   LOCAL_ENDPOINT=https://litellm.example LOCAL_ENDPOINT_API_KEY=... \
#     scripts/probe_bedrock_tools.sh [profile ...]
#
# Profiles default to the Kimi K3.1 and GPT-6.1 inference profiles. Set
# PROBE_REASONING_EFFORT (e.g. xhigh) to send reasoning_effort. The key is
# passed to curl on a file descriptor, never on its command line, and is never
# printed. Exits 0 when every profile passes, 1 when any fails, 2 on usage
# errors.
set -euo pipefail

if [[ -z "${LOCAL_ENDPOINT:-}" || -z "${LOCAL_ENDPOINT_API_KEY:-}" ]]; then
  echo "usage: LOCAL_ENDPOINT and LOCAL_ENDPOINT_API_KEY must be set" >&2
  exit 2
fi
for bin in curl jq; do
  command -v "$bin" >/dev/null || { echo "missing dependency: $bin" >&2; exit 2; }
done

profiles=("$@")
if [[ ${#profiles[@]} -eq 0 ]]; then
  profiles=(
    global.moonshotai.kimi-k3.1
    us.moonshotai.kimi-k3.1
    global.openai.gpt-6.1-sol
    us.openai.gpt-6.1-sol
    global.openai.gpt-6.1-terra
    us.openai.gpt-6.1-terra
  )
fi

base="${LOCAL_ENDPOINT%/}/bedrock/model"
effort="${PROBE_REASONING_EFFORT:-}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

tools_json='[{"type":"function","function":{"name":"get_weather","description":"Current weather for a city","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]'

# invoke <profile> <body file> <response file>; prints the HTTP status.
invoke() {
  curl -sS -o "$3" -w '%{http_code}' --max-time 300 \
    -H @<(printf 'Authorization: Bearer %s\n' "$LOCAL_ENDPOINT_API_KEY") \
    -H 'Content-Type: application/json' \
    --data-binary @"$2" \
    "$base/$1/invoke"
}

# body <profile> <messages json>; prints a chat-completions request body.
body() {
  jq -n --arg model "$1" --argjson messages "$2" --argjson tools "$tools_json" --arg effort "$effort" \
    '{model: $model, messages: $messages, tools: $tools, tool_choice: "auto", max_completion_tokens: 2048}
     + (if $effort == "" then {} else {reasoning_effort: $effort} end)'
}

fail=0
for profile in "${profiles[@]}"; do
  dir="$work/$profile"
  mkdir -p "$dir"

  turn1='[{"role":"system","content":"Answer weather questions with the get_weather tool."},{"role":"user","content":"What is the weather in Oslo right now?"}]'
  body "$profile" "$turn1" >"$dir/req1.json"
  status="$(invoke "$profile" "$dir/req1.json" "$dir/res1.json" || true)"
  call="$(jq -c '.choices[0].message.tool_calls[0] // empty' "$dir/res1.json" 2>/dev/null || true)"
  if [[ "$status" != 200 || -z "$call" || "$(jq -r '.function.name' <<<"$call")" != get_weather ]]; then
    echo "FAIL $profile: turn 1 returned HTTP $status without a get_weather call: $(head -c 600 "$dir/res1.json")"
    fail=1
    continue
  fi
  if ! jq -e '.function.arguments | fromjson | .city | type == "string"' <<<"$call" >/dev/null 2>&1; then
    echo "FAIL $profile: turn 1 tool arguments are not {\"city\": string}: $(jq -r '.function.arguments' <<<"$call")"
    fail=1
    continue
  fi

  assistant="$(jq -c '.choices[0].message | {role: "assistant", content: (.content // ""), tool_calls: .tool_calls}' "$dir/res1.json")"
  turn2="$(jq -c --argjson assistant "$assistant" --arg id "$(jq -r '.id' <<<"$call")" \
    '. + [$assistant, {role: "tool", tool_call_id: $id, content: "{\"temperature_c\": 7, \"conditions\": \"light rain\"}"}]' <<<"$turn1")"
  body "$profile" "$turn2" >"$dir/req2.json"
  status="$(invoke "$profile" "$dir/req2.json" "$dir/res2.json" || true)"
  text="$(jq -r '.choices[0].message.content // empty' "$dir/res2.json" 2>/dev/null || true)"
  if [[ "$status" != 200 || -z "$text" ]]; then
    echo "FAIL $profile: turn 2 returned HTTP $status without an answer: $(head -c 600 "$dir/res2.json")"
    fail=1
    continue
  fi
  echo "PASS $profile: $(head -c 120 <<<"$text" | tr '\n' ' ')"
done

exit "$fail"
