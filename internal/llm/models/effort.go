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
// hide a routing bug, so the outcome here is an error:
//
//   - "" is always legal — the provider default applies.
//   - a model that cannot reason accepts no effort at all.
//   - every reasoning model accepts low|medium|high (OpenAI, Yandex, local
//     endpoints and Gemini stop there).
//   - xhigh needs SupportsXHighThinking and max needs SupportsMaximumThinking:
//     the adaptive-thinking Claude and Kimi models, plus Kimi K3 on Bedrock,
//     whose OpenAI-style reasoning_effort field takes max as well.
//
// This is deliberately STRICTER than config.validateAgent, not a mirror of
// it. Config only coerces the effort for OpenAI/Local-provider models, for
// adaptive-thinking models and for Kimi on Bedrock; a reasoning model that
// is none of those (e.g.
// claude-4.5-opus, gemini-3.0-flash, yandexcloud.*) passes through config
// with whatever effort string the agent declared, xhigh/max included, and
// the provider client decides what to do with it. The same value on a flow
// step override is rejected here, because the catalog flags say the model
// cannot honour it. Accept that asymmetry rather than loosening this side:
// the config path is a compatibility shim over hand-written agent config,
// while a step override is a per-run routing decision that should fail
// loudly when it names a level the model cannot run.
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
		if !m.SupportsXHighThinking {
			return fmt.Errorf("%w: model %s does not support %q", ErrInvalidReasoningEffort, m.ID, lower)
		}
	case ReasoningEffortMax:
		if !m.SupportsMaximumThinking {
			return fmt.Errorf("%w: model %s does not support %q", ErrInvalidReasoningEffort, m.ID, lower)
		}
	}
	return nil
}
