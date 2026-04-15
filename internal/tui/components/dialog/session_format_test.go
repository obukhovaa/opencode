package dialog

import (
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/session"
)

func TestFormatRelativeTime(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		offset  time.Duration
		want    string
		skipEq  bool // when true, check prefix-match instead of exact (for YYYY-MM-DD)
		wantFmt string
	}{
		{name: "future clamps to just now", offset: -1 * time.Hour, want: "just now"},
		{name: "zero", offset: 0, want: "just now"},
		{name: "59 seconds", offset: 59 * time.Second, want: "just now"},
		{name: "60 seconds", offset: 60 * time.Second, want: "1m"},
		{name: "59 minutes", offset: 59 * time.Minute, want: "59m"},
		{name: "60 minutes", offset: 60 * time.Minute, want: "1h"},
		{name: "23 hours", offset: 23 * time.Hour, want: "23h"},
		{name: "24 hours", offset: 24 * time.Hour, want: "1d"},
		{name: "6 days", offset: 6 * 24 * time.Hour, want: "6d"},
		{name: "7 days", offset: 7 * 24 * time.Hour, want: "1w"},
		{name: "27 days", offset: 27 * 24 * time.Hour, want: "3w"},
		{
			name:    "28 days falls to absolute",
			offset:  28 * 24 * time.Hour,
			skipEq:  true,
			wantFmt: "2006-01-02",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			updatedAt := now.Add(-tc.offset).Unix()
			got := formatRelativeTime(updatedAt, now)
			if tc.skipEq {
				// Must match layout YYYY-MM-DD; re-parsing it proves that.
				if _, err := time.Parse(tc.wantFmt, got); err != nil {
					t.Errorf("expected absolute date, got %q (err: %v)", got, err)
				}
				return
			}
			if got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1000, "1.0k"},
		{1500, "1.5k"},
		{12_345, "12.3k"},
		{999_999, "1000.0k"}, // rounds just below the M boundary; acceptable
		{1_000_000, "1.0M"},
		{1_500_000, "1.5M"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := formatTokenCount(tc.n)
			if got != tc.want {
				t.Errorf("formatTokenCount(%d) = %q, want %q", tc.n, got, tc.want)
			}
		})
	}
}

