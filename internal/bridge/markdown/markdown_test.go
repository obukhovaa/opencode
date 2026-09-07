package markdown

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// --- NormalizeSlackLinks -----------------------------------------------

func TestNormalizeSlackLinks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "labelled link",
			in:   "Triggered <https://tc/build|MySQL #12> on `c2`.",
			want: "Triggered [MySQL #12](https://tc/build) on `c2`.",
		},
		{
			name: "bare link",
			in:   "See <https://gitlab.com/piano/x/-/merge_requests/1> for details.",
			want: "See https://gitlab.com/piano/x/-/merge_requests/1 for details.",
		},
		{
			name: "empty label",
			in:   "<https://example.com|>",
			want: "https://example.com",
		},
		{
			name: "two labelled links",
			in:   "<https://a.test|A> and <https://b.test|B>",
			want: "[A](https://a.test) and [B](https://b.test)",
		},
		{
			name: "already-GFM content untouched",
			in:   "# Title\n\n- [MR !1](https://gitlab.com/x/-/merge_requests/1)\n- **bold**",
			want: "# Title\n\n- [MR !1](https://gitlab.com/x/-/merge_requests/1)\n- **bold**",
		},
		{
			name: "incidental angle brackets untouched",
			in:   "if a < b && b > c then <not a link>",
			want: "if a < b && b > c then <not a link>",
		},
		{
			name: "user mention untouched",
			in:   "cc <@U123456> please review",
			want: "cc <@U123456> please review",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeSlackLinks(tt.in)
			if got != tt.want {
				t.Errorf("NormalizeSlackLinks(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// --- Split ---------------------------------------------------------------

func TestSplit_FitsWithinLimitReturnsVerbatim(t *testing.T) {
	text := "hello world, this fits easily."
	chunks, truncated := Split(text, 1000, 0, TruncationMarker)
	if truncated {
		t.Fatalf("truncated = true, want false")
	}
	if len(chunks) != 1 || chunks[0] != text {
		t.Fatalf("chunks = %#v, want single verbatim chunk", chunks)
	}
}

func TestSplit_WhitespaceOnlyInputReturnsNil(t *testing.T) {
	chunks, truncated := Split("   \n\t  ", 10, 0, TruncationMarker)
	if chunks != nil || truncated {
		t.Fatalf("chunks=%#v truncated=%v, want (nil, false)", chunks, truncated)
	}
	chunks, truncated = Split("", 10, 0, TruncationMarker)
	if chunks != nil || truncated {
		t.Fatalf("chunks=%#v truncated=%v, want (nil, false) for empty input", chunks, truncated)
	}
}

func TestSplit_FencesBalancedAcrossChunks(t *testing.T) {
	text := buildParagraphsWithFence(40, 60)
	chunks, _ := Split(text, 120, 0, TruncationMarker)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if n := countBacktickToggles(c); n%2 != 0 {
			t.Errorf("chunk %d has odd fence-toggle count %d (not independently valid): %q", i, n, c)
		}
	}
}

func TestSplit_NoContentLossWhenNotTruncated(t *testing.T) {
	text := buildParagraphsWithFence(40, 60)
	chunks, truncated := Split(text, 120, 0, TruncationMarker)
	if truncated {
		t.Fatalf("truncated = true, want false (maxChunks unlimited)")
	}
	var got []string
	for _, c := range chunks {
		got = append(got, wordsSkippingFenceLines(c)...)
	}
	want := wordsSkippingFenceLines(text)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("word list mismatch after chunking:\ngot:  %v\nwant: %v", got, want)
	}
}

func TestSplit_FenceSpanningBoundaryReopensWithSameInfo(t *testing.T) {
	intro := "Intro paragraph with some words here to fill the buffer nicely."
	var codeLines strings.Builder
	for i := 0; i < 60; i++ {
		codeLines.WriteString(fmt.Sprintf("line %02d of code content here\n", i))
	}
	text := intro + "\n\n```python\n" + codeLines.String() + "```\n\nOutro paragraph text."

	chunks, truncated := Split(text, 200, 0, TruncationMarker)
	if truncated {
		t.Fatalf("truncated = true, want false")
	}
	if len(chunks) < 2 {
		t.Fatalf("expected the fence to force a split, got %d chunk(s)", len(chunks))
	}

	foundReopen := false
	for i := 0; i < len(chunks)-1; i++ {
		lines := strings.Split(chunks[i], "\n")
		last := strings.TrimSpace(lines[len(lines)-1])
		if last != "```" {
			continue
		}
		nextFirst := strings.SplitN(chunks[i+1], "\n", 2)[0]
		if nextFirst != "```python" {
			t.Errorf("chunk %d closes a fence but chunk %d reopens as %q, want %q", i, i+1, nextFirst, "```python")
		}
		foundReopen = true
	}
	if !foundReopen {
		t.Fatalf("no chunk boundary closed+reopened the fence; test fixture/limit needs adjustment")
	}

	// Every chunk must independently be fence-balanced.
	for i, c := range chunks {
		if n := countBacktickToggles(c); n%2 != 0 {
			t.Errorf("chunk %d not fence-balanced (toggles=%d): %q", i, n, c)
		}
	}
}

func TestSplit_MaxChunksHardCap(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		sb.WriteString(strings.Repeat("word ", 20))
		sb.WriteString("\n\n")
	}
	text := sb.String()
	limit := 100

	chunks, truncated := Split(text, limit, 3, TruncationMarker)
	if len(chunks) != 3 {
		t.Fatalf("len(chunks) = %d, want 3 (hard cap)", len(chunks))
	}
	if !truncated {
		t.Fatalf("truncated = false, want true")
	}
	last := chunks[len(chunks)-1]
	if !strings.HasSuffix(last, TruncationMarker) {
		t.Errorf("last chunk does not end with marker: %q", last)
	}
	lastRunes := utf8.RuneCountInString(last)
	if lastRunes > limit {
		t.Errorf("last chunk rune length %d exceeds limit %d", lastRunes, limit)
	}
	if lastRunes <= limit/2 {
		t.Errorf("last chunk rune length %d did not pack near the limit (limit/2=%d)", lastRunes, limit/2)
	}
	for i, c := range chunks {
		if n := utf8.RuneCountInString(c); n > limit {
			t.Errorf("chunk %d rune length %d exceeds limit %d", i, n, limit)
		}
	}
}

