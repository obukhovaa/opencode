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
	// blockInitialize / blockCallTool make the respective method hang until its
	// ctx is done, standing in for a server that accepts a request and never
	// answers. Zero value = respond immediately, so existing tests are unaffected.
	blockInitialize bool
	blockCallTool   bool
	// callToolDelay delays CallTool without ignoring ctx.
	callToolDelay time.Duration
	closed        atomic.Bool
}

func (f *fakeMCPClient) Initialize(ctx context.Context, req mcp.InitializeRequest) (*mcp.InitializeResult, error) {
	if f.blockInitialize {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &mcp.InitializeResult{}, nil
}
func (f *fakeMCPClient) ListTools(ctx context.Context, req mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{}, nil
}
func (f *fakeMCPClient) CallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if f.blockCallTool {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.callToolDelay > 0 {
		select {
		case <-time.After(f.callToolDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.result, nil
}
func (f *fakeMCPClient) Close() error { f.closed.Store(true); return nil }

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

// withInitTimeout shortens the package-level handshake budget for one test so
// the bound can be exercised without a 30s wait.
func withInitTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := mcpInitTimeout
	mcpInitTimeout = d
	t.Cleanup(func() { mcpInitTimeout = prev })
}

// TestRunToolHandshakeBound covers the deadline on the per-call MCP handshake.
// Before it existed, a server that started but never answered initialize parked
// the caller for the life of the process: the tool part stayed at
// status=running with no start timestamp, and the callTimeout below never
// applied because CallTool was never reached.
func TestRunToolHandshakeBound(t *testing.T) {
	t.Cleanup(tools.CleanupTempDir)

	t.Run("handshake that never answers fails within the budget", func(t *testing.T) {
		withInitTimeout(t, 50*time.Millisecond)
		c := &fakeMCPClient{blockInitialize: true}

		start := time.Now()
		resp, err := runTool(context.Background(), c, "wedged_tool", "{}", mcpCallToolTimeout, mcpCallToolMaxOutputBytes)
		elapsed := time.Since(start)

		if err != nil {
			t.Fatalf("runTool returned a Go error, want a tool error: %v", err)
		}
		if elapsed > 5*time.Second {
			t.Fatalf("runTool blocked for %s; the handshake bound did not apply", elapsed)
		}
		if !resp.IsError {
			t.Error("wedged handshake should produce an error response")
		}
		for _, want := range []string{"wedged_tool", "handshake", mcpInitTimeout.String()} {
			if !strings.Contains(resp.Content, want) {
				t.Errorf("response missing %q; got: %s", want, resp.Content)
			}
		}
	})

	t.Run("upstream cancellation is not blamed on the handshake budget", func(t *testing.T) {
		// A long budget so only the parent ctx can end the call.
		withInitTimeout(t, time.Hour)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		resp, err := runTool(ctx, &fakeMCPClient{blockInitialize: true}, "cancelled_tool", "{}", mcpCallToolTimeout, mcpCallToolMaxOutputBytes)
		if err != nil {
			t.Fatalf("runTool returned a Go error: %v", err)
		}
		if strings.Contains(resp.Content, "handshake") {
			t.Errorf("upstream cancellation attributed to the handshake budget: %s", resp.Content)
		}
	})

	t.Run("handshake budget does not curtail a slow tool call", func(t *testing.T) {
		// The tool takes longer than the handshake budget but stays inside its
		// own call budget: a deferred cancelInit would have killed this.
		withInitTimeout(t, 30*time.Millisecond)
		c := &fakeMCPClient{result: textResult("done"), callToolDelay: 150 * time.Millisecond}

		resp, err := runTool(context.Background(), c, "slow_tool", "{}", 10*time.Second, mcpCallToolMaxOutputBytes)
		if err != nil {
			t.Fatalf("runTool error: %v", err)
		}
		if resp.IsError {
			t.Fatalf("slow-but-within-budget call failed: %s", resp.Content)
		}
		if resp.Content != "done" {
			t.Errorf("content = %q, want %q", resp.Content, "done")
		}
	})

	t.Run("call budget still applies after a prompt handshake", func(t *testing.T) {
		withInitTimeout(t, time.Hour)
		c := &fakeMCPClient{blockCallTool: true}

		resp, err := runTool(context.Background(), c, "hung_call", "{}", 50*time.Millisecond, mcpCallToolMaxOutputBytes)
		if err != nil {
			t.Fatalf("runTool error: %v", err)
		}
		if !strings.Contains(resp.Content, "timed out") {
			t.Errorf("expected the CallTool timeout message, got: %s", resp.Content)
		}
	})
}

// TestAwaitCacheEntry covers the backstop on the shared MCP client-cache wait.
// The fetcher closes entry.done from a defer, which never runs while it is
// blocked in a call that ignores its ctx — and an open entry blocks EVERY
// waiter, including agents with no interest in that server.
func TestAwaitCacheEntry(t *testing.T) {
	t.Run("ready entry returns immediately", func(t *testing.T) {
		entry := &toolsCacheEntry{done: make(chan bool)}
		close(entry.done)
		if !awaitCacheEntry(context.Background(), "srv", entry, time.Hour) {
			t.Error("awaitCacheEntry = false for a closed entry, want true")
		}
	})

	t.Run("wedged fetcher releases the waiter at the backstop", func(t *testing.T) {
		entry := &toolsCacheEntry{done: make(chan bool)} // never closed

		start := time.Now()
		ok := awaitCacheEntry(context.Background(), "wedged", entry, 40*time.Millisecond)
		elapsed := time.Since(start)

		if ok {
			t.Error("awaitCacheEntry = true for a wedged fetcher, want false")
		}
		if elapsed > 5*time.Second {
			t.Fatalf("waiter blocked for %s; the backstop did not apply", elapsed)
		}
		select {
		case <-entry.done:
			t.Error("waiter closed the shared entry; the fetcher owns its lifecycle")
		default:
		}
	})

	t.Run("registry shutdown releases the waiter without waiting out the backstop", func(t *testing.T) {
		entry := &toolsCacheEntry{done: make(chan bool)}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		start := time.Now()
		if awaitCacheEntry(ctx, "srv", entry, time.Hour) {
			t.Error("awaitCacheEntry = true after shutdown, want false")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("shutdown waiter blocked for %s", elapsed)
		}
	})
}
