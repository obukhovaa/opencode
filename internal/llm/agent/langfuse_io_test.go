package agent

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

func TestTelemetryAgentID(t *testing.T) {
	tests := []struct {
		name     string
		ctx      context.Context
		fallback string
		want     string
	}{
		{
			name:     "context agent wins over the owning agent",
			ctx:      context.WithValue(context.Background(), tools.AgentIDContextKey, config.AgentName("descriptor")),
			fallback: "coder",
			want:     "descriptor",
		},
		{
			name:     "falls back to the owning agent",
			ctx:      context.Background(),
			fallback: "coder",
			want:     "coder",
		},
		{
			name:     "empty context value falls back",
			ctx:      context.WithValue(context.Background(), tools.AgentIDContextKey, config.AgentName("")),
			fallback: "coder",
			want:     "coder",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := telemetryAgentID(tt.ctx, tt.fallback); got != tt.want {
				t.Errorf("telemetryAgentID = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSetTraceOutputGate locks in that an opted-out agent never even builds the
// trace output value — the thunk must stay unevaluated, since on the main turn
// it walks the final message.
func TestSetTraceOutputGate(t *testing.T) {
	tests := []struct {
		name      string
		gen       *config.GenerationTelemetryConfig
		agentID   string
		wantBuilt bool
	}{
		{
			name:    "unset config builds nothing",
			gen:     nil,
			agentID: "coder",
		},
		{
			name:    "disabled builds nothing",
			gen:     &config.GenerationTelemetryConfig{LogOutput: []string{"*"}},
			agentID: "coder",
		},
		{
			name:    "logInput alone does not open the output side",
			gen:     &config.GenerationTelemetryConfig{Enabled: true, LogInput: []string{"*"}},
			agentID: "coder",
		},
		{
			name:      "wildcard builds for every agent",
			gen:       &config.GenerationTelemetryConfig{Enabled: true, LogOutput: []string{"*"}},
			agentID:   "coder",
			wantBuilt: true,
		},
		{
			name:      "listed agent builds",
			gen:       &config.GenerationTelemetryConfig{Enabled: true, LogOutput: []string{"summarizer"}},
			agentID:   "summarizer",
			wantBuilt: true,
		},
		{
			name:    "unlisted agent builds nothing",
			gen:     &config.GenerationTelemetryConfig{Enabled: true, LogOutput: []string{"summarizer"}},
			agentID: "coder",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config.Reset()
			if _, err := config.Load(".", false); err != nil {
				t.Fatalf("load config: %v", err)
			}
			t.Cleanup(config.Reset)
			config.Get().Telemetry = &config.TelemetryConfig{Generations: tt.gen}

			a := &agent{agentID: tt.agentID}
			built := false
			a.setTraceOutput(context.Background(), func() any {
				built = true
				return "output"
			})
			if built != tt.wantBuilt {
				t.Errorf("output built = %v, want %v", built, tt.wantBuilt)
			}
		})
	}
}
