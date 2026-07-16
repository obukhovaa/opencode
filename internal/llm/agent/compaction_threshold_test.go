package agent

import "testing"

// TestEffectiveCompactionThreshold covers the small helper that resolves
// RunOptions.CompactionThreshold against the global default. The behaviour
// contract is:
//   - zero override → return AutoCompactionThreshold (opt-out is implicit).
//   - override in (0, 1] → return the override verbatim.
//   - override < 0 → clamp to AutoCompactionThreshold (misconfigured flow
//     shouldn't silently disable compaction).
//   - override > 1 → clamp to 1 (any positive value beyond full context is
//     equivalent to "compact only on hard limit").
func TestEffectiveCompactionThreshold(t *testing.T) {
	cases := []struct {
		name     string
		override float64
		want     float64
	}{
		{"zero returns default", 0, AutoCompactionThreshold},
		{"0.7 override", 0.7, 0.7},
		{"1.0 upper bound", 1.0, 1.0},
		{"0.01 lower bound", 0.01, 0.01},
		{"negative clamps to default", -0.5, AutoCompactionThreshold},
		{"greater than 1 clamps to 1", 1.5, 1.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := effectiveCompactionThreshold(c.override)
			if got != c.want {
				t.Errorf("effectiveCompactionThreshold(%v) = %v, want %v", c.override, got, c.want)
			}
		})
	}
}
