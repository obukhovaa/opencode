package agent

import (
	"fmt"
	"os"
	"strconv"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
)

const (
	DefaultMaxTurns        = 100
	DefaultRepeatThreshold = 3
)

type callTracker struct {
	lastCall    map[string]string
	streakCount map[string]int
	threshold   int
}

func newCallTracker() *callTracker {
	return &callTracker{
		lastCall:    make(map[string]string),
		streakCount: make(map[string]int),
		threshold:   resolveRepeatThreshold(),
	}
}

func (t *callTracker) Track(name, input string) bool {
	if t.lastCall[name] == input {
		t.streakCount[name]++
	} else {
		t.streakCount[name] = 1
		t.lastCall[name] = input
	}
	return t.streakCount[name] >= t.threshold
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

func resolveMaxTurnsValues(globalMaxTurns, agentMaxTurns int) int {
	if globalMaxTurns > 0 {
		return globalMaxTurns
	}
	if agentMaxTurns > 0 {
		return agentMaxTurns
	}
	return DefaultMaxTurns
}

func resolveMaxTurns(agentID config.AgentName) int {
	var globalMaxTurns int
	if cfg := config.Get(); cfg != nil {
		globalMaxTurns = cfg.MaxTurns
	}
	var agentMaxTurns int
	reg := agentregistry.GetRegistry()
	if info, ok := reg.Get(agentID); ok {
		agentMaxTurns = info.MaxTurns
	}
	return resolveMaxTurnsValues(globalMaxTurns, agentMaxTurns)
}

func loopDetectedMessage(toolName string, streak int) string {
	return fmt.Sprintf(
		"Loop detected: you have called tool %q with identical arguments %d times consecutively. "+
			"The output will not change. Please try a different approach, use different arguments, "+
			"or provide your final response.",
		toolName, streak,
	)
}
