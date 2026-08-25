package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/agent/mcpauthctx"
)

// This file covers the discovery half of the per-flow MCP auth contract
// (openspec change agent-pod-pool-runtime, design D1).
//
// Tool CALLS take their Authorization override from the caller's context.
// Tool DISCOVERY (initialize + tools/list) cannot: it is deliberately
// context-free so a short-lived creator can't strand an agent without MCP
// tools, which means it runs under the registry's own baseCtx and never
// sees a run context. A server whose ONLY credential is the per-run token
// — the orchestrator MCP endpoint on a pool pod — therefore 401'd on
// initialize and contributed zero tools for the entire run, leaving the
// per-call override with nothing to apply to.

// newAuthGatedMCPServer serves the MCP protocol only to requests bearing
// wantAuth; everything else gets a 401, exactly like the orchestrator's
// RequireBearerToken-wrapped endpoint. Returns the URL and a counter of
// rejected requests.
func newAuthGatedMCPServer(t *testing.T, wantAuth string) (string, *atomic.Int64) {
	t.Helper()
	mcpSrv := server.NewMCPServer("auth-gated-test", "0.0.1")
	mcpSrv.AddTool(mcp.NewTool("echo", mcp.WithDescription("echoes")), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return textResult("ok"), nil
	})
	h := server.NewStreamableHTTPServer(mcpSrv)
	rejected := &atomic.Int64{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != wantAuth {
			rejected.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts.URL, rejected
}

// TestDiscoveryAuthReachesInitialize is the end-to-end regression guard:
// with the run's token published to the registry, discovery authenticates
// and the server's tools appear; without it, they don't.
func TestDiscoveryAuthReachesInitialize(t *testing.T) {
	url, rejected := newAuthGatedMCPServer(t, "Bearer T1")
	seedMCPServerConfig(t, "orchestrator", url)

	t.Run("without the override discovery is rejected", func(t *testing.T) {
		reg := NewMCPRegistry(context.Background(), nil, nil)
		if got := drainLoadTools(t, reg.LoadTools(nil)); len(got) != 0 {
			t.Fatalf("expected no tools without auth, got %d", len(got))
		}
		if rejected.Load() == 0 {
			t.Error("expected the server to have rejected the unauthenticated discovery")
		}
	})

	t.Run("with the override discovery succeeds", func(t *testing.T) {
		reg := NewMCPRegistry(context.Background(), nil, nil)
		reg.SetDiscoveryAuth(map[string]string{"orchestrator": "Bearer T1"})
		got := drainLoadTools(t, reg.LoadTools(nil))
		if len(got) != 1 {
			t.Fatalf("expected 1 tool with the run's token, got %d", len(got))
		}
		if name := got[0].Info().Name; name != "orchestrator_echo" {
			t.Errorf("tool name = %q, want %q", name, "orchestrator_echo")
		}
	})
}

// TestSetDiscoveryAuthClearAndOverwrite pins the lifecycle the flow runner
// drives: publish on Start, clear on terminal, replace on the next run.
func TestSetDiscoveryAuthClearAndOverwrite(t *testing.T) {
	url, _ := newAuthGatedMCPServer(t, "Bearer T2")
	seedMCPServerConfig(t, "orchestrator", url)

	reg := NewMCPRegistry(context.Background(), nil, nil).(*mcpRegistry)

	reg.SetDiscoveryAuth(map[string]string{"orchestrator": "Bearer T1"})
	if v, ok := mcpauthctx.AuthOverrideFromContext(reg.discoveryCtx(), "orchestrator"); !ok || v != "Bearer T1" {
		t.Fatalf("discoveryCtx override = %q/%v, want Bearer T1", v, ok)
	}

	// Run 2 replaces run 1's token wholesale — a stale entry must not
	// linger and re-authenticate a later run with an expired JWT.
	reg.SetDiscoveryAuth(map[string]string{"orchestrator": "Bearer T2"})
	if v, _ := mcpauthctx.AuthOverrideFromContext(reg.discoveryCtx(), "orchestrator"); v != "Bearer T2" {
		t.Errorf("after overwrite = %q, want Bearer T2", v)
	}

	reg.SetDiscoveryAuth(nil)
	if _, ok := mcpauthctx.AuthOverrideFromContext(reg.discoveryCtx(), "orchestrator"); ok {
		t.Error("clear left an override behind")
	}

	// Empty keys/values are dropped rather than stored as a blank header.
	reg.SetDiscoveryAuth(map[string]string{"": "Bearer X", "orchestrator": ""})
	if _, ok := mcpauthctx.AuthOverrideFromContext(reg.discoveryCtx(), "orchestrator"); ok {
		t.Error("empty header value was stored as an override")
	}
}

// TestDiscoveryAuthPreservesRegistryLifetime re-asserts the invariant the
// fix had to respect: discovery must still be bounded by the registry's
// own context and nothing else. The overrides carry VALUES only.
func TestDiscoveryAuthPreservesRegistryLifetime(t *testing.T) {
	url, _ := newAuthGatedMCPServer(t, "Bearer T1")
	seedMCPServerConfig(t, "orchestrator", url)

	appCtx, cancel := context.WithCancel(context.Background())
	reg := NewMCPRegistry(appCtx, nil, nil).(*mcpRegistry)
	reg.SetDiscoveryAuth(map[string]string{"orchestrator": "Bearer T1"})

	if reg.discoveryCtx().Err() != nil {
		t.Fatal("discoveryCtx is already cancelled before app shutdown")
	}
	cancel()
	if reg.discoveryCtx().Err() == nil {
		t.Error("discoveryCtx did not inherit the registry context's cancellation")
	}
}

// TestResolveMCPHeadersDropsLowercaseAuthorization is the regression guard
// for the header-key collision. The fixture uses the LOWERCASE key that
// viper actually produces from .opencode.json — a fixture spelled
// "Authorization" cannot reproduce the bug, because config loading can
// never emit that shape.
func TestResolveMCPHeadersDropsLowercaseAuthorization(t *testing.T) {
	ctx := mcpauthctx.WithAuthOverride(context.Background(), "orchestrator", "Bearer FLOW")

	tests := []struct {
		name   string
		static map[string]string
	}{
		{"viper-lowercased key", map[string]string{"authorization": "Bearer BOOT", "x-env": "prod"}},
		{"canonical key", map[string]string{"Authorization": "Bearer BOOT", "X-Env": "prod"}},
		{"mixed case key", map[string]string{"AuThOrIzAtIoN": "Bearer BOOT"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveMCPHeaders(ctx, "orchestrator", tt.static)

			var authKeys []string
			for k := range got {
				if len(k) == len("authorization") && equalFoldASCII(k, "authorization") {
					authKeys = append(authKeys, k)
				}
			}
			if len(authKeys) != 1 {
				t.Fatalf("layered headers carry %d Authorization-equivalent keys (%v) — net/http would pick one at random", len(authKeys), authKeys)
			}
			if got[authorizationHeader] != "Bearer FLOW" {
				t.Errorf("Authorization = %q, want the run's token", got[authorizationHeader])
			}
			// Non-auth headers survive untouched.
			for k, v := range tt.static {
				if equalFoldASCII(k, "authorization") {
					continue
				}
				if got[k] != v {
					t.Errorf("header %q = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

// TestConfigLoadLowercasesHeaderKeys pins the viper behaviour the fix
// above defends against, so a future config-loader change that stops
// folding keys shows up here rather than as intermittent 401s in
// production.
func TestConfigLoadLowercasesHeaderKeys(t *testing.T) {
	dir := t.TempDir()
	cfgJSON := `{"mcpServers":{"orchestrator":{"type":"http","url":"http://example.invalid","headers":{"Authorization":"Bearer BOOT"}}}}`
	if err := writeFileForTest(dir, ".opencode.json", cfgJSON); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("HOME", dir)
	if _, err := config.Load(dir, false); err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	srv, ok := config.Get().MCPServers["orchestrator"]
	if !ok {
		t.Fatal("orchestrator server missing from loaded config")
	}
	// Assert the KEY shape only. config.Get() is a process-wide singleton
	// that other tests in this package have already populated, and the
	// loader performs its own value substitution — so the value is not
	// ours to predict. The key casing is the whole point here.
	if _, hasCanonical := srv.Headers["Authorization"]; hasCanonical {
		t.Skip("config loader no longer folds header keys — the collision guard is now belt-and-braces")
	}
	if _, hasFolded := srv.Headers["authorization"]; !hasFolded {
		t.Fatalf("loaded headers = %v, expected the lowercase key viper produces", srv.Headers)
	}
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// writeFileForTest writes name inside dir with test-appropriate perms.
func writeFileForTest(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
}
