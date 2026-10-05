package mcpserve

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/clitool"
)

func manifest(t *testing.T, name, body string) *clitool.Manifest {
	t.Helper()
	wd := t.TempDir()
	m, err := clitool.Parse([]byte(body), filepath.Join(wd, name+".yaml"), wd)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// rpc drives the stdio server over pipes with newline-delimited JSON-RPC.
type rpc struct {
	t   *testing.T
	in  io.Writer
	out *bufio.Reader
}

func (r *rpc) call(id int, method string, params any) map[string]any {
	r.t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if id > 0 {
		msg["id"] = id
	}
	raw, _ := json.Marshal(msg)
	if _, err := r.in.Write(append(raw, '\n')); err != nil {
		r.t.Fatal(err)
	}
	if id == 0 {
		return nil
	}
	line, err := r.out.ReadString('\n')
	if err != nil {
		r.t.Fatalf("read response to %s: %v", method, err)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		r.t.Fatalf("bad response %q: %v", line, err)
	}
	return resp
}

func TestServe_ListAndCall(t *testing.T) {
	say := manifest(t, "say", "name: say\ndescription: Echo wrapper.\ncommand: /bin/echo\nargs:\n  deny: [\"-x\"]\npermission:\n  \"*\": allow\n  \"secret *\": deny\n")
	srv := New([]*clitool.Manifest{say}, "test")

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srv, inR, outW, log.New(io.Discard, "", 0)) }()
	c := &rpc{t: t, in: inW, out: bufio.NewReader(outR)}

	init := c.call(1, "initialize", map[string]any{
		"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "test", "version": "0"},
	})
	if init["error"] != nil {
		t.Fatalf("initialize: %v", init["error"])
	}
	c.call(0, "notifications/initialized", map[string]any{})

	list := c.call(2, "tools/list", map[string]any{})
	result := list["result"].(map[string]any)
	toolsList := result["tools"].([]any)
	if len(toolsList) != 1 {
		t.Fatalf("tools/list = %v", toolsList)
	}
	tool := toolsList[0].(map[string]any)
	if tool["name"] != "say" || !strings.Contains(tool["description"].(string), "no shell") {
		t.Errorf("tool = %v", tool)
	}
	schema := tool["inputSchema"].(map[string]any)
	if props := schema["properties"].(map[string]any); props["args"] == nil {
		t.Errorf("schema = %v", schema)
	}

	callOK := c.call(3, "tools/call", map[string]any{"name": "say", "arguments": map[string]any{"args": []string{"hello", "world"}}})
	res := callOK["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if res["isError"] == true || !strings.HasPrefix(text, "hello world\nexit status 0") {
		t.Errorf("call = %v", res)
	}

	denied := c.call(4, "tools/call", map[string]any{"name": "say", "arguments": map[string]any{"args": []string{"sql", "-x"}}})
	res = denied["result"].(map[string]any)
	text = res["content"].([]any)[0].(map[string]any)["text"].(string)
	if res["isError"] != true || !strings.Contains(text, `deny pattern "-x"`) {
		t.Errorf("policy over MCP: %v", res)
	}

	permDenied := c.call(5, "tools/call", map[string]any{"name": "say", "arguments": map[string]any{"args": []string{"secret", "thing"}}})
	res = permDenied["result"].(map[string]any)
	text = res["content"].([]any)[0].(map[string]any)["text"].(string)
	if res["isError"] != true || !strings.Contains(text, "denied by the tool's default permission") {
		t.Errorf("manifest deny over MCP: %v", res)
	}

	inW.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
}

// writeScript creates an executable shell script; the script stands in for
// the wrapped binary, the server itself never uses a shell.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestServe_AppliesInheritedLimits: the limits a manifest inherits from the
// resolved cliTools / OPENCODE_CLI_TOOLS_* knobs bound a served call exactly
// as they bound the native tool — one output cap, one timeout, one place.
func TestServe_AppliesInheritedLimits(t *testing.T) {
	wd := t.TempDir()
	spew := writeScript(t, wd, "spew.sh", `i=0; while [ $i -lt 300 ]; do echo "line $i of a long report that keeps going"; i=$((i+1)); done`)
	nap := writeScript(t, wd, "nap.sh", `sleep 5`)
	d := clitool.Defaults{Timeout: time.Second, MaxTimeout: time.Second, MaxOutputBytes: 512}
	mk := func(name, cmd string) *clitool.Manifest {
		t.Helper()
		m, err := clitool.ParseWithDefaults([]byte("name: "+name+"\ndescription: d\ncommand: "+cmd+"\npermission: allow\n"),
			filepath.Join(wd, name+".yaml"), wd, d)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	srv := New([]*clitool.Manifest{mk("spew", spew), mk("nap", nap)}, "test")

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srv, inR, outW, log.New(io.Discard, "", 0)) }()
	c := &rpc{t: t, in: inW, out: bufio.NewReader(outR)}
	c.call(1, "initialize", map[string]any{
		"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "test", "version": "0"},
	})
	c.call(0, "notifications/initialized", map[string]any{})

	capped := c.call(2, "tools/call", map[string]any{"name": "spew", "arguments": map[string]any{"args": []string{}}})
	res := capped["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if res["isError"] == true || !strings.Contains(text, "<spew output truncated:") || !strings.Contains(text, "Full output saved to:") || len(text) > 2048 {
		t.Errorf("inherited output cap not applied over MCP: isError=%v len=%d\n%.300s", res["isError"], len(text), text)
	}

	start := time.Now()
	timedOut := c.call(3, "tools/call", map[string]any{"name": "nap", "arguments": map[string]any{"args": []string{}}})
	res = timedOut["result"].(map[string]any)
	text = res["content"].([]any)[0].(map[string]any)["text"].(string)
	if res["isError"] != true || !strings.Contains(text, "timed out after 1s") {
		t.Errorf("inherited timeout not applied over MCP: %v", res)
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("timed-out call took %s; the process group was not killed promptly", time.Since(start))
	}

	inW.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
}
