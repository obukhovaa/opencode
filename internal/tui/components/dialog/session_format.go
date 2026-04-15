package dialog

import (
	"fmt"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/session"
)

// formatRelativeTime returns a compact, human-readable representation of the
// duration between updatedAtUnix (Unix seconds) and now. Returns "just now"
// for deltas under a minute, falls back to an absolute YYYY-MM-DD beyond 4
// weeks, and clamps negative deltas to zero.
func formatRelativeTime(updatedAtUnix int64, now time.Time) string {
	updated := time.Unix(updatedAtUnix, 0)
	delta := max(now.Sub(updated), 0)

	switch {
	case delta < time.Minute:
		return "just now"
	case delta < time.Hour:
		return fmt.Sprintf("%dm", int(delta/time.Minute))
	case delta < 24*time.Hour:
		return fmt.Sprintf("%dh", int(delta/time.Hour))
	case delta < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(delta/(24*time.Hour)))
	case delta < 28*24*time.Hour:
		return fmt.Sprintf("%dw", int(delta/(7*24*time.Hour)))
	default:
		return updated.Format("2006-01-02")
	}
}

// formatTokenCount returns a short human-readable token count: raw digits
// below 1000, "N.Nk" between 1_000 and 999_999, "N.NM" above 1_000_000.
func formatTokenCount(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
}

// formatSessionMetadata composes the metadata line shown under a session
// title. Sections are joined with " • " and are skipped entirely when empty,
// so a freshly created session renders as just "updated just now".
func formatSessionMetadata(s session.Session, now time.Time) string {
	sections := make([]string, 0, 2)

	sections = append(sections, "updated "+formatRelativeTime(s.UpdatedAt, now))

	if s.PromptTokens > 0 || s.CompletionTokens > 0 {
		sections = append(sections, fmt.Sprintf(
			"%s / %s tokens",
			formatTokenCount(s.PromptTokens),
			formatTokenCount(s.CompletionTokens),
		))
	}

	return strings.Join(sections, " • ")
}
