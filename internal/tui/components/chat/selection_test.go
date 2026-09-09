package chat

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/tui/vim"
)

// TestSelectionLayoutMatchesTheRealTextarea is what pins the highlight to the
// bubbles textarea's own soft-wrap.
//
// The probe reproduces the real widget's layout only because both run the same
// unexported wrap at the same width. Nothing in the type system enforces that.
// If a bubbles upgrade changes the wrap algorithm, this test goes red — which
// is the point. Without it the highlight would quietly drift onto the wrong
// columns and nobody would learn why.
func TestSelectionLayoutMatchesTheRealTextarea(t *testing.T) {
	values := []string{
		"short",
		"one two three four five six seven eight nine ten eleven twelve",
		"first line\nsecond line that is quite a lot longer than the first one\nthird",
		"a-very-long-unbroken-token-that-cannot-be-word-wrapped-at-all-so-it-must-break-mid-word",
		"trailing spaces   \nand another line",
		"日本語のテキストと English mixed together in one long line that wraps",
	}
	widths := []int{10, 20, 40, 80}

	for _, value := range values {
		for _, width := range widths {
			real := textarea.New()
			real.ShowLineNumbers = false
			real.Prompt = " "
			real.CharLimit = -1
			real.SetWidth(width)
			real.SetHeight(20)
			real.SetValue(value)

			layout := newSelectionLayout()
			layout.sync(value, width, 20)

			lines := strings.Split(value, "\n")
			for line := range lines {
				for col := 0; col <= len([]rune(lines[line])); col++ {
					real.MoveToBegin()
					for range line {
						real.CursorDown()
					}
					real.SetCursorColumn(col)
					want := real.LineInfo()

					layout.moveProbe(line, col)
					got := layout.probe.LineInfo()

					if got.RowOffset != want.RowOffset || got.CharOffset != want.CharOffset || got.Height != want.Height {
						t.Fatalf("probe diverged from the textarea at width=%d line=%d col=%d value=%q\n got RowOffset=%d CharOffset=%d Height=%d\nwant RowOffset=%d CharOffset=%d Height=%d",
							width, line, col, value,
							got.RowOffset, got.CharOffset, got.Height,
							want.RowOffset, want.CharOffset, want.Height)
					}
				}
			}
		}
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

	spans := layout.spans(6, 11, 0, textareaPromptWidth, 5)
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
	spans := layout.spans(2, len(value), 0, textareaPromptWidth, 6)
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
	spans := layout.spans(0, len(value), 2, textareaPromptWidth, 3)
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
	if spans := layout.spans(3, 3, 0, textareaPromptWidth, 5); spans != nil {
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
