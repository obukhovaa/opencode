package langfuse

import (
	"strings"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/permission"
)

// ShouldLogGenerationInput reports whether the LLM request payload (system
// prompt + message history) and the trace-level input for the given agent may
// be attached to telemetry. Off unless telemetry.generations.enabled is true
// and the agent ID matches one of the telemetry.generations.logInput patterns.
func ShouldLogGenerationInput(agentID string) bool {
	return generationIOAllowed(generationTelemetry(), agentID, true)
}

// ShouldLogGenerationOutput reports whether the LLM response (content,
// reasoning, tool calls) and the trace-level output for the given agent may be
// attached to telemetry. Gated by telemetry.generations.logOutput.
func ShouldLogGenerationOutput(agentID string) bool {
	return generationIOAllowed(generationTelemetry(), agentID, false)
}

// generationTelemetry returns the generation telemetry config, or nil when
// telemetry is unconfigured or generation capture is disabled. Nil-safe on an
// unloaded config so telemetry policy never panics a request path.
func generationTelemetry() *config.GenerationTelemetryConfig {
	cfg := config.Get()
	if cfg == nil || cfg.Telemetry == nil {
		return nil
	}
	return cfg.Telemetry.Generations
}

// generationIOAllowed is the pure policy decision, split out from the global
// config read so it can be tested directly. input selects which pattern list
// applies.
func generationIOAllowed(gen *config.GenerationTelemetryConfig, agentID string, input bool) bool {
	if gen == nil || !gen.Enabled {
		return false
	}
	patterns := gen.LogOutput
	if input {
		patterns = gen.LogInput
	}
	// Agent IDs are lowercase by convention but markdown-defined agents take
	// their ID from a file basename, so fold both sides before matching.
	agentID = strings.ToLower(agentID)
	for _, p := range patterns {
		if permission.MatchWildcard(strings.ToLower(p), agentID) {
			return true
		}
	}
	return false
}
