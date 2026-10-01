package agent

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

func TestStampRequester(t *testing.T) {
	tests := []struct {
		name      string
		static    string
		ctxValue  string
		flowArg   string
		want      string
		wantUnset bool
	}{
		{name: "nothing known leaves metadata unset", wantUnset: true},
		{name: "static config is the fallback", static: "owner@example.com", want: "owner@example.com"},
		{name: "per-turn requester beats static config", static: "owner@example.com", ctxValue: "author@example.com", want: "author@example.com"},
		{name: "per-turn requester without static config", ctxValue: "U123", want: "U123"},
		{name: "flow arg beats both", static: "owner@example.com", ctxValue: "author@example.com", flowArg: "flow@example.com", want: "flow@example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loadConfigIn(t, ".")
			config.Get().Telemetry = &config.TelemetryConfig{Requester: tt.static}

			metadata := map[string]any{}
			if tt.flowArg != "" {
				metadata["requester"] = tt.flowArg
			}
			ctx := tools.WithRequester(context.Background(), tt.ctxValue)
			stampRequester(ctx, metadata)

			got, ok := metadata["requester"]
			if tt.wantUnset {
				if ok {
					t.Fatalf("requester = %v, want unset", got)
				}
				return
			}
			if got != tt.want {
				t.Fatalf("requester = %v, want %q", got, tt.want)
			}
		})
	}
}

// A detached async subagent must keep the parent turn's requester: its base
// ctx is context.Background() (interactive) or the step scope, neither of
// which carries the turn's values.
func TestRequesterSurvivesDetachedSubagentBase(t *testing.T) {
	parent := tools.WithRequester(context.Background(), "author@example.com")
	runCtx := tools.WithRequester(subagentBaseContext(parent), tools.RequesterFromContext(parent))
	if got := tools.RequesterFromContext(runCtx); got != "author@example.com" {
		t.Fatalf("requester = %q, want author@example.com", got)
	}
}
