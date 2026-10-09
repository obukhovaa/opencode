package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/agent/mcpauthctx"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// fakeMCPFactory hands the pool a fresh fake client per connect, built by
// make(n) for the n-th connect, and keeps them for inspection.
type fakeMCPFactory struct {
	make func(n int) *fakeMCPClient

	mu      sync.Mutex
	clients []*fakeMCPClient
}

func (f *fakeMCPFactory) newClient(context.Context, string, config.MCPServer, map[string]string) (MCPClient, *mcpProc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.make(len(f.clients) + 1)
	f.clients = append(f.clients, c)
	return c, nil, nil
}

func (f *fakeMCPFactory) dials() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.clients)
}

func (f *fakeMCPFactory) client(i int) *fakeMCPClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clients[i]
}

func okFactory() *fakeMCPFactory {
	return &fakeMCPFactory{make: func(int) *fakeMCPClient { return &fakeMCPClient{result: textResult("ok")} }}
}

// newFakeMCPRegistry returns a registry whose pool connects through f, with
// server name seeded into the config as m. Cleanup cancels the registry's
// context first, so a connect still blocked in a fake handshake unwinds.
func newFakeMCPRegistry(t *testing.T, name string, m config.MCPServer, f *fakeMCPFactory) *mcpRegistry {
	t.Helper()
	seedMCPServers(t, map[string]config.MCPServer{name: m})
	ctx, cancel := context.WithCancel(context.Background())
	reg := NewMCPRegistry(ctx, nil, nil).(*mcpRegistry)
	reg.pool.newClient = f.newClient
	t.Cleanup(func() {
		// Shutdown first: it aborts connects still in progress and stops the
		// janitor, so nothing is left reading a budget a later cleanup
		// restores. Cancelling afterwards unwinds anything else.
		shutdownCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		reg.Shutdown(shutdownCtx)
		cancel()
	})
	return reg
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

var fakeServer = config.MCPServer{Type: config.MCPHttp, URL: "http://fake.invalid"}

func TestMCPPool_ReusesClientAcrossCalls(t *testing.T) {
	f := okFactory()
	reg := newFakeMCPRegistry(t, "srv", fakeServer, f)

	for i := range 3 {
		resp := reg.CallTool(context.Background(), "srv", "echo", "{}")
		if resp.IsError || resp.Content != "ok" {
			t.Fatalf("call %d: %+v", i, resp)
		}
	}
	if got := f.dials(); got != 1 {
		t.Fatalf("connects = %d, want 1", got)
	}
	c := f.client(0)
	if c.initCalls.Load() != 1 || c.callCalls.Load() != 3 {
		t.Errorf("initialize = %d, calls = %d; want 1 and 3", c.initCalls.Load(), c.callCalls.Load())
	}
	if c.closed.Load() {
		t.Error("a healthy pooled client was closed")
	}
}

func TestMCPPool_ConcurrentFirstUseConnectsOnce(t *testing.T) {
	f := &fakeMCPFactory{make: func(int) *fakeMCPClient {
		return &fakeMCPClient{result: textResult("ok"), initDelay: 50 * time.Millisecond}
	}}
	reg := newFakeMCPRegistry(t, "srv", fakeServer, f)

	var wg sync.WaitGroup
	var failed atomic.Int32
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if resp := reg.CallTool(context.Background(), "srv", "echo", "{}"); resp.IsError {
				failed.Add(1)
			}
		}()
	}
	wg.Wait()
	if failed.Load() != 0 {
		t.Fatalf("%d concurrent calls failed", failed.Load())
	}
	if got := f.dials(); got != 1 {
		t.Errorf("connects = %d, want 1", got)
	}
}

// A caller that stops waiting must not fail the connect other callers share —
// the rule the tools/list cache already follows.
func TestMCPPool_CallerGivingUpDoesNotPoisonConnect(t *testing.T) {
	f := &fakeMCPFactory{make: func(int) *fakeMCPClient {
		return &fakeMCPClient{result: textResult("ok"), initDelay: 100 * time.Millisecond}
	}}
	reg := newFakeMCPRegistry(t, "srv", fakeServer, f)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if resp := reg.CallTool(ctx, "srv", "echo", "{}"); !resp.IsError {
		t.Fatal("expected the impatient caller to get an error")
	}
	if resp := reg.CallTool(context.Background(), "srv", "echo", "{}"); resp.IsError {
		t.Fatalf("patient caller failed: %s", resp.Content)
	}
	if got := f.dials(); got != 1 {
		t.Errorf("connects = %d, want 1: the abandoned connect was not reused", got)
	}
}

