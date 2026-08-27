// Package-level telemetry capture policy: the single place that decides
// whether a payload may be attached to the telemetry backend. Both the tool
// section (telemetry.tools) and the LLM section (telemetry.generations) resolve
// through the same matcher so their configs behave identically.
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
	return captureAllowed(telemetrySection(func(t *config.TelemetryConfig) *config.CaptureTelemetryConfig {
		return t.Generations
	}), agentID, captureInput)
}

// ShouldLogGenerationOutput reports whether the LLM response (content,
// reasoning, tool calls) and the trace-level output for the given agent may be
// attached to telemetry. Gated by telemetry.generations.logOutput.
func ShouldLogGenerationOutput(agentID string) bool {
	return captureAllowed(telemetrySection(func(t *config.TelemetryConfig) *config.CaptureTelemetryConfig {
		return t.Generations
	}), agentID, captureOutput)
}

// ShouldLogToolInput reports whether the named tool's input may be attached to
// telemetry, per telemetry.tools.logInput.
func ShouldLogToolInput(toolName string) bool {
	return captureAllowed(telemetrySection(func(t *config.TelemetryConfig) *config.CaptureTelemetryConfig {
		return t.Tools
	}), toolName, captureInput)
}

// ShouldLogToolOutput reports whether the named tool's output may be attached
// to telemetry, per telemetry.tools.logOutput. Note that tool *errors* are
// logged regardless — they are diagnostic, not content.
func ShouldLogToolOutput(toolName string) bool {
	return captureAllowed(telemetrySection(func(t *config.TelemetryConfig) *config.CaptureTelemetryConfig {
		return t.Tools
	}), toolName, captureOutput)
}

type captureSide bool

const (
	captureInput  captureSide = true
	captureOutput captureSide = false
)

// telemetrySection reads one capture section off the global config, or nil when
// telemetry is unconfigured. Nil-safe on an unloaded config so telemetry policy
// never panics a request path.
func telemetrySection(pick func(*config.TelemetryConfig) *config.CaptureTelemetryConfig) *config.CaptureTelemetryConfig {
	cfg := config.Get()
	if cfg == nil || cfg.Telemetry == nil {
		return nil
	}
	return pick(cfg.Telemetry)
}

// captureAllowed is the pure policy decision, split out from the global config
// read so it can be tested directly.
func captureAllowed(section *config.CaptureTelemetryConfig, name string, side captureSide) bool {
	if section == nil || !section.Enabled {
		return false
	}
	patterns := section.LogOutput
	if side == captureInput {
		patterns = section.LogInput
	}
	// Fold both sides: agent IDs come from markdown file basenames and tool
	// names from MCP servers, neither of which guarantees a case convention.
	name = strings.ToLower(name)
	for _, p := range patterns {
		if permission.MatchWildcard(strings.ToLower(p), name) {
			return true
		}
	}
	return false
}
