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

	// rows is the wrap table: one entry per display row, in order. It is the
	// only thing the probe is used for, and it is rebuilt only when the draft
	// or the width changes — so a keystroke that merely moves the cursor, and
	// every idle message the editor receives, costs nothing.
	rows []rowInfo
	// lines is value split on newlines, as runes, so column arithmetic needs no
	// further scanning.
	lines [][]rune
}

// rowInfo describes one display row: which logical line it belongs to and which
// slice of that line's runes it shows.
type rowInfo struct {
	line      int
	startCol  int
	runeCount int
}

// maxProbeRows bounds the wrap walk. A draft that somehow fails to terminate
// must not spin the render path forever.
const maxProbeRows = 20000

func newSelectionLayout() selectionLayout {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = " "
	ta.CharLimit = -1
	return selectionLayout{probe: ta}
}

// sync brings the probe in line with the real textarea and rebuilds the wrap
// table. outerWidth must be the same value the caller passed to the real
// textarea's SetWidth.
func (l *selectionLayout) sync(value string, outerWidth, height int) {
	if l.value == value && l.outerWidth == outerWidth && l.rows != nil {
		return
	}
	l.probe.SetWidth(outerWidth)
	l.probe.SetHeight(max(1, height))
	l.probe.SetValue(value)
	l.value = value
	l.outerWidth = outerWidth

	l.lines = nil
	for _, line := range strings.Split(value, "\n") {
		l.lines = append(l.lines, []rune(line))
	}
	l.buildRows()
}

// buildRows records how the textarea wraps each logical line.
//
// It loads one logical line into the probe at a time and enumerates that line's
// display rows by column, rather than walking the cursor down through the whole
// draft. Two reasons:
//
//   - The textarea wraps each logical line independently (its renderer calls
//     wrap() per line), so a line's layout does not depend on its neighbours.
//   - CursorDown moves one DISPLAY row, not one logical line, and on some
//     drafts — a line of trailing spaces was the one that caught it — a step
//     reports no progress while content remains. A walk that trusts it either
//     lands on the wrong line or stops early, and the highlight then paints
//     blank rows or vanishes entirely.
//
// The result is also linear in the draft's size. The first shape re-walked from
// the top once per preceding line, which measured in seconds per keystroke on a
// long pasted draft.
func (l *selectionLayout) buildRows() {
	l.rows = l.rows[:0]
	if l.probe.Width() <= 0 {
		return
	}

	for lineIdx, runes := range l.lines {
		l.probe.SetValue(string(runes))

		// Height is the authority for how many display rows a line occupies: it
		// is len(wrap(line)), the very slice the renderer iterates. Deriving the
		// count from the text instead misses the empty row the wrap can append
		// after a line that ends in spaces — a real case, and one that shifted
		// every following line's highlight up by a row.
		l.probe.SetCursorColumn(0)
		height := max(1, l.probe.LineInfo().Height)

		col := 0
		for range height {
			l.probe.SetCursorColumn(col)
			info := l.probe.LineInfo()

			// The wrap appends a trailing space to a line's last row; clamp so a
			// recorded column never points past the text it describes.
			count := info.Width
			if remaining := len(runes) - info.StartColumn; count > remaining {
				count = max(0, remaining)
			}
			l.rows = append(l.rows, rowInfo{line: lineIdx, startCol: info.StartColumn, runeCount: count})

			next := info.StartColumn + info.Width
			if next <= col {
				next = col + max(1, info.Width)
			}
			col = next
		}
	}

	// Restore the probe to the whole draft so anything else reading it sees the
	// same buffer the real textarea holds.
	l.probe.SetValue(l.value)
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
func (l *selectionLayout) spans(from, to, scrollY, promptWidth, viewHeight int, linewise bool) []selectionSpan {
	if from >= to || l.value == "" || l.contentWidth() <= 0 || len(l.rows) == 0 {
		return nil
	}

	startRow, startCol := l.position(from)
	last := l.lastOffsetBefore(to)
	endRow, endCol := l.position(last)
	endCol += l.cellWidthAt(last)

	if endRow < startRow || startRow < 0 {
		return nil
	}

	var out []selectionSpan
	for row := startRow; row <= endRow && row < len(l.rows); row++ {
		viewRow := row - scrollY
		if viewRow < 0 || (viewHeight > 0 && viewRow >= viewHeight) {
			continue
		}

		colFrom := 0
		colTo := l.rowExtent(row, linewise)
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

// rowExtent is how far a fully-selected display row is highlighted.
//
// A linewise selection covers the whole row, trailing blank cells included —
// that is what selecting a line means. A charwise selection stops at the text,
// plus one cell for the line ending when the row ends a logical line, which is
// how vim draws it; painting the full row would show a block of empty
// background the user never selected.
func (l *selectionLayout) rowExtent(row int, linewise bool) int {
	if linewise {
		return l.contentWidth()
	}
	r := l.rows[row]
	if r.line >= len(l.lines) {
		return l.contentWidth()
	}
	text := l.lines[r.line]
	end := min(r.startCol+r.runeCount, len(text))
	width := runeSpanWidth(text, r.startCol, end)
	if end >= len(text) && r.line < len(l.lines)-1 {
		width++ // the line ending occupies one cell
	}
	return min(width, l.contentWidth())
}

// position returns the absolute display row and column of a byte offset.
func (l *selectionLayout) position(offset int) (row, col int) {
	line, column := offsetToRuneLineCol(l.value, offset)
	for i, r := range l.rows {
		if r.line != line {
			continue
		}
		// The row that contains this column, or the line's last row when the
		// column sits at the very end.
		if column < r.startCol+r.runeCount || l.isLastRowOfLine(i) {
			text := l.lines[min(line, len(l.lines)-1)]
			return i, runeSpanWidth(text, r.startCol, min(column, len(text)))
		}
	}
	return 0, 0
}

func (l *selectionLayout) isLastRowOfLine(i int) bool {
	return i+1 >= len(l.rows) || l.rows[i+1].line != l.rows[i].line
}

// runeSpanWidth is the display width of text[from:to].
func runeSpanWidth(text []rune, from, to int) int {
	if from < 0 {
		from = 0
	}
	if to > len(text) {
		to = len(text)
	}
	if from >= to {
		return 0
	}
	return ansi.StringWidth(string(text[from:to]))
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