func TestMCPPool_RetriesWhenRequestNotSent(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"session terminated (HTTP 404)", transport.NewError(fmt.Errorf("failed to send request: %w", transport.ErrSessionTerminated))},
		{"stdio server already exited", transport.NewError(fmt.Errorf("failed to write request: %w", &os.PathError{Op: "write", Path: "|1", Err: syscall.EPIPE}))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeMCPFactory{make: func(n int) *fakeMCPClient {
				if n == 1 {
					return &fakeMCPClient{callErr: tt.err}
				}
				return &fakeMCPClient{result: textResult("ok")}
			}}
			reg := newFakeMCPRegistry(t, "srv", fakeServer, f)

			resp := reg.CallTool(context.Background(), "srv", "echo", "{}")
			if resp.IsError || resp.Content != "ok" {
				t.Fatalf("retried call failed: %+v", resp)
			}
			if got := f.dials(); got != 2 {
				t.Fatalf("connects = %d, want 2 (one fresh client for the retry)", got)
			}
			if f.client(0).callCalls.Load() != 1 || f.client(1).callCalls.Load() != 1 {
				t.Error("each client should have seen exactly one call")
			}
			eventually(t, "the dead client to close", f.client(0).closed.Load)
		})
	}
}

func TestMCPPool_RetriesAtMostOnce(t *testing.T) {
	notSent := transport.NewError(fmt.Errorf("failed to send request: %w", transport.ErrSessionTerminated))
	f := &fakeMCPFactory{make: func(int) *fakeMCPClient { return &fakeMCPClient{callErr: notSent} }}
	reg := newFakeMCPRegistry(t, "srv", fakeServer, f)

	if resp := reg.CallTool(context.Background(), "srv", "echo", "{}"); !resp.IsError {
		t.Fatal("expected an error once the retry also failed")
	}
	if got := f.dials(); got != 2 {
		t.Errorf("connects = %d, want 2", got)
	}
}

// A transport failure after the request may have run evicts the client but
// is not retried: the tool must stay at-most-once.
func TestMCPPool_EvictsWithoutRetryOnOtherTransportErrors(t *testing.T) {
	f := &fakeMCPFactory{make: func(n int) *fakeMCPClient {
		if n == 1 {
			return &fakeMCPClient{callErr: transport.NewError(errors.New("connection reset by peer"))}
		}
		return &fakeMCPClient{result: textResult("ok")}
	}}
	reg := newFakeMCPRegistry(t, "srv", fakeServer, f)

	resp := reg.CallTool(context.Background(), "srv", "echo", "{}")
	if !resp.IsError || !strings.Contains(resp.Content, "connection reset") {
		t.Fatalf("expected the transport error, got %+v", resp)
	}
	if got := f.dials(); got != 1 {
		t.Fatalf("connects = %d after the failure, want 1 (no retry)", got)
	}
	eventually(t, "the broken client to close", f.client(0).closed.Load)

	if resp := reg.CallTool(context.Background(), "srv", "echo", "{}"); resp.IsError {
		t.Fatalf("next call failed: %s", resp.Content)
	}
	if got := f.dials(); got != 2 {
		t.Errorf("connects = %d, want 2 (the broken client was replaced)", got)
	}
}

