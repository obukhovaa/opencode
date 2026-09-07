package markdown

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// splitWithTimeout runs Split on a watchdog. Split used to be able to spin
// forever on inputs where the synthetic fence-reopen delimiter was at least
// as long as the slice consumed per pass, so a plain call would hang the
// whole test binary rather than fail it.
func splitWithTimeout(t *testing.T, text string, limit, maxChunks int, marker string) []string {
	t.Helper()
	type result struct {
		chunks []string
	}
	done := make(chan result, 1)
	go func() {
		c, _ := Split(text, limit, maxChunks, marker)
		done <- result{chunks: c}
	}()
	select {
	case r := <-done:
		return r.chunks
	case <-time.After(5 * time.Second):
		t.Fatalf("Split did not terminate within 5s (limit=%d, maxChunks=%d) — forward-progress regression", limit, maxChunks)
		return nil
	}
}

// TestSplitTerminatesOnLongFenceInfoString covers the non-termination bug:
// a fence whose info string rivals the chunk limit produced a reopen
// delimiter longer than the content consumed, so `remaining` grew every
// pass instead of shrinking.
func TestSplitTerminatesOnLongFenceInfoString(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		limit int
	}{
		{
			name:  "info string longer than limit",
			text:  "```" + strings.Repeat("x", 200) + "\ncode body here\n```\ntail",
			limit: 100,
		},
		{
			name:  "info string near the telegram limit",
			text:  "```" + strings.Repeat("x", 3600) + "\ncode\n```\ntail",
			limit: 3500,
		},
		{
			name:  "limit smaller than the fence delimiter itself",
			text:  "````\nabc",
			limit: 4,
		},
		{
			name:  "limit of one rune with an open fence",
			text:  "```go\nabcdef\n```",
			limit: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chunks := splitWithTimeout(t, tc.text, tc.limit, 0, TruncationMarker)
			if len(chunks) == 0 {
				t.Fatal("Split returned no chunks")
			}
			for i, c := range chunks {
				if !utf8.ValidString(c) {
					t.Errorf("chunk %d is not valid UTF-8: %q", i, c)
				}
				if n := utf8.RuneCountInString(c); n > tc.limit {
					t.Errorf("chunk %d has %d runes, limit is %d: %q", i, n, tc.limit, c)
				}
			}
		})
	}
}

// TestSplitPreservesEveryWordOnLongFenceInfoString asserts the
// non-termination fix degrades formatting only — never content.
func TestSplitPreservesEveryWordOnLongFenceInfoString(t *testing.T) {
	body := "alpha bravo charlie delta echo foxtrot golf hotel india juliet"
	text := "```" + strings.Repeat("x", 200) + "\n" + body + "\n```\ntail words here"

	chunks := splitWithTimeout(t, text, 100, 0, TruncationMarker)

	joined := strings.Join(chunks, "")
	for _, w := range strings.Fields(body + " tail words here") {
		if !strings.Contains(joined, w) {
			t.Errorf("word %q lost across chunk boundaries; joined = %q", w, joined)
		}
	}
}

// TestSplitClampsReopenedFenceInfoString documents that the info string is
// clamped only on the SYNTHETIC reopen delimiter — the original opening
// line always survives verbatim.
func TestSplitClampsReopenedFenceInfoString(t *testing.T) {
	info := strings.Repeat("y", maxFenceInfoRunes+40)
	text := "```" + info + "\n" + strings.Repeat("code line\n", 40) + "```"

	chunks := splitWithTimeout(t, text, 200, 0, TruncationMarker)
	if len(chunks) < 2 {
		t.Fatalf("expected the input to split, got %d chunk(s)", len(chunks))
	}
	if !strings.Contains(chunks[0], "```"+info) {
		t.Errorf("first chunk lost the original opening delimiter: %q", chunks[0])
	}
	for i, c := range chunks[1:] {
		for _, line := range strings.Split(c, "\n") {
			trimmed := strings.TrimLeft(line, " \t")
			n := countLeadingBackticks(trimmed)
			if n < 3 {
				continue
			}
			if got := utf8.RuneCountInString(strings.TrimSpace(trimmed[n:])); got > maxFenceInfoRunes {
				t.Errorf("chunk %d reopened a fence with a %d-rune info string, max is %d",
					i+1, got, maxFenceInfoRunes)
			}
		}
	}
}
