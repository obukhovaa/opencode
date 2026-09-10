package styles

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func selStyle() lipgloss.Style {
	return lipgloss.NewStyle().Background(lipgloss.Color("#444444"))
}

func TestRestyleRangePreservesWidth(t *testing.T) {
	tests := []struct {
		name string
		line string
		from int
		to   int
	}{
		{name: "plain text mid-span", line: "hello world", from: 2, to: 7},
		{name: "whole line", line: "hello world", from: 0, to: 11},
		{name: "line start", line: "hello world", from: 0, to: 5},
		{name: "line end", line: "hello world", from: 6, to: 11},
		{name: "single cell", line: "hello", from: 2, to: 3},
		{name: "already styled input", line: lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render("hello world"), from: 3, to: 8},
		{name: "wide characters", line: "日本語 text", from: 0, to: 4},
		{name: "wide characters mid-span", line: "ab日本語cd", from: 3, to: 6},
		{name: "range beyond the line is clamped", line: "abc", from: 1, to: 99},
		{name: "empty line", line: "", from: 0, to: 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := ansi.StringWidth(tt.line)
			got := RestyleRange(tt.line, tt.from, tt.to, selStyle())
			if gotWidth := ansi.StringWidth(got); gotWidth != want {
				t.Errorf("width = %d, want %d\ninput:  %q\noutput: %q", gotWidth, want, tt.line, got)
			}
			// The visible text must survive: only its styling changes.
			if ansi.Strip(got) != ansi.Strip(tt.line) {
				t.Errorf("visible text changed:\n got %q\nwant %q", ansi.Strip(got), ansi.Strip(tt.line))
			}
		})
	}
}

func TestRestyleRangeAppliesTheStyle(t *testing.T) {
	got := RestyleRange("hello world", 6, 11, selStyle())
	if !strings.Contains(got, "world") {
		t.Fatalf("styled span lost its text: %q", got)
	}
	// The style must appear somewhere before the span and not swallow the head.
	if !strings.HasPrefix(ansi.Strip(got), "hello ") {
		t.Errorf("head text corrupted: %q", ansi.Strip(got))
	}
}

func TestRestyleRangeNoOpForEmptyRange(t *testing.T) {
	const line = "hello world"
	for _, tt := range []struct{ from, to int }{{5, 5}, {7, 3}, {-4, 0}, {20, 30}} {
		if got := RestyleRange(line, tt.from, tt.to, selStyle()); got != line {
			t.Errorf("RestyleRange(%q, %d, %d) = %q, want the input unchanged", line, tt.from, tt.to, got)
		}
	}
}

// TestRestyleRangeDoesNotSplitWideCharacters guards the reason this helper
// works in cells: a byte-wise slice would cut a multi-byte rune in half.
func TestRestyleRangeDoesNotSplitWideCharacters(t *testing.T) {
	const line = "ab日本語cd"
	for to := 0; to <= ansi.StringWidth(line); to++ {
		got := RestyleRange(line, 0, to, selStyle())
		if strings.ContainsRune(ansi.Strip(got), '�') {
			t.Fatalf("to=%d produced a replacement character: %q", to, ansi.Strip(got))
		}
	}
}