// A JSON-RPC error with a standard code comes from a healthy session; one
// with an implementation-defined code is how a remote server rejects a session
// it no longer knows (mcp-go returns the 4xx body as an ordinary error answer).
func TestMCPPool_ServerErrors(t *testing.T) {
	stdioServer := config.MCPServer{Type: config.MCPStdio, Command: "fake"}
	tests := []struct {
		name      string
		server    config.MCPServer
		err       error
		wantDials int
	}{
		{"tool failure keeps a remote client", fakeServer, fmt.Errorf("%w: tool exploded", mcp.ErrInternalError), 1},
		{"invalid params keep a remote client", fakeServer, mcp.ErrInvalidParams, 1},
		{"unknown session evicts a remote client", fakeServer, errors.New("Bad Request: No valid session ID provided"), 2},
		{"a stdio client survives any server error", stdioServer, errors.New("Bad Request: No valid session ID provided"), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeMCPFactory{make: func(int) *fakeMCPClient { return &fakeMCPClient{callErr: tt.err} }}
			reg := newFakeMCPRegistry(t, "srv", tt.server, f)
			for range 2 {
				if resp := reg.CallTool(context.Background(), "srv", "echo", "{}"); !resp.IsError {
					t.Fatal("expected the server's error")
				}
			}
			if got := f.dials(); got != tt.wantDials {
				t.Errorf("connects = %d, want %d", got, tt.wantDials)
			}
		})
	}
}

// Before the pool, an abandoned call's per-call client was closed, which
// stopped a stdio server working on it; a stdio client is evicted instead.
func TestMCPPool_CancelledCallEvictsOnlyStdio(t *testing.T) {
	tests := []struct {
		name      string
		server    config.MCPServer
		wantDials int
	}{
		{"stdio", config.MCPServer{Type: config.MCPStdio, Command: "fake"}, 2},
		{"http", fakeServer, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeMCPFactory{make: func(n int) *fakeMCPClient {
				if n == 1 {
					return &fakeMCPClient{blockCallTool: true}
				}
				return &fakeMCPClient{result: textResult("ok")}
			}}
			reg := newFakeMCPRegistry(t, "srv", tt.server, f)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if resp := reg.CallTool(ctx, "srv", "hang", "{}"); !resp.IsError {
				t.Fatal("expected the abandoned call to fail")
			}
			// Bounded: on HTTP this reuses the same, still-hanging client.
			next, cancelNext := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancelNext()
			reg.CallTool(next, "srv", "echo", "{}")
			if got := f.dials(); got != tt.wantDials {
				t.Errorf("connects = %d, want %d", got, tt.wantDials)
			}
		})
	}
}

// A transport whose start ignores the handshake budget (SSE's GET runs under
// the client's lifetime) is still cut off by it.
func TestMCPPool_HangingStartIsBounded(t *testing.T) {
	const budget = 100 * time.Millisecond
	withInitTimeout(t, budget)
	seedMCPServers(t, map[string]config.MCPServer{"srv": fakeServer})
	reg := NewMCPRegistry(context.Background(), nil, nil).(*mcpRegistry)
	t.Cleanup(func() { reg.Shutdown(context.Background()) })
	reg.pool.newClient = func(lifetime context.Context, _ string, _ config.MCPServer, _ map[string]string) (MCPClient, *mcpProc, error) {
		<-lifetime.Done()
		return nil, nil, lifetime.Err()
	}

	start := time.Now()
	resp := reg.CallTool(context.Background(), "srv", "echo", "{}")
	if elapsed := time.Since(start); elapsed > 20*budget {
		t.Fatalf("connect took %s against a %s budget", elapsed, budget)
	}
	if !resp.IsError || !strings.Contains(resp.Content, "handshake") {
		t.Errorf("want the handshake error, got %+v", resp)
	}
}

func TestMCPRegistry_DiscoveryRecoversFromAStaleClient(t *testing.T) {
	stale := transport.NewError(fmt.Errorf("failed to send request: %w", transport.ErrSessionTerminated))
	f := &fakeMCPFactory{make: func(n int) *fakeMCPClient {
		if n == 1 {
			return &fakeMCPClient{listErr: stale}
		}
		return &fakeMCPClient{tools: []mcp.Tool{mcp.NewTool("echo")}}
	}}
	reg := newFakeMCPRegistry(t, "srv", fakeServer, f)
	// Warm the pool, as a tool call would have, before the server drops it.
	conn, err := reg.pool.acquire(context.Background(), "srv", fakeServer)
	if err != nil {
		t.Fatal(err)
	}
	reg.pool.release(conn, false)

	if got := len(drainLoadTools(t, reg.LoadTools(nil))); got != 1 {
		t.Fatalf("discovered %d tools, want 1 after retrying on a fresh client", got)
	}
	if got := f.dials(); got != 2 {
		t.Errorf("connects = %d, want 2", got)
	}
}

