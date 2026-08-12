package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

func TestResolveCallToolMaxOutputBytes(t *testing.T) {
	tests := []struct {
		name string
		cfg  int
		want int
	}{
		{"unset uses default", 0, mcpCallToolMaxOutputBytes},
		{"positive overrides", 4096, 4096},
		{"negative disables (unlimited)", -5, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveCallToolMaxOutputBytes(config.MCPServer{CallToolMaxOutputBytes: tt.cfg})
			if got != tt.want {
				t.Errorf("resolveCallToolMaxOutputBytes(%d) = %d, want %d", tt.cfg, got, tt.want)
			}
		})
	}
}

type fakeMCPClient struct {
	result *mcp.CallToolResult
}

func (f *fakeMCPClient) Initialize(ctx context.Context, req mcp.InitializeRequest) (*mcp.InitializeResult, error) {
	return &mcp.InitializeResult{}, nil
}
func (f *fakeMCPClient) ListTools(ctx context.Context, req mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{}, nil
}
func (f *fakeMCPClient) CallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return f.result, nil
}
func (f *fakeMCPClient) Close() error { return nil }

func textResult(blocks ...string) *mcp.CallToolResult {
	r := &mcp.CallToolResult{}
	for _, b := range blocks {
		r.Content = append(r.Content, mcp.TextContent{Type: "text", Text: b})
	}
	return r
}

func TestRunToolOutputCap(t *testing.T) {
	t.Cleanup(tools.CleanupTempDir)
	ctx := context.Background()

	t.Run("small output is returned unchanged", func(t *testing.T) {
		c := &fakeMCPClient{result: textResult("hello world")}
		resp, err := runTool(ctx, c, "some_tool", "{}", mcpCallToolTimeout, mcpCallToolMaxOutputBytes)
		if err != nil {
			t.Fatalf("runTool error: %v", err)
		}
		if resp.Content != "hello world" {
			t.Errorf("small output altered: %q", resp.Content)
		}
	})

	t.Run("oversized output is capped and spilled to a file", func(t *testing.T) {
		big := strings.Repeat("X", 200_000) // ~200KB, over the 50KB default
		c := &fakeMCPClient{result: textResult(big)}
		resp, err := runTool(ctx, c, "big_tool", "{}", mcpCallToolTimeout, mcpCallToolMaxOutputBytes)
		if err != nil {
			t.Fatalf("runTool error: %v", err)
		}
		if len(resp.Content) >= len(big) {
			t.Errorf("output not capped: %d bytes (input %d)", len(resp.Content), len(big))
		}
		if !strings.Contains(resp.Content, "output truncated") || !strings.Contains(resp.Content, "Full output saved to:") {
			t.Errorf("capped output missing overflow header; got:\n%s", resp.Content[:min(300, len(resp.Content))])
		}
		if resp.IsError {
			t.Error("capping should not mark the response as an error")
		}
	})

	t.Run("multi-block output is concatenated before capping", func(t *testing.T) {
		// With the cap disabled, both blocks must survive (regression guard for the
		// old loop that kept only the last block).
		c := &fakeMCPClient{result: textResult("AAAA", "BBBB")}
		resp, err := runTool(ctx, c, "multi_tool", "{}", mcpCallToolTimeout, -1)
		if err != nil {
			t.Fatalf("runTool error: %v", err)
		}
		if resp.Content != "AAAABBBB" {
			t.Errorf("multi-block not concatenated: got %q, want %q", resp.Content, "AAAABBBB")
		}
	})
}

// newTestMCPHTTPServer starts an in-process streamable-HTTP MCP server with a
// single "echo" tool and returns its base URL plus a counter of MCP HTTP
// requests received (a registry cache hit performs zero requests).
func newTestMCPHTTPServer(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	mcpSrv := server.NewMCPServer("registry-test", "0.0.1")
	mcpSrv.AddTool(mcp.NewTool("echo", mcp.WithDescription("echoes")), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return textResult("ok"), nil
	})
	h := server.NewStreamableHTTPServer(mcpSrv)
	requests := &atomic.Int64{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts.URL, requests
}

