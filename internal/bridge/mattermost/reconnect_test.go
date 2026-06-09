package mattermost

import "testing"

// TestBackoffDelayCappedAt30s pins the curve at the standard breakpoints
// so a refactor that changes the formula trips the test instead of
// silently changing the reconnect cadence in production.
func TestBackoffDelayCappedAt30s(t *testing.T) {
	t.Parallel()
	cases := []struct {
		attempt    int
		minSeconds float64
		maxSeconds float64
	}{
		// attempt 1: 1s base + 0-500ms jitter
		{1, 1.0, 1.5},
		// attempt 5: 16s base + 0-500ms jitter
		{5, 16.0, 16.5},
		// attempt 10+: capped at 30s + jitter
		{10, 30.0, 30.5},
		{20, 30.0, 30.5},
	}
	for _, tc := range cases {
		got := backoffDelay(tc.attempt).Seconds()
		if got < tc.minSeconds || got > tc.maxSeconds {
			t.Errorf("backoffDelay(%d) = %.2fs, want [%.2f, %.2f]", tc.attempt, got, tc.minSeconds, tc.maxSeconds)
		}
	}
}

// TestMaxReconnectAttemptsConstant guards the spec-defined 20-attempt cap.
// Changes to this number must be coordinated with the bridge-http-api
// "Mattermost reconnect loop" spec scenario.
func TestMaxReconnectAttemptsConstant(t *testing.T) {
	t.Parallel()
	if MaxReconnectAttempts != 20 {
		t.Errorf("MaxReconnectAttempts = %d, spec demands 20", MaxReconnectAttempts)
	}
}