func TestMCPRegistry_DiscoveryTimeoutEvicts(t *testing.T) {
	withInitTimeout(t, 100*time.Millisecond)
	f := &fakeMCPFactory{make: func(n int) *fakeMCPClient {
		if n == 1 {
			return &fakeMCPClient{blockListTools: true}
		}
		return &fakeMCPClient{tools: []mcp.Tool{mcp.NewTool("echo")}}
	}}
	reg := newFakeMCPRegistry(t, "srv", fakeServer, f)

	if got := len(drainLoadTools(t, reg.LoadTools(nil))); got != 0 {
		t.Fatalf("discovered %d tools from a wedged server, want 0", got)
	}
	eventually(t, "the wedged client to close", f.client(0).closed.Load)
	if got := len(drainLoadTools(t, reg.LoadTools(nil))); got != 1 {
		t.Fatalf("discovered %d tools on the next fetch, want 1", got)
	}
}

func TestMCPPool_RetiredClientFinishesInFlightCall(t *testing.T) {
	f := okFactory()
	reg := newFakeMCPRegistry(t, "srv", fakeServer, f)
	p := reg.pool
	ctx := context.Background()

	inFlight, err := p.acquire(ctx, "srv", fakeServer)
	if err != nil {
		t.Fatal(err)
	}
	failing, err := p.acquire(ctx, "srv", fakeServer)
	if err != nil {
		t.Fatal(err)
	}
	p.release(failing, true)
	time.Sleep(20 * time.Millisecond)
	if f.client(0).closed.Load() {
		t.Fatal("retired client closed while a call was still running on it")
	}

	fresh, err := p.acquire(ctx, "srv", fakeServer)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.dials(); got != 2 {
		t.Fatalf("connects = %d, want 2: a retired client was handed out again", got)
	}
	p.release(inFlight, false)
	eventually(t, "the retired client to close after its last call", f.client(0).closed.Load)
	p.release(fresh, false)
	if f.client(1).closed.Load() {
		t.Error("the fresh client was closed")
	}
}

func TestMCPPool_IdleClientsAreClosed(t *testing.T) {
	t.Run("default timeout", func(t *testing.T) {
		f := okFactory()
		reg := newFakeMCPRegistry(t, "srv", fakeServer, f)
		reg.CallTool(context.Background(), "srv", "echo", "{}")

		reg.pool.sweep(time.Now())
		time.Sleep(20 * time.Millisecond)
		if f.client(0).closed.Load() {
			t.Fatal("a just-used client was closed")
		}
		reg.pool.sweep(time.Now().Add(mcpClientIdleTimeout + time.Second))
		eventually(t, "the idle client to close", f.client(0).closed.Load)

		reg.CallTool(context.Background(), "srv", "echo", "{}")
		if got := f.dials(); got != 2 {
			t.Errorf("connects = %d, want 2 after the idle close", got)
		}
	})

	t.Run("per-server timeout", func(t *testing.T) {
		f := okFactory()
		m := fakeServer
		m.ClientIdleTimeoutSeconds = 1
		reg := newFakeMCPRegistry(t, "srv", m, f)
		reg.CallTool(context.Background(), "srv", "echo", "{}")

		reg.pool.sweep(time.Now().Add(2 * time.Second))
		eventually(t, "the idle client to close", f.client(0).closed.Load)
	})

	t.Run("a client in use is never swept", func(t *testing.T) {
		f := okFactory()
		reg := newFakeMCPRegistry(t, "srv", fakeServer, f)
		conn, err := reg.pool.acquire(context.Background(), "srv", fakeServer)
		if err != nil {
			t.Fatal(err)
		}
		reg.pool.sweep(time.Now().Add(24 * time.Hour))
		time.Sleep(20 * time.Millisecond)
		if f.client(0).closed.Load() {
			t.Error("sweep closed a client with a call in progress")
		}
		reg.pool.release(conn, false)
	})
}

