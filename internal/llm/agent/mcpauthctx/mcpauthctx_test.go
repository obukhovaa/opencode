package mcpauthctx

import (
	"context"
	"testing"
)

func TestWithAuthOverride(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		setup     func() context.Context
		server    string
		wantValue string
		wantOK    bool
	}{
		{
			name:   "no override set",
			setup:  context.Background,
			server: "orchestrator",
			wantOK: false,
		},
		{
			name: "override present for requested server",
			setup: func() context.Context {
				return WithAuthOverride(context.Background(), "orchestrator", "Bearer T1")
			},
			server:    "orchestrator",
			wantValue: "Bearer T1",
			wantOK:    true,
		},
		{
			name: "override keyed by different server name is not returned",
			setup: func() context.Context {
				return WithAuthOverride(context.Background(), "orchestrator", "Bearer T1")
			},
			server: "gitlab",
			wantOK: false,
		},
		{
			name: "empty server name is a no-op",
			setup: func() context.Context {
				return WithAuthOverride(context.Background(), "", "Bearer T1")
			},
			server: "",
			wantOK: false,
		},
		{
			name: "second override for same server shadows the first",
			setup: func() context.Context {
				ctx := WithAuthOverride(context.Background(), "orchestrator", "Bearer OLD")
				return WithAuthOverride(ctx, "orchestrator", "Bearer NEW")
			},
			server:    "orchestrator",
			wantValue: "Bearer NEW",
			wantOK:    true,
		},
		{
			name: "overrides for distinct servers accumulate",
			setup: func() context.Context {
				ctx := WithAuthOverride(context.Background(), "orchestrator", "Bearer T1")
				return WithAuthOverride(ctx, "gitlab", "Bearer T2")
			},
			server:    "orchestrator",
			wantValue: "Bearer T1",
			wantOK:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := AuthOverrideFromContext(tt.setup(), tt.server)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.wantValue {
				t.Errorf("value = %q, want %q", got, tt.wantValue)
			}
		})
	}
}

// TestWithAuthOverrideCopyOnWrite verifies a derived context's override
// never leaks into the parent — the map is copied, not shared.
func TestWithAuthOverrideCopyOnWrite(t *testing.T) {
	t.Parallel()
	parent := WithAuthOverride(context.Background(), "orchestrator", "Bearer PARENT")
	child := WithAuthOverride(parent, "orchestrator", "Bearer CHILD")

	if v, _ := AuthOverrideFromContext(parent, "orchestrator"); v != "Bearer PARENT" {
		t.Errorf("parent override mutated to %q", v)
	}
	if v, _ := AuthOverrideFromContext(child, "orchestrator"); v != "Bearer CHILD" {
		t.Errorf("child override = %q, want Bearer CHILD", v)
	}
}

// TestAuthOverrideSurvivesDerivedContexts verifies the override is
// visible through contexts derived from the run context (tool-call
// goroutines derive their own contexts from runCtx).
func TestAuthOverrideSurvivesDerivedContexts(t *testing.T) {
	t.Parallel()
	runCtx := WithAuthOverride(context.Background(), "orchestrator", "Bearer T1")
	derived, cancel := context.WithCancel(runCtx)
	defer cancel()

	if v, ok := AuthOverrideFromContext(derived, "orchestrator"); !ok || v != "Bearer T1" {
		t.Errorf("derived ctx override = %q ok=%v, want Bearer T1 true", v, ok)
	}
}
