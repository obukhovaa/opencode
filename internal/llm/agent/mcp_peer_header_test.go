package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// The bridge peer of the calling turn reaches a server that declares a
// peerHeader, and only such a server; a static value under that name never
// survives.
func TestResolvePeerHeader(t *testing.T) {
	t.Parallel()
	peer := tools.Peer{Channel: "external", Identity: "default", PeerID: "app1:d1:c1"}
	withPeer := tools.WithPeer(context.Background(), peer)
	tests := []struct {
		name     string
		ctx      context.Context
		header   string
		static   map[string]string
		want     map[string]string
		wantSame bool
	}{
		{
			name: "no header configured", ctx: withPeer, header: "",
			static: map[string]string{"X-Env": "dev"}, want: map[string]string{"X-Env": "dev"}, wantSame: true,
		},
		{
			name: "no peer on the context", ctx: context.Background(), header: "X-Peer-Id",
			static: map[string]string{"X-Env": "dev"}, want: map[string]string{"X-Env": "dev"}, wantSame: true,
		},
		{
			name: "peer layered under the configured name", ctx: withPeer, header: "X-Peer-Id",
			static: map[string]string{"X-Env": "dev"}, want: map[string]string{"X-Env": "dev", "X-Peer-Id": "app1:d1:c1"},
		},
		{
			name: "a static value under any letter case is replaced", ctx: withPeer, header: "X-Peer-Id",
			static: map[string]string{"x-peer-id": "forged"}, want: map[string]string{"X-Peer-Id": "app1:d1:c1"},
		},
		{
			// net/http would refuse the value and fail the call.
			name:   "a peer id net/http cannot send is omitted, static value too",
			ctx:    tools.WithPeer(context.Background(), tools.Peer{Channel: "external", Identity: "default", PeerID: "app1:d1\r\nX-Evil: 1"}),
			header: "X-Peer-Id",
			static: map[string]string{"X-Env": "dev", "x-peer-id": "forged"}, want: map[string]string{"X-Env": "dev"},
		},
		{
			name:   "a tab is a valid header value",
			ctx:    tools.WithPeer(context.Background(), tools.Peer{Channel: "external", Identity: "default", PeerID: "app1\tc1"}),
			header: "X-Peer-Id",
			static: nil, want: map[string]string{"X-Peer-Id": "app1\tc1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			before := map[string]string{}
			for k, v := range tt.static {
				before[k] = v
			}
			got := resolvePeerHeader(tt.ctx, tt.header, tt.static)
			if len(got) != len(tt.want) {
				t.Fatalf("headers = %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("headers[%q] = %q, want %q", k, got[k], v)
				}
			}
			if len(tt.static) != len(before) {
				t.Fatalf("static map mutated: %v", tt.static)
			}
			for k, v := range before {
				if tt.static[k] != v {
					t.Errorf("static map mutated at %q: %q", k, tt.static[k])
				}
			}
			if tt.wantSame && len(tt.static) > 0 {
				got["probe"] = "x"
				_, shared := tt.static["probe"]
				delete(got, "probe")
				if !shared {
					t.Error("expected the static map itself back")
				}
			}
		})
	}
}

// End to end against a stub MCP server: a client started under a bridge
// turn's context sends the peer id in the configured header.
func TestPeerHeaderReachesTheServer(t *testing.T) {
	mcpSrv := server.NewMCPServer("peer-header-test", "0.0.1")
	h := server.NewStreamableHTTPServer(mcpSrv)
	var mu sync.Mutex
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("X-Peer-Id"))
		mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	if config.Get() == nil {
		if _, err := config.Load(t.TempDir(), false); err != nil {
			t.Fatalf("config.Load: %v", err)
		}
	}
	cfg := config.Get()
	old := cfg.MCPServers
	cfg.MCPServers = map[string]config.MCPServer{
		"scoped": {Type: config.MCPHttp, URL: ts.URL, PeerHeader: "X-Peer-Id"},
	}
	t.Cleanup(func() { cfg.MCPServers = old })

	reg := NewMCPRegistry(context.Background(), nil, nil)
	ctx := tools.WithPeer(context.Background(), tools.Peer{Channel: "external", Identity: "default", PeerID: "app1:d1:c1"})
	c, err := reg.StartClient(ctx, "scoped")
	if err != nil {
		t.Fatalf("StartClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || seen[0] != "app1:d1:c1" {
		t.Fatalf("server saw X-Peer-Id %q, want the peer id", seen)
	}
}
