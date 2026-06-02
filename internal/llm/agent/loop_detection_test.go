package agent

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestCallTracker_BasicStreak(t *testing.T) {
	tracker := &callTracker{
		lastCall:    make(map[string]string),
		streakCount: make(map[string]int),
		threshold:   3,
	}

	// First call — no loop
	if tracker.Track("bash", "ls -la") {
		t.Error("first call should not trigger loop")
	}
	// Second identical call — still no loop
	if tracker.Track("bash", "ls -la") {
		t.Error("second call should not trigger loop")
	}
	// Third identical call — loop detected
	if !tracker.Track("bash", "ls -la") {
		t.Error("third identical call should trigger loop")
	}
}

func TestCallTracker_ResetOnDifferentInput(t *testing.T) {
	tracker := &callTracker{
		lastCall:    make(map[string]string),
		streakCount: make(map[string]int),
		threshold:   3,
	}

	tracker.Track("bash", "ls -la")
	tracker.Track("bash", "ls -la")
	// Different input resets
	if tracker.Track("bash", "cat foo.txt") {
		t.Error("different input should reset streak")
	}
	if tracker.streakCount["bash"] != 1 {
		t.Errorf("streak should be 1 after reset, got %d", tracker.streakCount["bash"])
	}
}

func TestCallTracker_IndependentToolNames(t *testing.T) {
	tracker := &callTracker{
		lastCall:    make(map[string]string),
		streakCount: make(map[string]int),
		threshold:   3,
	}

	tracker.Track("bash", "ls")
	tracker.Track("bash", "ls")
	// Different tool name — independent
	if tracker.Track("read", "ls") {
		t.Error("different tool name should track independently")
	}
	if tracker.streakCount["bash"] != 2 {
		t.Errorf("bash streak should remain 2, got %d", tracker.streakCount["bash"])
	}
	if tracker.streakCount["read"] != 1 {
		t.Errorf("read streak should be 1, got %d", tracker.streakCount["read"])
	}
}

func TestCallTracker_ThresholdOne(t *testing.T) {
	tracker := &callTracker{
		lastCall:    make(map[string]string),
		streakCount: make(map[string]int),
		threshold:   1,
	}

	// With threshold=1, even the first call triggers (streak starts at 1)
	if !tracker.Track("bash", "echo hi") {
		t.Error("threshold=1 should trigger on first call")
	}
}

func TestResolveRepeatThreshold_Default(t *testing.T) {
	os.Unsetenv("OPENCODE_MAX_REPEAT_CALLS")
	if got := resolveRepeatThreshold(); got != DefaultRepeatThreshold {
		t.Errorf("default threshold = %d, want %d", got, DefaultRepeatThreshold)
	}
}

func TestResolveRepeatThreshold_EnvOverride(t *testing.T) {
	t.Setenv("OPENCODE_MAX_REPEAT_CALLS", "5")
	if got := resolveRepeatThreshold(); got != 5 {
		t.Errorf("env override threshold = %d, want 5", got)
	}
}

func TestResolveRepeatThreshold_InvalidEnv(t *testing.T) {
	t.Setenv("OPENCODE_MAX_REPEAT_CALLS", "0")
	if got := resolveRepeatThreshold(); got != DefaultRepeatThreshold {
		t.Errorf("invalid env should fallback to default, got %d", got)
	}

	t.Setenv("OPENCODE_MAX_REPEAT_CALLS", "-1")
	if got := resolveRepeatThreshold(); got != DefaultRepeatThreshold {
		t.Errorf("negative env should fallback to default, got %d", got)
	}

	t.Setenv("OPENCODE_MAX_REPEAT_CALLS", "abc")
	if got := resolveRepeatThreshold(); got != DefaultRepeatThreshold {
		t.Errorf("non-numeric env should fallback to default, got %d", got)
	}
}

func TestResolveMaxTurnsValues(t *testing.T) {
	tests := []struct {
		name     string
		override int
		global   int
		agent    int
		expected int
	}{
		// override > global > agent > default
		{"Override wins over global+agent", 10, 50, 200, 10},
		{"Override wins when others zero", 7, 0, 0, 7},
		{"Override skipped when zero, global wins", 0, 50, 200, 50},
		{"Override and global zero, agent wins", 0, 0, 75, 75},
		{"All zero falls through to default", 0, 0, 0, DefaultMaxTurns},
		{"Override skipped, global wins when agent zero", 0, 30, 0, 30},
		{"Negative override treated as unset", -5, 0, 200, 200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveMaxTurnsValues(tt.override, tt.global, tt.agent)
			if got != tt.expected {
				t.Errorf("resolveMaxTurnsValues(%d, %d, %d) = %d, want %d", tt.override, tt.global, tt.agent, got, tt.expected)
			}
		})
	}
}

