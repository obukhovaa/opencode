package agent

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/agent/mcpauthctx"
)

// TestResolveMCPHeaders covers the per-call header layering
// mcpRegistry.StartClient performs for the per-flow MCP auth override
// (openspec change agent-pod-pool-runtime, design D1).
func TestResolveMCPHeaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		ctx      func() context.Context
		server   string
		static   map[string]string
		want     map[string]string
		wantSame bool // expect the static map returned as-is (no copy)
	}{
		{
			name:     "no override returns static map untouched",
			ctx:      context.Background,
			server:   "orchestrator",
			static:   map[string]string{"Authorization": "Bearer BOOT"},
			want:     map[string]string{"Authorization": "Bearer BOOT"},
			wantSame: true,
		},
		{
			name: "override shadows boot-time Authorization",
			ctx: func() context.Context {
				return mcpauthctx.WithAuthOverride(context.Background(), "orchestrator", "Bearer FLOW")
			},
			server: "orchestrator",
			static: map[string]string{"Authorization": "Bearer BOOT", "X-Env": "prod"},
			want:   map[string]string{"Authorization": "Bearer FLOW", "X-Env": "prod"},
		},
		{
			name: "override adds Authorization when boot headers empty",
			ctx: func() context.Context {
				return mcpauthctx.WithAuthOverride(context.Background(), "orchestrator", "Bearer T1")
			},
			server: "orchestrator",
			static: nil,
			want:   map[string]string{"Authorization": "Bearer T1"},
		},
		{
			name: "override for a different server does not apply",
			ctx: func() context.Context {
				return mcpauthctx.WithAuthOverride(context.Background(), "gitlab", "Bearer OTHER")
			},
			server:   "orchestrator",
			static:   map[string]string{"Authorization": "Bearer BOOT"},
			want:     map[string]string{"Authorization": "Bearer BOOT"},
			wantSame: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolveMCPHeaders(tt.ctx(), tt.server, tt.static)
			if len(got) != len(tt.want) {
				t.Fatalf("headers = %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("header %q = %q, want %q", k, got[k], v)
				}
			}
			if tt.wantSame && tt.static != nil {
				// Maps are reference types; verify identity via a
				// mutation probe — a write through the returned map must
				// be visible through the static one.
				got["__probe__"] = "x"
				if tt.static["__probe__"] != "x" {
					t.Errorf("expected the static map to be returned as-is when no override applies")
				}
				delete(got, "__probe__")
			}
		})
	}
}

// TestResolveMCPHeadersDoesNotMutateStatic verifies the boot-time
// config map is preserved across an override — the layered copy is
// per-call, so concurrent tool calls and later flows keep seeing the
// boot value.
func TestResolveMCPHeadersDoesNotMutateStatic(t *testing.T) {
	t.Parallel()
	static := map[string]string{"Authorization": "Bearer BOOT"}
	ctx := mcpauthctx.WithAuthOverride(context.Background(), "orchestrator", "Bearer FLOW")

	got := resolveMCPHeaders(ctx, "orchestrator", static)
	if got["Authorization"] != "Bearer FLOW" {
		t.Fatalf("layered Authorization = %q, want Bearer FLOW", got["Authorization"])
	}
	if static["Authorization"] != "Bearer BOOT" {
		t.Errorf("static map mutated: Authorization = %q, want Bearer BOOT", static["Authorization"])
	}

	// A subsequent call under a context with no override sees the
	// original boot value again.
	after := resolveMCPHeaders(context.Background(), "orchestrator", static)
	if after["Authorization"] != "Bearer BOOT" {
		t.Errorf("post-flow Authorization = %q, want Bearer BOOT", after["Authorization"])
	}
}
