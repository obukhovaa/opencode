#!/usr/bin/env python3
"""Black-box composition acceptance. Every model response is scripted locally."""
import copy
import gzip
import http.server
import json
import os
from pathlib import Path
import shlex
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

HERE = Path(__file__).resolve()
ROOT = HERE.parents[2]
FIXTURES = ROOT / "internal/llm/tools/testdata/mcp_compose_workflows.json"
MODEL = "claude-sonnet-4-6"
PROMPT = "Read the fixture facts and return the requested identifiers, states and nulls."
TOOLS = ["fixture_" + name for name in ("list", "detail", "status", "nested", "rows")]


def dump(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"))


def load(path):
    return json.loads(Path(path).read_text())


def mcp_main(path):
    scenario = load(path)
    calls = scenario["calls"]
    for line in sys.stdin:
        req = json.loads(line)
        if "id" not in req:
            continue
        method = req["method"]
        result = {}
        if method == "initialize":
            result = {"protocolVersion": "2024-11-05", "capabilities": {"tools": {}},
                      "serverInfo": {"name": "composition-fixture", "version": "1"}}
        elif method == "tools/list":
            result = {"tools": [{"name": name.removeprefix("fixture_"),
                                 "description": "Read deterministic fixture facts",
                                 "inputSchema": {"type": "object", "properties": {}},
                                 "annotations": {"readOnlyHint": True}} for name in TOOLS]}
        elif method == "tools/call":
            params = req["params"]
            actual = {"tool": "fixture_" + params["name"], "arguments": params.get("arguments", {})}
            with open(scenario["callLog"], "a") as log:
                log.write(dump(actual) + "\n")
            matches = [c for c in calls if c["tool"] == actual["tool"] and c["arguments"] == actual["arguments"]]
            if not matches:
                result = {"content": [{"type": "text", "text": '{"unexpected_request":true}'}], "isError": True}
            else:
                call = matches[0]
                result = {"content": [{"type": "text", "text": call["response"]}],
                          "isError": call.get("isError", False)}
        print(dump({"jsonrpc": "2.0", "id": req["id"], "result": result}), flush=True)


def hook_main(path):
    scenario, event = load(path), json.load(sys.stdin)
    name = event["tool_name"]
    if name == "mcp_compose":
        return
    matches = [c for c in scenario["calls"] if c["tool"] == name and c["arguments"] == event["tool_input"]]
    if event["hook_event_name"] == "PreToolUse":
        # Exercise the shared hook argument decoder with a large integer even
        # when the hook returns the input unchanged.
        print(dump({"hookSpecificOutput": {"hookEventName": "PreToolUse", "updatedInput": event["tool_input"]}}))
        return
    if scenario.get("postBlock") == name:
        print('{"id":9007199254740993}', file=sys.stderr)
        sys.exit(2)
    output = {"hookEventName": "PostToolUse"}
    if matches and matches[0].get("hookContext"):
        output["additionalContext"] = matches[0]["hookContext"]
    if scenario.get("redact") == name:
        value = json.loads(event["tool_output"])
        value.pop("id", None)
        output["updatedToolOutput"] = dump(value)
    print(dump({"hookSpecificOutput": output}))


def batches(fixture):
    """Batch every ready independent call; never manufacture ordinary turns."""
    pending = list(zip(fixture["plan"]["steps"], fixture["calls"]))
    done, result = set(), []
    while pending:
        ready = [(s, c) for s, c in pending if all(b["step"] in done for b in s.get("bindings", []))]
        assert ready, "fixture has cyclic dependencies"
        result.append([{"name": c["tool"], "input": c["arguments"]} for _, c in ready])
        done.update(s["id"] for s, _ in ready)
        pending = [(s, c) for s, c in pending if s["id"] not in done]
    return result


class Provider(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        state = self.server.state
        if self.headers.get("Transfer-Encoding") == "chunked":
            chunks = []
            while True:
                size = int(self.rfile.readline().split(b";", 1)[0].strip(), 16)
                if not size:
                    assert self.rfile.readline() == b"\r\n"
                    break
                chunks.append(self.rfile.read(size))
                assert self.rfile.read(2) == b"\r\n"
            raw = b"".join(chunks)
        else:
            raw = self.rfile.read(int(self.headers["Content-Length"]))
        if self.path == "/api/public/otel/v1/traces":
            if self.headers.get("Content-Encoding") == "gzip":
                raw = gzip.decompress(raw)
            state.setdefault("telemetry", []).append(raw)
            self.send_response(200)
            self.send_header("Content-Type", "application/x-protobuf")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        request = json.loads(raw)
        if self.path.endswith("/count_tokens"):
            payload = b'{"input_tokens":100}'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            return
        # Descriptor/title requests are irrelevant to the agent request gate.
        if not any(t.get("name") in TOOLS + ["mcp_compose"] for t in request.get("tools", [])):
            blocks, stop = [{"type": "text", "text": "Fixture"}], "end_turn"
        else:
            index = len(state["requests"])
            state.setdefault("tools", []).append([t.get("name") for t in request["tools"]])
            state["requests"].append(raw)
            if index < len(state["batches"]):
                blocks = [{"type": "tool_use", "id": f"fixture_call_{index}_{i}", **call}
                          for i, call in enumerate(state["batches"][index])]
                stop = "tool_use"
            else:
                blocks, stop = [{"type": "text", "text": "Fixture complete."}], "end_turn"
        message = {"id": "fixture_message", "type": "message", "role": "assistant", "model": MODEL,
                   "content": blocks, "stop_reason": stop, "stop_sequence": None,
                   "usage": {"input_tokens": 100, "output_tokens": 20}}
        if not request.get("stream"):
            payload = dump(message).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            return
        events = [{"type": "message_start", "message": {**message, "content": [], "stop_reason": None}}]
        for i, block in enumerate(blocks):
            initial = {**block, "input": {}} if block["type"] == "tool_use" else {"type": "text", "text": ""}
            events.append({"type": "content_block_start", "index": i, "content_block": initial})
            delta = ({"type": "input_json_delta", "partial_json": dump(block["input"])}
                     if block["type"] == "tool_use" else {"type": "text_delta", "text": block["text"]})
            events.extend([{"type": "content_block_delta", "index": i, "delta": delta},
                           {"type": "content_block_stop", "index": i}])
        events.extend([{"type": "message_delta", "delta": {"stop_reason": stop, "stop_sequence": None},
                        "usage": {"output_tokens": 20}}, {"type": "message_stop"}])
        payload = "".join(f"event: {e['type']}\ndata: {dump(e)}\n\n" for e in events).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def api(base, path, value=None):
    request = urllib.request.Request(base + path, data=dump(value).encode() if value is not None else None,
                                     headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(request, timeout=45) as response:
        return json.load(response)


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def contents(request):
    return [block for msg in json.loads(request)["messages"] for block in msg.get("content", [])
            if isinstance(block, dict) and block.get("type") == "tool_result"]


def result_text(block):
    content = block["content"]
    return content if isinstance(content, str) else "".join(p.get("text", "") for p in content)


def pointer(value, path):
    for part in path.split("/")[1:]:
        part = part.replace("~1", "/").replace("~0", "~")
        value = value[int(part)] if isinstance(value, list) else value[part]
    return value


def project(value, select):
    if "rows" in select:
        return [{key: pointer(row, path) for key, path in select["fields"].items()}
                for row in pointer(value, select["rows"])[:select["limit"]]]
    return {key: pointer(value, path) for key, path in select["fields"].items()}


def run(binary, work, provider, fixture, mode, label):
    artifacts = work / "artifacts" / label
    artifacts.mkdir(parents=True)
    scenario = copy.deepcopy(fixture)
    scenario["callLog"] = str(artifacts / "calls.jsonl")
    scenario_path = artifacts / "scenario.json"
    scenario_path.write_text(dump(scenario))
    composed = mode in ("composed", "deferred-composed")
    provider.state = {"requests": [], "batches": ([ [{"name": "mcp_compose", "input": fixture["plan"]}] ]
                                                    if composed else batches(fixture))}
    allow = TOOLS + (["mcp_compose"] if composed else [])
    if fixture.get("unauthorized"):
        allow.remove(fixture["unauthorized"])
    config = {
        "data": {"directory": str(artifacts / "data")}, "autoCompact": False,
        "telemetry": {
            "langfuse": {"enabled": True, "publicKey": "fixture-public", "secretKey": "fixture-secret",
                         "baseURL": f"http://127.0.0.1:{provider.server_port}"},
            "tools": {"enabled": True, "logInput": ["*"], "logOutput": ["*"]},
            "generations": {"enabled": True, "logInput": ["*"], "logOutput": ["*"]},
            "redaction": {"enabled": False},
        }, "contextPaths": [],
        "agents": {"coder": {"model": "claude-4.6-sonnet", "maxTokens": 1024, "maxTurns": 8,
                              "prompt": "You read deterministic local fixture facts.",
                              "allowTools": allow, "parallelToolUse": True}},
        "providers": {"anthropic": {"apiKey": "fixture-not-a-real-key",
                                     "baseURL": f"http://127.0.0.1:{provider.server_port}"}},
        "mcpServers": {"fixture": {"type": "stdio", "command": sys.executable,
                                   "args": [str(HERE), "--mcp", str(scenario_path)]}},
        "hooks": {event: [{"matcher": "fixture_.*", "hooks": [{"type": "command",
                   "command": shlex.join([sys.executable, str(HERE), "--hook", str(scenario_path)])}]}]
                  for event in ("PreToolUse", "PostToolUse")},
    }
    if fixture.get("outerDeny"):
        config["agents"]["coder"]["permission"] = {"mcp_compose": "deny"}
    if mode == "deferred-composed":
        config["agents"]["coder"]["allowTools"].append("toolsearch")
        config["agents"]["coder"]["deferredTools"] = {"fixture_*": True}
    if mode != "absent":
        config["mcpComposition"] = {"readTools": TOOLS}
    (work / ".opencode.json").write_text(dump(config))
    env = {"PATH": os.environ["PATH"], "HOME": str(work / "home"), "XDG_CONFIG_HOME": str(work / "home/.config"),
           "TMPDIR": str(work / "tmp"), "LANG": "C.UTF-8", "ANTHROPIC_API_KEY": "fixture-not-a-real-key"}
    port = free_port()
    base = f"http://127.0.0.1:{port}"
    with open(artifacts / "server.log", "wb") as log, open(artifacts / "events.txt", "wb") as events:
        process = subprocess.Popen([binary, "serve", "--hostname", "127.0.0.1", "--port", str(port), "--auto-approve", "--agent", "coder"],
                                   cwd=work, env=env, stdout=log, stderr=log)
        event_process = None
        try:
            deadline = time.monotonic() + 25
            while True:
                try:
                    api(base, "/global/health")
                    break
                except (OSError, urllib.error.URLError):
                    assert process.poll() is None, f"server exited: {artifacts / 'server.log'}"
                    assert time.monotonic() < deadline, "server startup timed out"
                    time.sleep(.05)
            event_process = subprocess.Popen(["curl", "-sN", base + "/event"], stdout=events, stderr=subprocess.DEVNULL)
            time.sleep(.1)
            session = api(base, "/session", {"title": "Composition fixture"})
            api(base, f"/session/{session['id']}/message", {"parts": [{"type": "text", "text": PROMPT}]})
            history = api(base, f"/session/{session['id']}/message")
            (artifacts / "history.json").write_text(dump(history))
        finally:
            if event_process:
                event_process.terminate()
                event_process.wait(timeout=5)
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
    state = provider.state
    assert state.get("telemetry"), "local telemetry exporter was not exercised"
    (artifacts / "telemetry.pb").write_bytes(b"\n".join(state["telemetry"]))
    events = (artifacts / "events.txt").read_text()
    assert '"type":"message.part.updated"' in events, "SSE capture did not observe tool activity"
    assert all(("mcp_compose" in names) == composed for names in state.get("tools", [])), "composition registration gate"
    assert len(state["requests"]) == len(state["batches"]) + 1, f"{label}: unexpected model decision count"
    for i, request in enumerate(state["requests"]):
        (artifacts / f"request-{i}.json").write_bytes(request)
    actual_calls = [json.loads(line) for line in Path(scenario["callLog"]).read_text().splitlines()] if Path(scenario["callLog"]).exists() else []
    return {"calls": actual_calls, "requests": state["requests"], "results": contents(state["requests"][-1]),
            "artifacts": artifacts}


def check_positive(fixture, ordinary, composed):
    expected_calls = [{"tool": c["tool"], "arguments": c["arguments"]} for c in fixture["calls"]]
    # Independent calls may reach the server in either order.
    canonical = lambda calls: sorted(dump(c) for c in calls)
    assert canonical(ordinary["calls"]) == canonical(expected_calls), "ordinary call parity"
    assert composed["calls"] == expected_calls, "composed call/large-integer parity"
    assert len(composed["results"]) == 1, "child results leaked into provider history"
    assert not any(r.get("is_error") for r in ordinary["results"] + composed["results"])
    envelope = json.loads(result_text(composed["results"][0]).split("\n\n[hook context:")[0])
    assert envelope["complete"] is True
    assert envelope["results"] == fixture["expectedResults"]
    assert [(s["id"], s["tool"], s["status"]) for s in envelope["steps"]] == [
        (s["id"], s["tool"], "ok") for s in fixture["plan"]["steps"]]
    ordinary_by_id = {r["tool_use_id"]: r for r in ordinary["results"]}
    calls_by_id = {b["id"]: b for m in json.loads(ordinary["requests"][-1])["messages"]
                   for b in m.get("content", []) if isinstance(b, dict) and b.get("type") == "tool_use"}
    scripted_calls = {f"fixture_call_{i}_{j}": call for i, group in enumerate(batches(fixture))
                      for j, call in enumerate(group)}
    for key, call in scripted_calls.items():
        assert calls_by_id[key]["input"] == call["input"], "ordinary provider history changed numeric arguments"
    composed_input = next(b["input"] for m in json.loads(composed["requests"][-1])["messages"]
                          for b in m.get("content", []) if b.get("type") == "tool_use")
    assert composed_input == fixture["plan"], "provider history changed the composition plan"
    for step, call in zip(fixture["plan"]["steps"], fixture["calls"]):
        match = next(ordinary_by_id[key] for key, b in scripted_calls.items()
                     if b["name"] == call["tool"] and b["input"] == call["arguments"])
        value = json.loads(result_text(match).split("\n\n[hook context:")[0])
        assert project(value, step["select"]) == fixture["expectedResults"][step["id"]]
        if call.get("hookContext"):
            assert call["hookContext"] in result_text(match)
            assert call["hookContext"] in result_text(composed["results"][0])
    visible = b"\n".join(composed["requests"]) + b"\n".join(
        (composed["artifacts"] / name).read_bytes() for name in ("history.json", "events.txt", "server.log", "telemetry.pb"))
    for call in fixture["calls"]:
        assert call["response"].encode() not in visible, "raw child response leaked"
        # Also detect JSON-escaped copies and long irrelevant-field fragments.
        assert dump(call["response"])[1:-1].encode() not in visible, "escaped child response leaked"
        value = json.loads(call["response"])
        def strings(v):
            if isinstance(v, str):
                yield v
            elif isinstance(v, dict):
                for child in v.values():
                    yield from strings(child)
            elif isinstance(v, list):
                for child in v:
                    yield from strings(child)
        for text in strings(value):
            if len(text) > 1000:
                assert text[:500].encode() not in visible, "irrelevant child data leaked"
    before = sum(len(result_text(r).encode()) for r in ordinary["results"])
    after = len(result_text(composed["results"][0]).encode())
    assert after <= .20 * before, f"{fixture['name']}: result ratio {after}/{before} exceeds 20%"
    avoided = len(ordinary["requests"]) - len(composed["requests"])
    assert avoided == fixture["intermediateDecisionsAvoided"]
    return {"fixture": fixture["name"], "ordinaryResultBytes": before, "composedResultBytes": after,
            "ordinaryRequestBytes": len(ordinary["requests"][-1]), "composedRequestBytes": len(composed["requests"][-1]),
            "scriptedIntermediateDecisionsAvoided": avoided}


def check_negative(fixture, result, expected_code, expected_calls):
    assert result["calls"] == [{"tool": c["tool"], "arguments": c["arguments"]}
                               for c in fixture["calls"][:expected_calls]]
    assert len(result["results"]) == 1
    block = result["results"][0]
    text = result_text(block)
    envelope = json.loads(text.split("\n\n[hook context:")[0])
    assert envelope["complete"] is False
    assert block.get("is_error") is True
    assert envelope["error"]["code"] == expected_code, envelope
    assert len(envelope["error"]["message"].encode()) <= 256
    assert all(s["status"] == "skipped" for s in envelope["steps"][expected_calls:]
               if s["id"] != envelope["error"]["step"])
    visible = b"\n".join(result["requests"]) + b"\n".join(
        (result["artifacts"] / name).read_bytes() for name in ("history.json", "events.txt", "server.log", "telemetry.pb"))
    for call in fixture["calls"]:
        assert call["response"].encode() not in visible, "raw error child response leaked"
    assert b"unprojected-error-canary" not in visible, "server error leaked to diagnostics"
    if fixture.get("forbiddenMarker"):
        assert fixture["forbiddenMarker"].encode() not in visible, "bound argument leaked to diagnostics"
    return envelope


def negative_cases(binary, work, provider, fixtures):
    original = fixtures[0]
    fixture = copy.deepcopy(original)
    fixture["outerDeny"] = True
    result = run(binary, work, provider, fixture, "composed", "outer-permission-deny")
    assert result["calls"] == [], "outer permission deny reached a child"
    assert len(result["results"]) == 1 and result["results"][0].get("is_error")
    assert "denied" in result_text(result["results"][0]).lower()
    print("PASS outer permission deny overrides explicit grant and auto-approve", flush=True)
    cases = []
    fixture = copy.deepcopy(original)
    fixture["calls"][0]["isError"] = True
    fixture["calls"][0]["response"] = dump({"id": 9007199254740993, "secret": "unprojected-error-canary"})
    cases.append(("mcp-valid-json-error", fixture, "child_failed", 1))
    fixture = copy.deepcopy(original)
    fixture["redact"] = "fixture_detail"
    cases.append(("post-hook-redaction-no-fallback", fixture, "projection_failed", 2))
    fixture = copy.deepcopy(original)
    fixture["postBlock"] = "fixture_list"
    cases.append(("post-hook-json-block", fixture, "hook_blocked", 1))
    fixture = copy.deepcopy(original)
    fixture["unauthorized"] = "fixture_list"
    cases.append(("unauthorized-child", fixture, "ineligible_tool", 0))
    fixture = copy.deepcopy(original)
    fixture["forbiddenMarker"] = "unreturned-bound-argument-canary"
    fixture["calls"][0]["response"] = dump({"items": [{"id": fixture["forbiddenMarker"], "name": "demo"}]})
    fixture["calls"][1]["arguments"] = {"id": fixture["forbiddenMarker"]}
    fixture["calls"][1]["isError"] = True
    fixture["plan"]["return"] = ["detail"]
    cases.append(("failed-child-bound-argument-privacy", fixture, "child_failed", 2))
    for label, fixture, code, count in cases:
        result = run(binary, work, provider, fixture, "composed", label)
        envelope = check_negative(fixture, result, code, count)
        if label == "post-hook-redaction-no-fallback":
            assert envelope["results"] == {"lookup": fixture["expectedResults"]["lookup"]}
        print("PASS " + label, flush=True)
    fixture = copy.deepcopy(fixtures[2])
    fixture["calls"][1]["isError"] = True
    result = run(binary, work, provider, fixture, "composed", "failure-preserves-context")
    envelope = check_negative(fixture, result, "child_failed", 2)
    assert envelope["results"] == {"nested": fixture["expectedResults"]["nested"]}
    assert fixture["calls"][0]["hookContext"] in result_text(result["results"][0])
    print("PASS failure preserves prior projection and large hook context", flush=True)
    fixture = copy.deepcopy(fixtures[2])
    fixture["plan"]["steps"][1]["select"]["limit"] = 1
    result = run(binary, work, provider, fixture, "composed", "incomplete-row-limit")
    block = result["results"][0]
    envelope = json.loads(result_text(block).split("\n\n[hook context:")[0])
    assert not block.get("is_error") and envelope["complete"] is False
    assert envelope["results"]["rows"] == fixture["expectedResults"]["rows"][:1]
    assert envelope["steps"][1] == {"id": "rows", "tool": "fixture_rows", "status": "ok",
                                      "totalRows": 2, "returnedRows": 1, "limited": True}
    assert fixture["calls"][0]["hookContext"] in result_text(block)
    print("PASS incomplete rows preserve provenance and large hook context", flush=True)


def main(binary, sandbox):
    work = Path(sandbox) / "work"
    for path in (work / "home/.config", work / "tmp"):
        path.mkdir(parents=True)
    provider = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Provider)
    threading.Thread(target=provider.serve_forever, daemon=True).start()
    fixtures = load(FIXTURES)
    report = []
    try:
        for fixture in fixtures:
            ordinary = run(binary, work, provider, fixture, "ordinary", fixture["name"] + "-ordinary")
            composed = run(binary, work, provider, fixture, "composed", fixture["name"] + "-composed")
            row = check_positive(fixture, ordinary, composed)
            report.append(row)
            print("PASS " + dump(row), flush=True)
            if fixture is fixtures[0]:
                deferred = run(binary, work, provider, fixture, "deferred-composed", "undiscovered-reads")
                check_positive(fixture, ordinary, deferred)
                assert len(deferred["requests"]) == 2, "composition required a schema-discovery round trip"
                print("PASS configured deferred reads compose before any search or ordinary child call", flush=True)
        assert sum(r["composedRequestBytes"] for r in report) < sum(r["ordinaryRequestBytes"] for r in report)
        absent = run(binary, work, provider, fixtures[0], "absent", "default-off-absent")
        configured = run(binary, work, provider, fixtures[0], "ordinary", "default-off-configured")
        # No normalization: the scripted provider IDs, cwd and prompt are fixed.
        assert absent["requests"] == configured["requests"], "default-off Claude request bytes changed"
        print("PASS default-off actual Claude requests: byte-identical (no normalization)", flush=True)
        negative_cases(binary, work, provider, fixtures)
        (work / "report.json").write_text(json.dumps(report, indent=2) + "\n")
        print("PASS aggregate full-request bytes decreased; scripted fixtures only, no live cost or latency claim")
    finally:
        provider.shutdown()


if __name__ == "__main__":
    if sys.argv[1] == "--mcp":
        mcp_main(sys.argv[2])
    elif sys.argv[1] == "--hook":
        hook_main(sys.argv[2])
    else:
        main(str(Path(sys.argv[1]).resolve()), sys.argv[2])
