package hooks

import (
	"context"
	"testing"
)

// BenchmarkRunPreTool_NoHooksConfigured measures the cost of firing
// PreToolUse when no hooks exist for the event — the common case for
// users without `.opencode.json` hooks. Should be a handful of ns;
// regression here means the hot path grew accidental work (e.g.
// JSON parsing moved upstream of the empty-groups check, or a new
// allocation slipped into the always-on path).
func BenchmarkRunPreTool_NoHooksConfigured(b *testing.B) {
	reg := NewRegistry(func() map[string][]MatcherGroup { return nil }, b.TempDir())
	ctx := context.Background()
	input := map[string]any{"command": "ls"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = reg.RunPreTool(ctx, "sess", "/tmp", "bash", input)
	}
}

// BenchmarkHasEvent_NoHooks measures the fast-path used by the agent
// hot loop to skip JSON unmarshal + os.Getwd when no hooks fire.
// Regression here means the agent loop overhead grew when hooks are
// unused.
func BenchmarkHasEvent_NoHooks(b *testing.B) {
	reg := NewRegistry(func() map[string][]MatcherGroup { return nil }, b.TempDir())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = reg.HasEvent(EventPreToolUse)
	}
}

// BenchmarkCompileMatcher_Cached locks in the per-event matcher cache:
// repeated calls with the same matcher string MUST be a sync.Map hit,
// not a regex compile. A regression doubles the cost per matcher per
// event for users with regex matchers configured.
func BenchmarkCompileMatcher_Cached(b *testing.B) {
	// Prime the cache with one regex pattern.
	_, _ = CompileMatcher("^memory_")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = CompileMatcher("^memory_")
	}
}

// BenchmarkCompileMatcher_Uncached measures the cold-path cost — what
// every call would pay without the cache. Used as the baseline so the
// _Cached benchmark's win is legible.
func BenchmarkCompileMatcher_Uncached(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = compileMatcherUncached("^memory_")
	}
}

// BenchmarkMatchEntries_FiveRegexes mirrors a realistic plugin-stack
// config: a handful of regex matchers iterated on every tool call.
// Without the matcher cache this would be 5 × regexp.Compile per call;
// with the cache it's 5 × sync.Map.Load + 5 × MatchString.
func BenchmarkMatchEntries_FiveRegexes(b *testing.B) {
	groups := []MatcherGroup{
		{Matcher: "^bash$"},
		{Matcher: "^memory_"},
		{Matcher: "^(edit|write|multiedit)$"},
		{Matcher: "(?i)^Bash$"},
		{Matcher: "^read$"},
	}
	// Prime the cache for all five.
	for _, g := range groups {
		_, _ = CompileMatcher(g.Matcher)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = matchEntries(groups, "bash")
	}
}
