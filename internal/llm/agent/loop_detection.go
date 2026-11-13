package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
)

const (
	DefaultMaxTurns        = 100
	DefaultRepeatThreshold = 3

	// MaxTurnsProactiveHintThreshold is the effective max-turn budget at or
	// below which we proactively tell the model how many turns it has. Empirically
	// budget hints help when the cap is tight (the model is more likely to commit
	// to a direct path) and add noise when the cap is large, so we only inject
	// the hint when the budget is small. See proactiveMaxTurnsHint.
	MaxTurnsProactiveHintThreshold = 10
)

type callTracker struct {
	lastCall    map[string]string
	streakCount map[string]int
	lastResult  map[string][32]byte
	threshold   int
	// denials counts the run's permission-rule denials (permission_denial.go).
	denials ruleDenialBreaker
}

func newCallTracker() *callTracker {
	return &callTracker{
		lastCall:    make(map[string]string),
		streakCount: make(map[string]int),
		threshold:   resolveRepeatThreshold(),
	}
}

// Track observes a completed call, before any loop warning replaces its result.
// Only model-visible result fields count; IDs and execution timing metadata do not.
func (t *callTracker) Track(name, input string, result message.ToolResult) bool {
	encoded, _ := json.Marshal([]any{result.Type, result.Content, result.IsError})
	digest := sha256.Sum256(encoded)
	if t.lastResult == nil {
		t.lastResult = make(map[string][32]byte)
	}
	previous, seen := t.lastResult[name]
	if seen && t.lastCall[name] == input && previous == digest {
		t.streakCount[name]++
	} else {
		t.streakCount[name] = 1
	}
	t.lastCall[name] = input
	t.lastResult[name] = digest
	return t.streakCount[name] >= t.threshold
}

// Blocked prevents further executions after a completed-result streak proved
// the call is stuck. A changed request or delivered input allows it to run again.
func (t *callTracker) Blocked(name, input string) bool {
	return t.lastCall[name] == input && t.streakCount[name] >= t.threshold
}

// Reset clears repetition evidence, not the independent permission-denial budget.
func (t *callTracker) Reset() {
	clear(t.lastCall)
	clear(t.lastResult)
	clear(t.streakCount)
}

func resolveRepeatThreshold() int {
	if envVal := os.Getenv("OPENCODE_MAX_REPEAT_CALLS"); envVal != "" {
		if v, err := strconv.Atoi(envVal); err == nil && v > 0 {
			return v
		}
		logging.Warn("Invalid OPENCODE_MAX_REPEAT_CALLS value, using default",
			"value", envVal, "default", DefaultRepeatThreshold)
	}
	return DefaultRepeatThreshold
}

// resolveMaxTurnsValues picks the effective max-turn budget for a generation.
// Precedence (highest wins):
//  1. callerOverride  — per-call override (e.g. a flow step setting Step.MaxTurns).
//  2. globalMaxTurns  — `maxTurns` in .opencode.json / --max-turns CLI flag.
//  3. agentMaxTurns   — `maxTurns` in the agent's config.
//  4. DefaultMaxTurns — hard-coded fallback (100).
//
// A value of 0 at any level means "not set; fall through". Negative values are
// validated out at the source (flow validator, config loader) and treated as 0 here.
func resolveMaxTurnsValues(callerOverride, globalMaxTurns, agentMaxTurns int) int {
	if callerOverride > 0 {
		return callerOverride
	}
	if globalMaxTurns > 0 {
		return globalMaxTurns
	}
	if agentMaxTurns > 0 {
		return agentMaxTurns
	}
	return DefaultMaxTurns
}

// resolveMaxTurns resolves the effective max-turn budget for a generation,
// optionally honoring a caller-supplied override (e.g. flow step Step.MaxTurns).
// Pass 0 for callerOverride to opt out of the override (existing call sites).
func resolveMaxTurns(callerOverride int, agentID config.AgentName) int {
	var globalMaxTurns int
	if cfg := config.Get(); cfg != nil {
		globalMaxTurns = cfg.MaxTurns
	}
	var agentMaxTurns int
	reg := agentregistry.GetRegistry()
	if info, ok := reg.Get(agentID); ok {
		agentMaxTurns = info.MaxTurns
	}
	return resolveMaxTurnsValues(callerOverride, globalMaxTurns, agentMaxTurns)
}

// proactiveMaxTurnsHint returns a short constraint suffix to append to the
// user's prompt when the effective max-turn budget is tight
// (≤ MaxTurnsProactiveHintThreshold). Returns empty string when the budget is
// larger — at that point the hint becomes irrelevant noise that can cause
// premature "I'm running out of turns" guessing.
func proactiveMaxTurnsHint(effectiveMaxTurns int) string {
	if effectiveMaxTurns <= 0 || effectiveMaxTurns > MaxTurnsProactiveHintThreshold {
		return ""
	}
	return fmt.Sprintf("\n\n[Turn budget] You have %d tool-use turns to complete this task.", effectiveMaxTurns)
}

func loopDetectedMessage(toolName string, streak int) string {
	return fmt.Sprintf(
		"Loop detected: you have called tool %q with identical arguments and results %d times consecutively. "+
			"No new result was observed. Please try a different approach, use different arguments, "+
			"or provide your final response.",
		toolName, streak,
	)
}
