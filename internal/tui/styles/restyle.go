package styles

import (
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// RestyleRange re-renders the display cells [from, to) of an already-rendered
// line with style, leaving everything outside that span untouched.
//
// It works in display cells, not bytes: the input carries ANSI escape sequences
// and may contain characters two columns wide, so byte slicing would both cut
// escape sequences in half and land the highlight on the wrong columns.
//
// The rendered width is preserved exactly. That is load-bearing: the editor's
// no-overflow invariant is asserted against its final output, so an overlay
// that widened a line by a single cell would break the layout contract at every
// terminal width.
//
// Styling inside the span is replaced rather than merged — the span is stripped
// before being re-rendered. The editor draws draft text in one foreground
// colour, so there is nothing to preserve; a caller that renders multi-coloured
// text through this helper would need a merging variant instead.
func RestyleRange(line string, from, to int, style lipgloss.Style) string {
	width := ansi.StringWidth(line)
	if from < 0 {
		from = 0
	}
	if to > width {
		to = width
	}
	if from >= to || width == 0 {
		return line
	}

	head := ansi.Cut(line, 0, from)
	span := ansi.Strip(ansi.Cut(line, from, to))
	tail := ansi.Cut(line, to, width)

	return head + style.Render(span) + tail
}
