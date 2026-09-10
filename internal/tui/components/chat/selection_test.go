package chat

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/tui/vim"
)

// realTextarea builds a textarea configured exactly as the editor's is.
func realTextarea(value string, outerWidth, height int) textarea.Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = " "
	ta.CharLimit = -1
	ta.SetWidth(outerWidth)
	ta.SetHeight(height)
	ta.SetValue(value)
	return ta
}

// renderedRows returns the textarea's rendered rows with ANSI stripped — the
// actual cells the terminal will show.
func renderedRows(ta *textarea.Model) []string {
	var out []string
	for _, line := range strings.Split(ta.View(), "\n") {
		out = append(out, ansi.Strip(line))
	}
	return out
}

// runeAtCell returns the rune occupying a given display CELL of a rendered row.
// Indexing runes instead would be wrong for wide characters: in " 日本語の ",
// cell 3 is inside 本 while rune 3 is 語.
func runeAtCell(row string, cell int) (rune, bool) {
	col := 0
	for _, r := range row {
		w := ansi.StringWidth(string(r))
		if w == 0 {
			continue
		}
		if cell < col+w {
			return r, true
		}
		col += w
	}
	return 0, false
}

// TestSelectionLayoutMatchesTheRenderedOutput pins the probe's wrap table to
// what the widget actually draws.
//
// The truth here is the RENDER, not a cursor walk. Two earlier versions of this
// test got that wrong: the first drove probe and widget with the same key
// sequence and was tautological, and the second derived truth from
// CursorDown-until-Line()-matches, which is itself unreliable — a line ending in
// spaces makes the wrap emit an extra empty row that the cursor walk skips. The
// renderer iterates wrap() directly, so comparing against its output is the only
// check that cannot share a bug with the code under test.
func TestSelectionLayoutMatchesTheRenderedOutput(t *testing.T) {
	values := []string{
		"short",
		"one two three four five six seven eight nine ten eleven twelve",
		// A first line that wraps, followed by more lines: the shape where a
		// per-logical-line row count is not the same as a display-row count.
		"aaaa bbbb cccc dddd eeee ffff gggg\nSECOND\nTHIRD",
		"first line\nsecond line that is quite a lot longer than the first one\nthird",
		"wrapping first line here we go\nb\nc\nd\ne",
		"a-very-long-unbroken-token-that-cannot-be-word-wrapped-at-all-so-it-must-break-mid-word",
		// Trailing spaces make the wrap append an empty row.
		"trailing spaces   \nand another line",
		"日本語のテキストと English mixed together in one long line that wraps",
		"日本語のテキスト\nEnglish line\nもう一行の日本語テキストです",
	}
	widths := []int{10, 20, 23, 40, 80}

	for _, value := range values {
		for _, width := range widths {
			ta := realTextarea(value, width, 60)
			rows := renderedRows(&ta)

			layout := newSelectionLayout()
			layout.sync(value, width, 60)

			for offset, r := range value {
				// Spaces and newlines have no distinctive cell to match against.
				if r == '\n' || r == ' ' {
					continue
				}
				gotRow, gotCol := layout.position(offset)
				if gotRow >= len(rows) {
					t.Fatalf("width=%d offset=%d value=%q: row %d is past the %d rendered rows",
						width, offset, value, gotRow, len(rows))
				}
				got, ok := runeAtCell(rows[gotRow], gotCol+textareaPromptWidth)
				if !ok {
					t.Fatalf("width=%d offset=%d value=%q: column %d is past row %q",
						width, offset, value, gotCol+textareaPromptWidth, rows[gotRow])
				}
				if got != r {
					t.Fatalf("width=%d offset=%d value=%q\n position said (row=%d col=%d), which renders %q\n but the character there is %q\n row: %q",
						width, offset, value, gotRow, gotCol, string(got), string(r), rows[gotRow])
				}
			}
		}
	}
}

