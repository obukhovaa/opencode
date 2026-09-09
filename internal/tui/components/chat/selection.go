package chat

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

// selectionSpan is one display row's worth of highlight, in the coordinates of
// the editor's rendered view.
type selectionSpan struct {
	row      int // display row within the rendered textarea view
	from, to int // display columns, [from, to)
}

// selectionLayout maps byte offsets in the draft onto display coordinates.
//
// The textarea has no selection API and no public accessor for its soft-wrap
// layout: `wrap` and `memoizedWrap` are unexported. What IS public is
// LineInfo(), which is derived from that same wrap — so a throwaway textarea
// with the same width and value can be asked where an offset lands, and its
// answer is authoritative for the real one.
//
// A throwaway is required rather than probing the real widget. Moving a
// textarea's cursor calls repositionView, which mutates its viewport; and
// because Model.viewport is a POINTER, even a copied Model shares it. Probing
// the real textarea would scroll the input under the user as a side effect of
// asking a question about it.
type selectionLayout struct {
	probe textarea.Model
	value string
	// outerWidth is the value passed to textarea.SetWidth, NOT the width the
	// textarea reports back. SetWidth subtracts its own prompt and frame
	// reservations, so feeding Width() back in would shrink the probe a second
	// time and its wrap would no longer match the real widget's.
	outerWidth int
}

func newSelectionLayout() selectionLayout {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = " "
	ta.CharLimit = -1
	return selectionLayout{probe: ta}
}

// sync brings the probe in line with the real textarea. outerWidth must be the
// same value the caller passed to the real textarea's SetWidth. Re-setting the
// value is what invalidates the probe's wrap cache, so it is skipped when
// nothing that affects layout has changed.
func (l *selectionLayout) sync(value string, outerWidth, height int) {
	if l.value == value && l.outerWidth == outerWidth {
		return
	}
	l.probe.SetWidth(outerWidth)
	l.probe.SetHeight(max(1, height))
	l.probe.SetValue(value)
	l.value = value
	l.outerWidth = outerWidth
}

// contentWidth is the probe's inner width — the number of columns a wrapped
// display row spans, which is what a full-row highlight has to cover.
func (l *selectionLayout) contentWidth() int {
	return l.probe.Width()
}

// spans returns the highlight for the byte range [from, to), expressed in the
// rendered view's coordinates. scrollY is the real textarea's scroll offset and
// promptWidth the columns the textarea's own prompt occupies on every row.
//
// Rows scrolled out of view are dropped rather than clamped: a clamped row
// would paint the highlight onto whatever line happens to be at the top.
func (l *selectionLayout) spans(from, to, scrollY, promptWidth, viewHeight int) []selectionSpan {
	if from >= to || l.value == "" || l.contentWidth() <= 0 {
		return nil
	}

	startRow, startCol := l.position(from)
	// `to` is exclusive, so the last selected cell is the one before it.
	endRow, endCol := l.position(l.lastOffsetBefore(to))
	endCol += l.cellWidthAt(l.lastOffsetBefore(to))

	if endRow < startRow {
		return nil
	}

	var out []selectionSpan
	for row := startRow; row <= endRow; row++ {
		viewRow := row - scrollY
		if viewRow < 0 || (viewHeight > 0 && viewRow >= viewHeight) {
			continue
		}
		colFrom := 0
		colTo := l.contentWidth()
		if row == startRow {
			colFrom = startCol
		}
		if row == endRow {
			colTo = endCol
		}
		if colTo <= colFrom {
			continue
		}
		out = append(out, selectionSpan{
			row:  viewRow,
			from: colFrom + promptWidth,
			to:   colTo + promptWidth,
		})
	}
	return out
}

// position returns the absolute display row and column of a byte offset.
func (l *selectionLayout) position(offset int) (row, col int) {
	line, column := offsetToRuneLineCol(l.value, offset)

	// Absolute row = the wrapped height of every preceding logical line, plus
	// the offset's own row within its line.
	for i := 0; i < line; i++ {
		row += l.heightOf(i)
	}
	l.moveProbe(line, column)
	info := l.probe.LineInfo()
	return row + info.RowOffset, info.CharOffset
}

// heightOf returns how many display rows logical line i occupies.
func (l *selectionLayout) heightOf(line int) int {
	l.moveProbe(line, 0)
	if h := l.probe.LineInfo().Height; h > 0 {
		return h
	}
	return 1
}

// moveProbe places the probe's cursor at a logical (line, column).
func (l *selectionLayout) moveProbe(line, column int) {
	// MoveToBegin resets to (0,0) so the vertical delta is unambiguous; the
	// probe is a throwaway, so the scrolling this causes affects nothing.
	l.probe.MoveToBegin()
	for i := 0; i < line; i++ {
		l.probe.CursorDown()
	}
	l.probe.SetCursorColumn(column)
}

// lastOffsetBefore returns the offset of the last rune starting before to.
func (l *selectionLayout) lastOffsetBefore(to int) int {
	if to <= 0 {
		return 0
	}
	if to > len(l.value) {
		to = len(l.value)
	}
	last := 0
	for i := range l.value {
		if i >= to {
			break
		}
		last = i
	}
	return last
}

// cellWidthAt returns the display width of the rune at a byte offset.
func (l *selectionLayout) cellWidthAt(offset int) int {
	if offset < 0 || offset >= len(l.value) {
		return 1
	}
	r := []rune(l.value[offset:])
	if len(r) == 0 {
		return 1
	}
	if w := ansi.StringWidth(string(r[0])); w > 0 {
		return w
	}
	return 1
}

// offsetToRuneLineCol converts a byte offset to the (line, rune column) pair
// the textarea indexes by — its columns count runes, not bytes.
func offsetToRuneLineCol(text string, offset int) (line, col int) {
	if offset > len(text) {
		offset = len(text)
	}
	head := text[:offset]
	line = strings.Count(head, "\n")
	if idx := strings.LastIndexByte(head, '\n'); idx >= 0 {
		head = head[idx+1:]
	}
	return line, len([]rune(head))
}

// applySelection paints the spans onto a rendered textarea view.
func applySelection(view string, spans []selectionSpan) string {
	if len(spans) == 0 {
		return view
	}
	t := theme.CurrentTheme()
	style := lipgloss.NewStyle().
		Background(t.TextMuted()).
		Foreground(t.Background())

	lines := strings.Split(view, "\n")
	for _, span := range spans {
		if span.row < 0 || span.row >= len(lines) {
			continue
		}
		lines[span.row] = styles.RestyleRange(lines[span.row], span.from, span.to, style)
	}
	return strings.Join(lines, "\n")
}
