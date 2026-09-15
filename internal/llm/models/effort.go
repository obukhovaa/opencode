package models

import (
	"errors"
	"fmt"
	"strings"
)

// Reasoning-effort levels a model may accept. Which subset a given model
// admits is decided by ValidateReasoningEffort; the catalog flags on Model
// (CanReason, SupportsAdaptiveThinking, SupportsXHighThinking,
// SupportsMaximumThinking) are the single source of truth for that.
const (
	ReasoningEffortLow    = "low"
	ReasoningEffortMedium = "medium"
	ReasoningEffortHigh   = "high"
	ReasoningEffortXHigh  = "xhigh"
	ReasoningEffortMax    = "max"
)

// ErrInvalidReasoningEffort is returned by ValidateReasoningEffort when the
// effort is not a level the given model accepts.
var ErrInvalidReasoningEffort = errors.New("invalid reasoning effort")

// IsReasoningEffort reports whether s (case-insensitively) names one of the
// reasoning-effort levels, regardless of which model would accept it.
func IsReasoningEffort(s string) bool {
	switch strings.ToLower(s) {
	case ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh,
		ReasoningEffortXHigh, ReasoningEffortMax:
		return true
	}
	return false
}

// ValidateReasoningEffort reports whether effort is a level model m accepts.
// It is the erroring counterpart of the coercion in config.validateAgent
// (internal/config/config.go), which silently rewrites an unsupported
// level to a supported one at config load. A flow step's model override is
// resolved at run time from templated args, where a silent rewrite would
// hide a routing bug; the rules are therefore the same but the outcome is
// an error:
//
//   - "" is always legal — the provider default applies.
//   - a model that cannot reason accepts no effort at all.
//   - adaptive-thinking models accept low|medium|high, plus xhigh only when
//     SupportsXHighThinking and max only when SupportsMaximumThinking.
//   - every other reasoning model (OpenAI, Yandex, local endpoints, Gemini)
//     accepts low|medium|high.
//
// Comparison is case-insensitive; callers that persist the value should
// lower-case it themselves.
func ValidateReasoningEffort(m Model, effort string) error {
	if effort == "" {
		return nil
	}
	lower := strings.ToLower(effort)
	if !IsReasoningEffort(lower) {
		return fmt.Errorf("%w: %q is not one of low|medium|high|xhigh|max", ErrInvalidReasoningEffort, effort)
	}
	if !m.CanReason {
		return fmt.Errorf("%w: model %s does not support reasoning", ErrInvalidReasoningEffort, m.ID)
	}
	switch lower {
	case ReasoningEffortXHigh:
		if !m.SupportsAdaptiveThinking || !m.SupportsXHighThinking {
			return fmt.Errorf("%w: model %s does not support %q", ErrInvalidReasoningEffort, m.ID, lower)
		}
	case ReasoningEffortMax:
		if !m.SupportsAdaptiveThinking || !m.SupportsMaximumThinking {
			return fmt.Errorf("%w: model %s does not support %q", ErrInvalidReasoningEffort, m.ID, lower)
		}
	}
	return nil
}