func TestSplit_RuneSafetyMultiByte(t *testing.T) {
	text := strings.Repeat("h\u00e9llo \U0001F600 w\u00f6rld ", 900)
	limit := 200

	chunks, truncated := Split(text, limit, 0, TruncationMarker)
	if truncated {
		t.Fatalf("truncated = true, want false (maxChunks unlimited)")
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for a long repeated string")
	}
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Errorf("chunk %d is not valid UTF-8: %q", i, c)
		}
		if n := utf8.RuneCountInString(c); n > limit {
			t.Errorf("chunk %d rune length %d exceeds limit %d", i, n, limit)
		}
	}
}

func TestSplit_TruncationMarkerNeverInsideFence(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 500; i++ {
		body.WriteString("x\n")
	}
	text := "```go\n" + body.String() + "```\n"
	total := utf8.RuneCountInString(text)

	for limit := 5; limit <= total; limit++ {
		chunks, truncated := Split(text, limit, 1, TruncationMarker)
		if len(chunks) == 0 {
			continue
		}
		last := chunks[len(chunks)-1]
		if truncated && !strings.HasSuffix(last, TruncationMarker) {
			t.Fatalf("limit=%d: truncated chunk missing marker: %q", limit, tail(last, 40))
		}
		if n := countBacktickToggles(last); n%2 != 0 {
			t.Fatalf("limit=%d: unbalanced fence around marker (toggles=%d): %q", limit, n, tail(last, 60))
		}
	}
}

// --- BuildBlockChunks ------------------------------------------------------