func TestProactiveMaxTurnsHint(t *testing.T) {
	tests := []struct {
		name              string
		effectiveMaxTurns int
		wantNonEmpty      bool
		wantContains      string
	}{
		{"zero → no hint", 0, false, ""},
		{"negative → no hint", -1, false, ""},
		{"1 → hint with 1", 1, true, "1 tool-use turns"},
		{"5 → hint with 5", 5, true, "5 tool-use turns"},
		{"exactly at threshold → hint", MaxTurnsProactiveHintThreshold, true, "10 tool-use turns"},
		{"just above threshold → no hint", MaxTurnsProactiveHintThreshold + 1, false, ""},
		{"large budget → no hint", 100, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := proactiveMaxTurnsHint(tt.effectiveMaxTurns)
			if tt.wantNonEmpty && got == "" {
				t.Fatalf("proactiveMaxTurnsHint(%d) = empty, want non-empty", tt.effectiveMaxTurns)
			}
			if !tt.wantNonEmpty && got != "" {
				t.Fatalf("proactiveMaxTurnsHint(%d) = %q, want empty", tt.effectiveMaxTurns, got)
			}
			if tt.wantContains != "" && !strings.Contains(got, tt.wantContains) {
				t.Errorf("proactiveMaxTurnsHint(%d) = %q, want substring %q", tt.effectiveMaxTurns, got, tt.wantContains)
			}
		})
	}
}

func TestLoopDetectedMessage(t *testing.T) {
	msg := loopDetectedMessage("bash", 5)
	if msg == "" {
		t.Error("message should not be empty")
	}
	if !strings.Contains(msg, "bash") {
		t.Error("message should contain tool name")
	}
	if !strings.Contains(msg, "5") {
		t.Error("message should contain streak count")
	}
	if !strings.Contains(msg, "different approach") {
		t.Error("message should suggest different approach")
	}
}

func TestCallTracker_SimulatedAgentLoop(t *testing.T) {
	tracker := &callTracker{
		lastCall:    make(map[string]string),
		streakCount: make(map[string]int),
		threshold:   3,
	}

	type toolCall struct {
		name  string
		input string
	}

	// Simulate an agent loop that repeats the same tool call across cycles,
	// interspersed with different calls. This mirrors what processGeneration does.
	cycles := [][]toolCall{
		// Cycle 1: two different tools
		{{"bash", "git show HEAD:file.kt"}, {"read", "src/main.go"}},
		// Cycle 2: bash repeats (streak=2), read changes
		{{"bash", "git show HEAD:file.kt"}, {"read", "src/other.go"}},
		// Cycle 3: bash repeats again (streak=3 → loop detected), read repeats (streak=1, reset)
		{{"bash", "git show HEAD:file.kt"}, {"read", "src/other.go"}},
		// Cycle 4: bash still repeating (streak=4 → still loop), read repeats (streak=2)
		{{"bash", "git show HEAD:file.kt"}, {"read", "src/other.go"}},
	}

	loopDetected := make(map[string][]int)

	for cycleIdx, calls := range cycles {
		for _, tc := range calls {
			if tracker.Track(tc.name, tc.input) {
				loopDetected[tc.name] = append(loopDetected[tc.name], cycleIdx)
			}
		}
	}

	// bash should trigger loop starting at cycle 2 (0-indexed), streak >= 3
	if len(loopDetected["bash"]) == 0 {
		t.Fatal("bash loop should have been detected")
	}
	if loopDetected["bash"][0] != 2 {
		t.Errorf("bash loop first detected at cycle %d, want 2", loopDetected["bash"][0])
	}
	if len(loopDetected["bash"]) != 2 {
		t.Errorf("bash loop detected %d times, want 2 (cycles 2 and 3)", len(loopDetected["bash"]))
	}

	// read should trigger loop at cycle 3 (streak=3: cycles 2,3,4... but read changes at cycle 1→2, so streak starts at cycle 2)
	if len(loopDetected["read"]) == 0 {
		t.Fatal("read loop should have been detected")
	}
	if loopDetected["read"][0] != 3 {
		t.Errorf("read loop first detected at cycle %d, want 3", loopDetected["read"][0])
	}
}

func TestCallTracker_MaxTurnsIntegration(t *testing.T) {
	maxTurns := 5
	tracker := &callTracker{
		lastCall:    make(map[string]string),
		streakCount: make(map[string]int),
		threshold:   100, // High threshold so loop detection doesn't fire
	}

	// Simulate the processGeneration loop with max turns check
	var terminatedAt int
	for cycle := 1; cycle <= 20; cycle++ {
		if cycle > maxTurns {
			terminatedAt = cycle
			break
		}
		// Each cycle uses a unique input, so no loop detection
		tracker.Track("bash", fmt.Sprintf("command-%d", cycle))
	}

	if terminatedAt != maxTurns+1 {
		t.Errorf("loop should terminate at cycle %d, got %d", maxTurns+1, terminatedAt)
	}
}