func TestMCPPool_ReuseDisabled(t *testing.T) {
	blocked := make(chan struct{})
	f := &fakeMCPFactory{make: func(int) *fakeMCPClient {
		// A close that never finishes: the result must not wait for it.
		return &fakeMCPClient{result: textResult("ok"), blockClose: blocked}
	}}
	m := fakeServer
	m.ClientIdleTimeoutSeconds = -1
	reg := newFakeMCPRegistry(t, "srv", m, f)
	// Registered after the registry, so it runs first: Shutdown would
	// otherwise wait out its budget on the blocked closes.
	t.Cleanup(func() { close(blocked) })

	for i := range 2 {
		start := time.Now()
		if resp := reg.CallTool(context.Background(), "srv", "echo", "{}"); resp.IsError {
			t.Fatalf("call %d: %s", i, resp.Content)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("call %d took %s: the result waited for the close", i, elapsed)
		}
	}
	if got := f.dials(); got != 2 {
		t.Errorf("connects = %d, want one per call", got)
	}
}

func TestMCPPool_Shutdown(t *testing.T) {
	f := okFactory()
	seedMCPServers(t, map[string]config.MCPServer{"idle": fakeServer, "busy": fakeServer})
	reg := NewMCPRegistry(context.Background(), nil, nil).(*mcpRegistry)
	reg.pool.newClient = f.newClient
	ctx := context.Background()

	reg.CallTool(ctx, "idle", "echo", "{}")
	busy, err := reg.pool.acquire(ctx, "busy", fakeServer)
	if err != nil {
		t.Fatal(err)
	}
	idleClient, busyClient := f.client(0), f.client(1)

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	reg.Shutdown(shutdownCtx)
	if !idleClient.closed.Load() {
		t.Error("Shutdown returned before the idle client closed")
	}
	if busyClient.closed.Load() {
		t.Error("Shutdown closed a client with a call in progress")
	}
	if resp := reg.CallTool(ctx, "idle", "echo", "{}"); !resp.IsError {
		t.Error("a call after Shutdown succeeded")
	}
	reg.pool.release(busy, false)
	eventually(t, "the busy client to close once released", busyClient.closed.Load)
}

func TestMCPPool_RegistryContextClosesPool(t *testing.T) {
	f := okFactory()
	seedMCPServers(t, map[string]config.MCPServer{"srv": fakeServer})
	ctx, cancel := context.WithCancel(context.Background())
	reg := NewMCPRegistry(ctx, nil, nil).(*mcpRegistry)
	reg.pool.newClient = f.newClient

	reg.CallTool(context.Background(), "srv", "echo", "{}")
	cancel()
	eventually(t, "the pooled client to close after the registry context ended", f.client(0).closed.Load)
	if resp := reg.CallTool(context.Background(), "srv", "echo", "{}"); !resp.IsError {
		t.Error("a call after the registry context ended succeeded")
	}
}

func TestMCPConnKey(t *testing.T) {
	http := config.MCPServer{Type: config.MCPHttp, URL: "https://mcp.example"}
	stdio := config.MCPServer{Type: config.MCPStdio, Command: "srv", Args: []string{"--stdio"}}
	a := map[string]string{"Authorization": "Bearer A"}
	b := map[string]string{"Authorization": "Bearer B"}

	if mcpConnKey("s", http, a) != mcpConnKey("s", http, map[string]string{"authorization": "Bearer A"}) {
		t.Error("header-name case changed the identity of the same request")
	}
	if mcpConnKey("s", http, a) == mcpConnKey("s", http, b) {
		t.Error("different Authorization values share an HTTP client")
	}
	if mcpConnKey("s", stdio, a) != mcpConnKey("s", stdio, b) {
		t.Error("headers split stdio clients, but a stdio server never sees them")
	}
	other := stdio
	other.Args = []string{"--other"}
	if mcpConnKey("s", stdio, nil) == mcpConnKey("s", other, nil) {
		t.Error("a changed launch configuration reused the old client")
	}
	if strings.Contains(mcpConnKey("s", http, a), "Bearer") {
		t.Error("the key holds a credential in clear text")
	}
}

// mcpMethodServer is an in-process streamable-HTTP MCP server with an "echo"
// tool that records each request's JSON-RPC method and Authorization header.
// Setting drop404 makes the next tools/call answer 404, as a server does for
// a session it no longer knows.
type mcpMethodServer struct {
	url     string
	mu      sync.Mutex
	methods []string
	auth    []string
	peers   []string
	drop404 atomic.Bool
}

