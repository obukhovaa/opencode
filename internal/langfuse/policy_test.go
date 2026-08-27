package langfuse

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
)

func TestCaptureAllowed(t *testing.T) {
	tests := []struct {
		name       string
		gen        *config.GenerationTelemetryConfig
		agentID    string
		wantInput  bool
		wantOutput bool
	}{
		{
			name:    "nil config captures nothing",
			gen:     nil,
			agentID: "coder",
		},
		{
			name:    "disabled captures nothing even with wildcards",
			gen:     &config.GenerationTelemetryConfig{LogInput: []string{"*"}, LogOutput: []string{"*"}},
			agentID: "coder",
		},
		{
			name:    "enabled with no patterns captures nothing",
			gen:     &config.GenerationTelemetryConfig{Enabled: true},
			agentID: "coder",
		},
		{
			name:       "wildcard covers every agent",
			gen:        &config.GenerationTelemetryConfig{Enabled: true, LogInput: []string{"*"}, LogOutput: []string{"*"}},
			agentID:    "coder",
			wantInput:  true,
			wantOutput: true,
		},
		{
			name:      "listed agent only, input side",
			gen:       &config.GenerationTelemetryConfig{Enabled: true, LogInput: []string{"workhorse"}},
			agentID:   "workhorse",
			wantInput: true,
		},
		{
			name:    "unlisted agent is excluded",
			gen:     &config.GenerationTelemetryConfig{Enabled: true, LogInput: []string{"workhorse"}, LogOutput: []string{"workhorse"}},
			agentID: "coder",
		},
		{
			name:       "input and output are independent",
			gen:        &config.GenerationTelemetryConfig{Enabled: true, LogInput: []string{"workhorse"}, LogOutput: []string{"coder"}},
			agentID:    "coder",
			wantOutput: true,
		},
		{
			name:      "prefix wildcard",
			gen:       &config.GenerationTelemetryConfig{Enabled: true, LogInput: []string{"sub*"}},
			agentID:   "subagent-reviewer",
			wantInput: true,
		},
		{
			name:      "pattern and agent id match case-insensitively",
			gen:       &config.GenerationTelemetryConfig{Enabled: true, LogInput: []string{"WorkHorse"}},
			agentID:   "workhorse",
			wantInput: true,
		},
		{
			name:      "wildcard matches an unknown (empty) agent id",
			gen:       &config.GenerationTelemetryConfig{Enabled: true, LogInput: []string{"*"}},
			agentID:   "",
			wantInput: true,
		},
		{
			name:    "named pattern does not match an unknown agent id",
			gen:     &config.GenerationTelemetryConfig{Enabled: true, LogInput: []string{"coder"}},
			agentID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := captureAllowed(tt.gen, tt.agentID, captureInput); got != tt.wantInput {
				t.Errorf("input allowed = %v, want %v", got, tt.wantInput)
			}
			if got := captureAllowed(tt.gen, tt.agentID, captureOutput); got != tt.wantOutput {
				t.Errorf("output allowed = %v, want %v", got, tt.wantOutput)
			}
		})
	}
}

// The exported entry points read the global config, which is unset in this
// package's tests — they must fail closed rather than panic.
func TestShouldLogWithoutConfig(t *testing.T) {
	for name, fn := range map[string]func(string) bool{
		"generation input":  ShouldLogGenerationInput,
		"generation output": ShouldLogGenerationOutput,
		"tool input":        ShouldLogToolInput,
		"tool output":       ShouldLogToolOutput,
	} {
		if fn("coder") {
			t.Errorf("%s must be off with no config loaded", name)
		}
	}
}

// Tool and generation capture resolve through one matcher, so the two sections
// must not drift apart on matching semantics (they previously did: tool
// patterns were case-sensitive, generation patterns were not).
func TestCaptureAllowedSemanticsAreSharedAcrossSections(t *testing.T) {
	section := &config.CaptureTelemetryConfig{Enabled: true, LogInput: []string{"Bash"}}
	if !captureAllowed(section, "bash", captureInput) {
		t.Error("tool patterns must match case-insensitively, same as agent patterns")
	}
}