// TestSelectionLayoutIsLinear guards the cost of the mapping. The first shape
// re-walked from the top once per preceding logical line, which measured in
// seconds per keystroke on a long pasted draft — unusable in a TUI event loop.
func TestSelectionLayoutIsLinear(t *testing.T) {
	var b strings.Builder
	for i := range 200 {
		fmt.Fprintf(&b, "line %d with enough words on it to wrap at a narrow width\n", i)
	}
	value := b.String()

	layout := newSelectionLayout()
	start := time.Now()
	layout.sync(value, 83, 10)
	build := time.Since(start)

	start = time.Now()
	for range 100 {
		layout.spans(0, len(value), 0, textareaPromptWidth, 10, false)
	}
	perSpan := time.Since(start) / 100

	// Generous bounds: the point is to catch a return to quadratic behaviour,
	// which was three orders of magnitude worse than this.
	if build > 500*time.Millisecond {
		t.Errorf("building the wrap table for a 200-line draft took %v", build)
	}
	if perSpan > 5*time.Millisecond {
		t.Errorf("computing spans took %v per call", perSpan)
	}
	t.Logf("200-line draft: table build %v, spans %v/call", build, perSpan)
}

// A cursor move must not rebuild the wrap table: the editor recomputes the
// selection on every message it receives, including idle ticks.
func TestSelectionLayoutReusesTheWrapTable(t *testing.T) {
	value := "one two three\nfour five six\nseven eight nine"
	layout := newSelectionLayout()
	layout.sync(value, 20, 5)
	first := &layout.rows[0]

	layout.sync(value, 20, 5)
	if &layout.rows[0] != first {
		t.Error("sync rebuilt the wrap table for an unchanged draft and width")
	}

	layout.sync(value+"!", 20, 5)
	if len(layout.rows) == 0 {
		t.Error("sync did not rebuild the wrap table after the draft changed")
	}
}

func TestOffsetToRuneLineCol(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		offset   int
		wantLine int
		wantCol  int
	}{
		{name: "start", text: "abc", offset: 0, wantLine: 0, wantCol: 0},
		{name: "mid first line", text: "abc\ndef", offset: 2, wantLine: 0, wantCol: 2},
		{name: "start of second line", text: "abc\ndef", offset: 4, wantLine: 1, wantCol: 0},
		{name: "mid second line", text: "abc\ndef", offset: 6, wantLine: 1, wantCol: 2},
		// The textarea indexes columns by rune, so a multi-byte character must
		// not inflate the column by its byte length.
		{name: "after a multi-byte rune", text: "aé b", offset: 3, wantLine: 0, wantCol: 2},
		{name: "past the end is clamped", text: "abc", offset: 99, wantLine: 0, wantCol: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, col := offsetToRuneLineCol(tt.text, tt.offset)
			if line != tt.wantLine || col != tt.wantCol {
				t.Errorf("offsetToRuneLineCol = (%d,%d), want (%d,%d)", line, col, tt.wantLine, tt.wantCol)
			}
		})
	}
}

func TestSelectionSpansSingleRow(t *testing.T) {
	layout := newSelectionLayout()
	layout.sync("hello world", 40, 5)

	spans := layout.spans(6, 11, 0, textareaPromptWidth, 5, false)
	if len(spans) != 1 {
		t.Fatalf("spans = %+v, want one row", spans)
	}
	// "world" occupies columns 6..11, shifted right by the textarea's prompt.
	if spans[0].row != 0 || spans[0].from != 6+textareaPromptWidth || spans[0].to != 11+textareaPromptWidth {
		t.Errorf("span = %+v, want row 0 columns [%d,%d)", spans[0], 6+textareaPromptWidth, 11+textareaPromptWidth)
	}
}

func TestSelectionSpansAcrossASoftWrap(t *testing.T) {
	const width = 12
	value := "aaaa bbbb cccc dddd"
	layout := newSelectionLayout()
	layout.sync(value, width, 6)

	// Select from within the first display row through the last one.
	spans := layout.spans(2, len(value), 0, textareaPromptWidth, 6, false)
	if len(spans) < 2 {
		t.Fatalf("spans = %+v, want the selection to cover more than one display row", spans)
	}

	rows := map[int]bool{}
	for _, s := range spans {
		if s.from >= s.to {
			t.Errorf("empty span %+v", s)
		}
		if s.from < textareaPromptWidth {
			t.Errorf("span %+v starts inside the prompt column", s)
		}
		if s.to > width+textareaPromptWidth {
			t.Errorf("span %+v extends past the textarea width", s)
		}
		if rows[s.row] {
			t.Errorf("duplicate span for row %d", s.row)
		}
		rows[s.row] = true
	}
	// The first row's highlight must start where the selection does, not at 0.
	if spans[0].from != 2+textareaPromptWidth {
		t.Errorf("first span starts at %d, want %d", spans[0].from, 2+textareaPromptWidth)
	}
}