func TestFormatSessionMetadata_DropsZeroTokens(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	s := session.Session{
		UpdatedAt:        now.Add(-5 * time.Minute).Unix(),
		PromptTokens:     0,
		CompletionTokens: 0,
	}
	got := formatSessionMetadata(s, now)
	want := "updated 5m"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func TestFormatSessionMetadata_WithTokens(t *testing.T) {
	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	s := session.Session{
		UpdatedAt:        now.Add(-2 * time.Hour).Unix(),
		PromptTokens:     4300,
		CompletionTokens: 1200,
	}
	got := formatSessionMetadata(s, now)
	want := "updated 2h • 4.3k / 1.2k tokens"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func TestFilterAndSelectionStability(t *testing.T) {
	sessions := []session.Session{
		{ID: "s1", Title: "Refactor auth module"},
		{ID: "s2", Title: "Add cron scheduling"},
		{ID: "s3", Title: "Fix cron test failure"},
		{ID: "s4", Title: "Document API"},
	}

	d := NewSessionDialogCmp().(*sessionDialogCmp)
	d.SetSessions(sessions)

	// Initially the full list is visible and the first session is selected.
	if got := len(d.visibleSessions()); got != 4 {
		t.Fatalf("expected 4 visible sessions, got %d", got)
	}
	if d.selectedSessionID != "s1" {
		t.Fatalf("expected s1 selected initially, got %q", d.selectedSessionID)
	}

	// User highlights s3, then searches for "cron".
	d.selectedIdx = 2
	d.selectedSessionID = "s3"
	d.query.SetValue("cron")
	d.filter()

	if got := len(d.visibleSessions()); got != 2 {
		t.Fatalf("expected 2 matching sessions, got %d", got)
	}
	// s3 is still present — selection must stay on s3.
	if d.selectedSessionID != "s3" {
		t.Errorf("selection should remain on s3, got %q", d.selectedSessionID)
	}
	if d.selectedIdx != 1 {
		t.Errorf("s3 should be at index 1 in filtered list, got %d", d.selectedIdx)
	}

	// Narrow the query further so s3 drops out — selectedIdx falls back to
	// the first surviving match, but selectedSessionID remains "s3" so the
	// original selection can be restored if the user widens the query again.
	d.query.SetValue("add cron")
	d.filter()
	if got := len(d.visibleSessions()); got != 1 {
		t.Fatalf("expected 1 matching session, got %d", got)
	}
	if d.selectedSessionID != "s3" {
		t.Errorf("expected remembered id to stay on s3, got %q", d.selectedSessionID)
	}
	if d.selectedIdx != 0 {
		t.Errorf("expected visible index to fall back to 0, got %d", d.selectedIdx)
	}

	// Clearing the query restores the full list and the remembered s3 is
	// highlighted again at its original position.
	d.query.SetValue("")
	d.filter()
	if got := len(d.visibleSessions()); got != 4 {
		t.Fatalf("expected 4 sessions after clear, got %d", got)
	}
	if d.selectedSessionID != "s3" {
		t.Errorf("selection should snap back to s3 after clearing query, got %q", d.selectedSessionID)
	}
	if d.selectedIdx != 2 {
		t.Errorf("expected s3 at index 2, got %d", d.selectedIdx)
	}
}

func TestFilterCaseInsensitive(t *testing.T) {
	d := NewSessionDialogCmp().(*sessionDialogCmp)
	d.SetSessions([]session.Session{
		{ID: "a", Title: "JIRA Ticket 123"},
		{ID: "b", Title: "random session"},
	})

	d.query.SetValue("jira")
	d.filter()
	if got := len(d.visibleSessions()); got != 1 {
		t.Fatalf("expected 1 match, got %d", got)
	}
	if d.visibleSessions()[0].ID != "a" {
		t.Errorf("expected session a, got %q", d.visibleSessions()[0].ID)
	}
}

func TestLayoutStableAcrossFilterChanges(t *testing.T) {
	// Dialog width and slot count must be driven by the full session list,
	// not by the filtered view — otherwise the modal shrinks and blinks as
	// the user types in the search bar.
	d := NewSessionDialogCmp().(*sessionDialogCmp)
	d.width = 120
	d.height = 40
	d.SetSessions([]session.Session{
		{ID: "s1", Title: "a very long session title that dominates width"},
		{ID: "s2", Title: "short"},
		{ID: "s3", Title: "another short one"},
	})

	wBefore := d.contentWidth
	slotsBefore := d.maxVisible

	// Narrow to only the short-titled session.
	d.query.SetValue("short")
	d.filter()

	if d.contentWidth != wBefore {
		t.Errorf("contentWidth changed on filter: before=%d after=%d", wBefore, d.contentWidth)
	}
	if d.maxVisible != slotsBefore {
		t.Errorf("maxVisible changed on filter: before=%d after=%d", slotsBefore, d.maxVisible)
	}

	// Clear filter — still stable.
	d.query.SetValue("")
	d.filter()
	if d.contentWidth != wBefore {
		t.Errorf("contentWidth changed on clear: before=%d after=%d", wBefore, d.contentWidth)
	}
	if d.maxVisible != slotsBefore {
		t.Errorf("maxVisible changed on clear: before=%d after=%d", slotsBefore, d.maxVisible)
	}
}

func TestFilterEmptyResult(t *testing.T) {
	d := NewSessionDialogCmp().(*sessionDialogCmp)
	d.SetSessions([]session.Session{
		{ID: "a", Title: "hello"},
	})

	d.query.SetValue("xyz")
	d.filter()
	if got := len(d.visibleSessions()); got != 0 {
		t.Fatalf("expected 0 matches, got %d", got)
	}
}