func newMCPMethodServer(t *testing.T) *mcpMethodServer {
	t.Helper()
	mcpSrv := server.NewMCPServer("method-test", "0.0.1")
	mcpSrv.AddTool(mcp.NewTool("echo", mcp.WithDescription("echoes")), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return textResult("ok"), nil
	})
	h := server.NewStreamableHTTPServer(mcpSrv)
	s := &mcpMethodServer{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.Method
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var msg struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(body, &msg)
			method = msg.Method
		}
		s.mu.Lock()
		s.methods = append(s.methods, method)
		if method == "tools/call" {
			s.auth = append(s.auth, r.Header.Get("Authorization"))
			s.peers = append(s.peers, r.Header.Get("X-Peer-Id"))
		}
		s.mu.Unlock()
		if method == "tools/call" && s.drop404.CompareAndSwap(true, false) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	s.url = ts.URL
	return s
}

func (s *mcpMethodServer) count(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.methods {
		if m == method {
			n++
		}
	}
	return n
}

func (s *mcpMethodServer) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.methods)
}

func newHTTPPoolRegistry(t *testing.T, name, url string) *mcpRegistry {
	t.Helper()
	seedMCPServerConfig(t, name, url)
	reg := NewMCPRegistry(context.Background(), nil, nil).(*mcpRegistry)
	t.Cleanup(func() { reg.Shutdown(context.Background()) })
	return reg
}

func TestMCPRegistry_CallToolReusesHTTPSession(t *testing.T) {
	srv := newMCPMethodServer(t)
	reg := newHTTPPoolRegistry(t, "web", srv.url)

	if resp := reg.CallTool(context.Background(), "web", "echo", "{}"); resp.IsError {
		t.Fatalf("first call: %s", resp.Content)
	}
	before := srv.total()
	if resp := reg.CallTool(context.Background(), "web", "echo", "{}"); resp.IsError {
		t.Fatalf("second call: %s", resp.Content)
	}
	if got := srv.total() - before; got != 1 {
		t.Errorf("a warm call sent %d requests, want 1 (the tools/call)", got)
	}
	if got := srv.count("initialize"); got != 1 {
		t.Errorf("initialize sent %d times, want 1", got)
	}
}

func TestMCPRegistry_RetriesAfterSessionTerminated(t *testing.T) {
	srv := newMCPMethodServer(t)
	reg := newHTTPPoolRegistry(t, "web", srv.url)

	if resp := reg.CallTool(context.Background(), "web", "echo", "{}"); resp.IsError {
		t.Fatalf("first call: %s", resp.Content)
	}
	srv.drop404.Store(true)
	if resp := reg.CallTool(context.Background(), "web", "echo", "{}"); resp.IsError {
		t.Fatalf("call after the session was dropped failed: %s", resp.Content)
	}
	if got := srv.count("initialize"); got != 2 {
		t.Errorf("initialize sent %d times, want 2 (one re-initialize)", got)
	}
	if got := srv.count("tools/call"); got != 3 {
		t.Errorf("tools/call sent %d times, want 3 (ok, 404, retried)", got)
	}
}

func TestMCPRegistry_DiscoveryWarmsTheFirstCall(t *testing.T) {
	srv := newMCPMethodServer(t)
	reg := newHTTPPoolRegistry(t, "web", srv.url)

	if got := len(drainLoadTools(t, reg.LoadTools(nil))); got != 1 {
		t.Fatalf("discovered %d tools, want 1", got)
	}
	if resp := reg.CallTool(context.Background(), "web", "echo", "{}"); resp.IsError {
		t.Fatalf("call: %s", resp.Content)
	}
	if got := srv.count("initialize"); got != 1 {
		t.Errorf("initialize sent %d times, want 1: the call did not reuse the discovery client", got)
	}
}