// seedMCPServerConfig points the global config's MCPServers at a single test
// server and restores the previous value on cleanup.
func seedMCPServerConfig(t *testing.T, name, url string) {
	t.Helper()
	if config.Get() == nil {
		if _, err := config.Load(t.TempDir(), false); err != nil {
			t.Fatalf("config.Load: %v", err)
		}
	}
	cfg := config.Get()
	old := cfg.MCPServers
	cfg.MCPServers = map[string]config.MCPServer{
		name: {Type: config.MCPHttp, URL: url},
	}
	t.Cleanup(func() { cfg.MCPServers = old })
}

func drainLoadTools(t *testing.T, ch <-chan tools.BaseTool) []tools.BaseTool {
	t.Helper()
	var got []tools.BaseTool
	deadline := time.After(15 * time.Second)
	for {
		select {
		case tl, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, tl)
		case <-deadline:
			// t.Error, not t.Fatal: this helper runs inside spawned goroutines
			// (see the concurrent-load test), where FailNow would only kill that
			// goroutine and deadlock the test-goroutine receive on its result.
			t.Error("LoadTools channel did not close in time")
			return got
		}
	}
}

// TestMCPRegistry_LoadToolsRegistryOwnedLifetime is the regression guard for
// the async-task toolset loss: subagent toolsets used to be fetched under the
// spawning tool call's context, which the parent's parallel-tool batch cancels
// as soon as the async ack returns — with a cold cache the subagent resolved
// zero MCP tools. Fetches now run under the registry's own context, so a
// live registry must deliver tools no matter how short-lived the creator was.
func TestMCPRegistry_LoadToolsRegistryOwnedLifetime(t *testing.T) {
	url, _ := newTestMCPHTTPServer(t)
	seedMCPServerConfig(t, "ctxtest", url)

	reg := NewMCPRegistry(context.Background(), nil, nil)

	got := drainLoadTools(t, reg.LoadTools(nil))
	if len(got) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(got))
	}
	if name := got[0].Info().Name; name != "ctxtest_echo" {
		t.Errorf("tool name = %q, want %q", name, "ctxtest_echo")
	}
	if loaded := reg.LoadedServers(); !loaded["ctxtest"] {
		t.Errorf("LoadedServers() = %v, want ctxtest loaded", loaded)
	}
}

// TestMCPRegistry_ConcurrentLoadSharesSingleFetch verifies the singleflight
// cache: concurrent LoadTools callers all receive the tools, later callers hit
// the cache, and the MCP server sees only the initial fetch's requests.
func TestMCPRegistry_ConcurrentLoadSharesSingleFetch(t *testing.T) {
	url, requests := newTestMCPHTTPServer(t)
	seedMCPServerConfig(t, "shared", url)

	reg := NewMCPRegistry(context.Background(), nil, nil)

	const callers = 5
	results := make(chan int, callers)
	for range callers {
		go func() {
			results <- len(drainLoadTools(t, reg.LoadTools(nil)))
		}()
	}
	for range callers {
		if n := <-results; n != 1 {
			t.Fatalf("concurrent caller got %d tools, want 1", n)
		}
	}

	fetched := requests.Load()
	if fetched == 0 {
		t.Fatal("expected the fetch to hit the test server")
	}
	if got := len(drainLoadTools(t, reg.LoadTools(nil))); got != 1 {
		t.Fatalf("cache-hit caller got %d tools, want 1", got)
	}
	if after := requests.Load(); after != fetched {
		t.Errorf("cache hit performed %d extra HTTP requests, want 0", after-fetched)
	}
}

// TestMCPRegistry_ShutdownBoundsFetch verifies the one context that SHOULD
// stop a fetch — the registry's own (app shutdown) — does so promptly and
// gracefully: the channel closes with no tools instead of hanging.
func TestMCPRegistry_ShutdownBoundsFetch(t *testing.T) {
	url, _ := newTestMCPHTTPServer(t)
	seedMCPServerConfig(t, "shutdown", url)

	appCtx, cancel := context.WithCancel(context.Background())
	reg := NewMCPRegistry(appCtx, nil, nil)
	cancel() // app is shutting down before the load starts

	done := make(chan []tools.BaseTool, 1)
	go func() { done <- drainLoadTools(t, reg.LoadTools(nil)) }()
	select {
	case got := <-done:
		if len(got) != 0 {
			t.Errorf("expected no tools after shutdown, got %d", len(got))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("LoadTools did not terminate after registry shutdown")
	}
}