func TestBuildBlockChunks_TruncatesWithinBudget(t *testing.T) {
	text := strings.Repeat("word ", 3000) // ~15,000 runes, over the 12,000 budget
	chunks, truncated := BuildBlockChunks(text, 12000, 3000, TruncationMarker)
	if !truncated {
		t.Fatalf("truncated = false, want true")
	}
	if len(chunks) > 4 {
		t.Fatalf("len(chunks) = %d, want <= 4", len(chunks))
	}
	sum := 0
	for _, c := range chunks {
		sum += utf8.RuneCountInString(c)
	}
	if sum > 12000 {
		t.Errorf("sum(runeLen) = %d, want <= 12000", sum)
	}
	if !strings.HasSuffix(chunks[len(chunks)-1], TruncationMarker) {
		t.Errorf("last chunk does not end with truncation marker")
	}
}

func TestBuildBlockChunks_ShortInputSingleChunkNoTruncation(t *testing.T) {
	chunks, truncated := BuildBlockChunks("hello **world**", 12000, 3000, TruncationMarker)
	if truncated {
		t.Fatalf("truncated = true, want false")
	}
	if len(chunks) != 1 || chunks[0] != "hello **world**" {
		t.Fatalf("chunks = %#v, want single verbatim chunk", chunks)
	}
}

func TestBuildBlockChunks_NormalizesLinks(t *testing.T) {
	chunks, _ := BuildBlockChunks("<https://x.test|y>", 12000, 3000, TruncationMarker)
	if len(chunks) != 1 || chunks[0] != "[y](https://x.test)" {
		t.Fatalf("chunks = %#v, want normalized link", chunks)
	}
}

func TestBuildBlockChunks_ClampsNonPositiveBudget(t *testing.T) {
	chunks, truncated := BuildBlockChunks("hello", 0, 0, TruncationMarker)
	if truncated || len(chunks) != 1 || chunks[0] != "hello" {
		t.Fatalf("zero budget: chunks=%#v truncated=%v, want single verbatim chunk", chunks, truncated)
	}
	chunks, truncated = BuildBlockChunks("hello", -5, -5, TruncationMarker)
	if truncated || len(chunks) != 1 || chunks[0] != "hello" {
		t.Fatalf("negative budget: chunks=%#v truncated=%v, want single verbatim chunk", chunks, truncated)
	}
}

// --- test helpers ----------------------------------------------------------

// buildParagraphsWithFence constructs a multi-paragraph markdown document
// (blank-line separated) containing one fenced code block, long enough
// that a small limit forces multiple chunks.
func buildParagraphsWithFence(paragraphs, wordsPerParagraph int) string {
	var sb strings.Builder
	for i := 0; i < paragraphs; i++ {
		for j := 0; j < wordsPerParagraph; j++ {
			fmt.Fprintf(&sb, "p%dw%d ", i, j)
			// Wrap every few words so no single line is long enough to
			// force a hard-wrap (which is explicitly allowed to split a
			// line's content and would legitimately break the
			// word-preservation invariant this fixture is meant to
			// exercise via the newline/blank-line boundary tiers only).
			if (j+1)%8 == 0 {
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n\n")
		if i == paragraphs/2 {
			sb.WriteString("```text\n")
			for k := 0; k < 10; k++ {
				fmt.Fprintf(&sb, "code line %d\n", k)
			}
			sb.WriteString("```\n\n")
		}
	}
	return sb.String()
}

// isFenceDelimiterLine reports whether line is nothing but a fence
// delimiter (3+ backticks, optionally followed by an info string) — used
// to skip both original and chunker-synthesized delimiter lines when
// comparing word content across chunk boundaries.
func isFenceDelimiterLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return len(trimmed) >= 3 && strings.HasPrefix(trimmed, "```")
}

// wordsSkippingFenceLines returns the whitespace-separated words of text,
// skipping any line that is purely a fence delimiter.
func wordsSkippingFenceLines(text string) []string {
	var words []string
	for _, line := range strings.Split(text, "\n") {
		if isFenceDelimiterLine(line) {
			continue
		}
		words = append(words, strings.Fields(line)...)
	}
	return words
}

// countBacktickToggles counts lines that open or close a ``` fence. This
// is an independent (deliberately simpler) check from the package's own
// fenceState/scanFence machinery, used to assert chunks are individually
// fence-balanced without relying on the code under test to grade itself.
func countBacktickToggles(text string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if isFenceDelimiterLine(line) {
			n++
		}
	}
	return n
}

func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
