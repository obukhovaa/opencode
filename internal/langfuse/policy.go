// Package-level telemetry capture policy: the single place that decides
// whether a payload may be attached to the telemetry backend. Both the tool
// section (telemetry.tools) and the LLM section (telemetry.generations) resolve
// through the same matcher so their configs behave identically.
package langfuse

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/redact"
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

// --- Redaction policy ---

// redactorOverride, when set, replaces the config-derived redactor. Only the
// package's own tests set it: config.Load is load-once per process (it returns
// the existing config if one is already loaded), so a test cannot drive
// redaction settings through the config global once any other test has loaded
// one. buildRedactor stays the unit under test for config mapping.
var redactorOverride atomic.Pointer[redact.Redactor]

var (
	redactorMu   sync.Mutex
	redactorCfg  *config.RedactionConfig
	redactorInst *redact.Redactor
	redactorInit bool
)

// redactor returns the process redactor, rebuilt only when the configured
// redaction section changes identity. Compiling ~12 patterns on every span
// attribute would be absurd; config is effectively immutable after load, so
// caching on the section pointer is both cheap and correct.
func redactor() *redact.Redactor {
	if r := redactorOverride.Load(); r != nil {
		return r
	}

	var rc *config.RedactionConfig
	if cfg := config.Get(); cfg != nil && cfg.Telemetry != nil {
		rc = cfg.Telemetry.Redaction
	}

	redactorMu.Lock()
	defer redactorMu.Unlock()
	if redactorInit && redactorCfg == rc {
		return redactorInst
	}
	redactorInst = buildRedactor(rc)
	redactorCfg = rc
	redactorInit = true
	return redactorInst
}

// buildRedactor maps config onto redact.Options. A nil section means "not
// configured", which is ON with built-in detectors — redaction defaults on
// because telemetry capture is itself opt-in, so the default costs nothing to
// anyone not already capturing content and protects everyone who is.
func buildRedactor(rc *config.RedactionConfig) *redact.Redactor {
	if !rc.IsEnabled() {
		return redact.Disabled()
	}
	opts := redact.Options{}
	if rc != nil {
		opts.Mode = redact.Mode(rc.Mode)
		opts.DisableBuiltins = rc.DisableBuiltins
		opts.EnablePII = rc.PII
		opts.Allowlist = rc.Allowlist
		for _, r := range rc.Rules {
			opts.Rules = append(opts.Rules, redact.Rule{
				Name: r.Name, Pattern: r.Pattern, Group: r.Group, Replacement: r.Replacement,
			})
		}
	}
	return redact.New(opts)
}

// ResetRedactorCache drops the cached redactor. Tests that mutate the global
// config call it so the next span picks up the new settings.
func ResetRedactorCache() {
	redactorMu.Lock()
	defer redactorMu.Unlock()
	redactorInit = false
	redactorCfg = nil
	redactorInst = nil
}