func TestMCPRegistry_IdentitiesDoNotShareASession(t *testing.T) {
	srv := newMCPMethodServer(t)
	reg := newHTTPPoolRegistry(t, "web", srv.url)
	asT1 := mcpauthctx.WithAuthOverride(context.Background(), "web", "Bearer T1")
	asT2 := mcpauthctx.WithAuthOverride(context.Background(), "web", "Bearer T2")

	for _, ctx := range []context.Context{asT1, asT2, asT1} {
		if resp := reg.CallTool(ctx, "web", "echo", "{}"); resp.IsError {
			t.Fatalf("call: %s", resp.Content)
		}
	}
	if got := srv.count("initialize"); got != 2 {
		t.Errorf("initialize sent %d times, want 2 (one session per identity)", got)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	want := []string{"Bearer T1", "Bearer T2", "Bearer T1"}
	if strings.Join(srv.auth, ",") != strings.Join(want, ",") {
		t.Errorf("tools/call Authorization = %v, want %v", srv.auth, want)
	}
}

func TestMCPRegistry_PeersDoNotShareASession(t *testing.T) {
	srv := newMCPMethodServer(t)
	seedMCPServers(t, map[string]config.MCPServer{"web": {Type: config.MCPHttp, URL: srv.url, PeerHeader: "X-Peer-Id"}})
	reg := NewMCPRegistry(context.Background(), nil, nil).(*mcpRegistry)
	t.Cleanup(func() { reg.Shutdown(context.Background()) })
	peer := func(id string) context.Context {
		return tools.WithPeer(context.Background(), tools.Peer{Channel: "external", Identity: "default", PeerID: id})
	}

	for _, ctx := range []context.Context{peer("t1:c1"), peer("t2:c1"), peer("t1:c1")} {
		if resp := reg.CallTool(ctx, "web", "echo", "{}"); resp.IsError {
			t.Fatalf("call: %s", resp.Content)
		}
	}
	if got := srv.count("initialize"); got != 2 {
		t.Errorf("initialize sent %d times, want 2 (one session per peer)", got)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if got := strings.Join(srv.peers, ","); got != "t1:c1,t2:c1,t1:c1" {
		t.Errorf("tools/call X-Peer-Id = %s", got)
	}
}

// Discovery under a run's published token warms the client for that run's
// calls, and only for them.
func TestMCPRegistry_DiscoveryAuthClientServesMatchingCalls(t *testing.T) {
	srv := newMCPMethodServer(t)
	reg := newHTTPPoolRegistry(t, "web", srv.url)
	reg.SetDiscoveryAuth(map[string]string{"web": "Bearer T1"})

	if got := len(drainLoadTools(t, reg.LoadTools(nil))); got != 1 {
		t.Fatalf("discovered %d tools, want 1", got)
	}
	asT1 := mcpauthctx.WithAuthOverride(context.Background(), "web", "Bearer T1")
	reg.CallTool(asT1, "web", "echo", "{}")
	if got := srv.count("initialize"); got != 1 {
		t.Errorf("initialize sent %d times, want 1: the run's call did not reuse its discovery client", got)
	}
	asT2 := mcpauthctx.WithAuthOverride(context.Background(), "web", "Bearer T2")
	reg.CallTool(asT2, "web", "echo", "{}")
	if got := srv.count("initialize"); got != 2 {
		t.Errorf("initialize sent %d times, want 2: another run reused T1's client", got)
	}
}

// SSE is never pooled, and a call keeps its event stream for as long as it
// needs it (the stream used to die with a 20s start context).
func TestMCPRegistry_SSECallsWork(t *testing.T) {
	mcpSrv := server.NewMCPServer("sse-test", "0.0.1")
	mcpSrv.AddTool(mcp.NewTool("echo"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return textResult("ok"), nil
	})
	ts := server.NewTestServer(mcpSrv)
	t.Cleanup(ts.Close)
	seedMCPServers(t, map[string]config.MCPServer{"events": {Type: config.MCPSse, URL: ts.URL + "/sse"}})
	reg := NewMCPRegistry(context.Background(), nil, nil).(*mcpRegistry)
	t.Cleanup(func() { reg.Shutdown(context.Background()) })

	for i := range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		resp := reg.CallTool(ctx, "events", "echo", "{}")
		cancel()
		if resp.IsError || resp.Content != "ok" {
			t.Fatalf("SSE call %d: %+v", i, resp)
		}
	}
	reg.pool.mu.Lock()
	pooled := len(reg.pool.conns)
	reg.pool.mu.Unlock()
	if pooled != 0 {
		t.Errorf("%d SSE clients pooled, want 0", pooled)
	}
}