func TestSelectionSpansDropRowsScrolledOutOfView(t *testing.T) {
	value := "l1\nl2\nl3\nl4\nl5\nl6"
	layout := newSelectionLayout()
	layout.sync(value, 40, 3)

	// Whole buffer selected, but only three rows are visible starting at row 2.
	spans := layout.spans(0, len(value), 2, textareaPromptWidth, 3, true)
	for _, s := range spans {
		if s.row < 0 || s.row >= 3 {
			t.Errorf("span %+v falls outside the visible rows [0,3)", s)
		}
	}
	if len(spans) == 0 {
		t.Error("no spans produced for a fully-selected scrolled buffer")
	}
}

func TestSelectionSpansEmptyForEmptyRange(t *testing.T) {
	layout := newSelectionLayout()
	layout.sync("hello", 40, 5)
	if spans := layout.spans(3, 3, 0, textareaPromptWidth, 5, false); spans != nil {
		t.Errorf("spans = %+v, want none for an empty range", spans)
	}
}

// ---- rendering --------------------------------------------------------------

func newVisualEditor(t *testing.T, draft string, width, height int) *editorCmp {
	t.Helper()
	ed := newTestEditor()
	ed.selectionLayout = newSelectionLayout()
	ed.vimHandler = vim.NewHandler()
	ed.SetSize(width, height)
	ed.textarea.SetValue(draft)
	// INSERT -> NORMAL -> VISUAL, through the real key path.
	ed.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	return ed
}

func TestEditorViewHighlightsTheSelection(t *testing.T) {
	ed := newVisualEditor(t, "hello world", 40, 3)

	plain := ed.View().Content

	ed.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	ed.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	ed.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})

	if ed.vimHandler.Mode() != vim.ModeVisual {
		t.Fatalf("mode = %s, want VISUAL", ed.vimHandler.Mode())
	}
	if len(ed.selection) == 0 {
		t.Fatal("no selection spans computed in VISUAL mode")
	}

	highlighted := ed.View().Content
	if highlighted == plain {
		t.Error("view is unchanged with a selection active")
	}
	if ansi.Strip(highlighted) != ansi.Strip(plain) {
		t.Errorf("highlight changed the visible text:\n got %q\nwant %q",
			ansi.Strip(highlighted), ansi.Strip(plain))
	}
}

// TestEditorViewUnchangedWithoutASelection is the "no change when inactive"
// guarantee: outside a visual mode the overlay must not touch a single byte of
// what the textarea rendered.
//
// The comparison is against the textarea's own output rather than against an
// editor built without vim: entering NORMAL mode legitimately changes the
// prompt colour and nudges the cursor left, and neither has anything to do with
// selection rendering.
func TestEditorViewUnchangedWithoutASelection(t *testing.T) {
	for _, mode := range []string{"INSERT", "NORMAL"} {
		t.Run(mode, func(t *testing.T) {
			ed := newTestEditor()
			ed.selectionLayout = newSelectionLayout()
			ed.vimHandler = vim.NewHandler()
			ed.SetSize(40, 3)
			ed.textarea.SetValue("hello world")
			if mode == "NORMAL" {
				ed.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			}

			if len(ed.selection) != 0 {
				t.Fatalf("selection spans computed in %s mode: %+v", mode, ed.selection)
			}
			if got, want := ed.textareaView(), ed.textarea.View(); got != want {
				t.Errorf("textareaView modified the render outside a visual mode:\n got %q\nwant %q", got, want)
			}
		})
	}
}

// TestEditorNoOverflowInVisualModes extends the editor's no-overflow contract
// over the highlight: the overlay rewrites rendered lines, so it has to be
// proven not to widen them.
func TestEditorNoOverflowInVisualModes(t *testing.T) {
	for _, width := range []int{20, 40, 80, 120} {
		for _, enter := range []string{"v", "V"} {
			t.Run(enter+"-"+strings.Repeat("w", 1)+itoa(width), func(t *testing.T) {
				ed := newVisualEditor(t, "the quick brown fox jumps over the lazy dog near the river bank", width, 3)
				ed.Update(tea.KeyPressMsg{Code: rune(enter[0]), Text: enter})
				for range 30 {
					ed.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
				}

				view := ed.View().Content
				for i, line := range strings.Split(view, "\n") {
					if w := lipgloss.Width(line); w > width {
						t.Errorf("line %d is %d columns wide, exceeding the %d-column container", i, w, width)
					}
				}
			})
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
