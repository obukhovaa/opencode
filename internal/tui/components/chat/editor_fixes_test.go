package chat

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/opencode-ai/opencode/internal/skill"
	"github.com/opencode-ai/opencode/internal/slashcmd"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/vim"
)

// TestStageInvocationKeepsTrailingTextOnItsOwnLine: staging inserts at the
// cursor, so text to its right would otherwise share the invocation's line and
// be parsed as its arguments — turning the rest of a sentence into an argument.
func TestStageInvocationKeepsTrailingTextOnItsOwnLine(t *testing.T) {
	ed := newTestEditor()
	ed.textarea.SetValue("hello world")
	ed.textarea.SetCursorColumn(5) // between "hello" and " world"

	ed.stageInvocation("/commit ")

	want := "hello\n/commit \n world"
	if got := ed.textarea.Value(); got != want {
		t.Fatalf("value = %q, want %q", got, want)
	}
	// The cursor must sit at the end of the staged line so typed arguments land
	// there rather than on the pushed-down text.
	if got := ed.textarea.Value(); strings.Split(got, "\n")[ed.textarea.Line()] != "/commit " {
		t.Errorf("cursor on line %d (%q), want the staged line",
			ed.textarea.Line(), strings.Split(got, "\n")[ed.textarea.Line()])
	}
	if got, want := ed.textarea.Column(), len("/commit "); got != want {
		t.Errorf("cursor column = %d, want %d", got, want)
	}
}

// TestStageInvocationAtEndOfLineAddsNoTrailingNewline: the common case — cursor
// at the end of the draft — must be unchanged by the trailing-text handling.
func TestStageInvocationAtEndOfLineAddsNoTrailingNewline(t *testing.T) {
	tests := []struct {
		name  string
		draft string
		col   int
		want  string
	}{
		{"empty editor", "", 0, "/commit "},
		{"end of a line", "hello", 5, "hello\n/commit "},
		{"start of an empty line", "hello\n", 0, "hello\n/commit "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ed := newTestEditor()
			ed.textarea.SetValue(tt.draft)
			ed.textarea.SetCursorColumn(tt.col)

			ed.stageInvocation("/commit ")

			if got := ed.textarea.Value(); got != tt.want {
				t.Errorf("value = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestAffordanceRowMatchesInputRowWidth: the affordance row and the input row
// are stacked by JoinVertical, which pads the shorter one with unstyled cells.
// A row wider than the input leaves a black column down the right edge of the
// editor — the background-gap pitfall in CLAUDE.md.
func TestAffordanceRowMatchesInputRowWidth(t *testing.T) {
	for _, width := range []int{20, 40, 80, 120} {
		ed := newTestEditor()
		ed.SetSize(width, 4)
		ed.recognized = []recognizedInvocation{{label: "/commit"}}

		inputRow := lipgloss.Width(lipgloss.JoinHorizontal(
			lipgloss.Top,
			lipgloss.NewStyle().Padding(0, 0, 0, 1).Bold(true).Render(">"),
			ed.textarea.View(),
		))
		if got := lipgloss.Width(ed.affordanceRow()); got != inputRow {
			t.Errorf("width %d: affordance row = %d cols, input row = %d cols", width, got, inputRow)
		}
	}
}

// TestEditorViewCellsCarryBackground: shell mode sets a textarea placeholder,
// which switches the textarea to a render path that leaves the rows below the
// placeholder padded with unstyled cells — they draw with the terminal's default
// background, a black rectangle under the `$` prompt (the background-gap pitfall
// in CLAUDE.md).
func TestEditorViewCellsCarryBackground(t *testing.T) {
	tests := []struct {
		name   string
		shell  bool
		visual bool
		value  string
	}{
		{name: "normal empty"},
		{name: "normal with draft", value: "hello"},
		{name: "shell empty", shell: true},
		{name: "shell with command", shell: true, value: "ls -la"},
		// The selection overlay rewrites rendered cells, so it has to satisfy
		// the same background contract as every other render path.
		{name: "visual selection", visual: true, value: "hello world"},
		{name: "visual selection wrapping", visual: true, value: "the quick brown fox jumps over the lazy dog"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ed := newTestEditor()
			ed.selectionLayout = newSelectionLayout()
			ed.SetSize(40, 3)
			if tt.shell {
				ed.enterShellMode()
			}
			ed.textarea.SetValue(tt.value)
			if tt.visual {
				ed.vimHandler = vim.NewHandler()
				ed.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
				ed.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
				for range 12 {
					ed.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
				}
				if len(ed.selection) == 0 {
					t.Fatal("no selection to render")
				}
			}

			for i, line := range strings.Split(ed.View().Content, "\n") {
				if col := firstCellOnDefaultBackground(line); col >= 0 {
					t.Errorf("line %d: cell %d renders with the default background: %q", i, col, line)
				}
			}
		})
	}
}

// firstCellOnDefaultBackground returns the index of the first cell in line the
// terminal draws on its default background, or -1 when none does. A cell drawn
// before any background is set inherits the one of the container the editor is
// rendered into, so only a reset (or an explicit 49) after which no background
// is set again puts a cell on the terminal's own background.
func firstCellOnDefaultBackground(line string) int {
	var (
		bgReset bool
		cell    int
	)
	for i := 0; i < len(line); {
		if strings.HasPrefix(line[i:], "\x1b[") {
			end := strings.IndexByte(line[i:], 'm')
			if end < 0 {
				break
			}
			for _, param := range strings.Split(line[i+2:i+end], ";") {
				switch param {
				case "", "0", "49": // reset, or explicit default background
					bgReset = true
				case "48": // 48;5;N or 48;2;R;G;B
					bgReset = false
				default:
					if n, err := strconv.Atoi(param); err == nil &&
						((n >= 40 && n <= 47) || (n >= 100 && n <= 107)) {
						bgReset = false
					}
				}
			}
			i += end + 1
			continue
		}
		_, size := utf8.DecodeRuneInString(line[i:])
		if bgReset {
			return cell
		}
		cell++
		i += size
	}
	return -1
}

// TestRecognitionChipForRejectedInvocation: a skill that resolves but may not be
// invoked rejects the whole submission, so it must not look like a line that
// will simply be sent as prose — the absence of a chip means exactly that.
func TestRecognitionChipForRejectedInvocation(t *testing.T) {
	notInvocable := false
	reg := slashcmd.Registry{
		Skills: []skill.Info{
			{
				Name:          "internal",
				Location:      "/skills/internal/SKILL.md",
				Content:       "Internal only.",
				UserInvocable: &notInvocable,
			},
		},
	}
	ed := newTestEditor()
	ed.SetSize(80, 4)
	ed.scan = func(text string) []slashcmd.Invocation { return slashcmd.Scan(text, reg) }

	ed.textarea.SetValue("/skill:internal\nplease do the thing")
	ed.refreshRecognition()

	if len(ed.recognized) != 1 {
		t.Fatalf("recognized = %#v, want one chip", ed.recognized)
	}
	if !ed.recognized[0].rejects {
		t.Errorf("chip = %#v, want it marked as rejecting", ed.recognized[0])
	}
	if ed.recognized[0].label != "/skill:internal" {
		t.Errorf("label = %q", ed.recognized[0].label)
	}
	// And it still renders within budget.
	if got, want := lipgloss.Width(ed.affordanceRow()), ed.rowWidth(); got != want {
		t.Errorf("affordance row = %d cols, want %d", got, want)
	}
}

// TestThemeChangeDoesNotShrinkTheEditor guards a bug that predates the
// selection work: CreateTextArea seeds a replacement textarea by passing the
// outgoing one's INNER width into SetWidth, which subtracts the prompt
// reservation again. Every theme change narrowed the input by a column, and the
// loss was permanent — switch themes ten times and ten columns are gone.
//
// It matters doubly now: the selection probe derives its wrap from the width
// the editor believes it set, so a silently shrinking textarea would also drift
// the highlight off the selected cells.
func TestThemeChangeDoesNotShrinkTheEditor(t *testing.T) {
	ed := newTestEditor()
	ed.selectionLayout = newSelectionLayout()
	ed.SetSize(40, 3)

	want := ed.textarea.Width()
	wantOuter := ed.textareaOuterWidth

	for i := range 5 {
		ed.Update(dialog.ThemeChangedMsg{})
		if got := ed.textarea.Width(); got != want {
			t.Fatalf("after %d theme change(s) textarea width = %d, want %d", i+1, got, want)
		}
		if ed.textareaOuterWidth != wantOuter {
			t.Fatalf("after %d theme change(s) the probe's width source = %d, want %d",
				i+1, ed.textareaOuterWidth, wantOuter)
		}
	}
}

// TestThemeChangeKeepsTheSelection: CreateTextArea rebuilds the widget with
// SetValue, which drops the cursor at the end of the buffer. The vim handler's
// anchor survives that, so without restoring the cursor a theme change silently
// collapses an active selection to a single character.
func TestThemeChangeKeepsTheSelection(t *testing.T) {
	ed := newTestEditor()
	ed.selectionLayout = newSelectionLayout()
	ed.vimHandler = vim.NewHandler()
	ed.SetSize(40, 3)
	ed.textarea.SetValue("aaaa bbbb cccc dddd")

	ed.Update(tea.KeyPressMsg{Code: tea.KeyEscape}) // NORMAL
	for range 30 {
		ed.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	}
	ed.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	for range 3 {
		ed.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	}

	from, to, _, active := ed.vimHandler.Selection(&ed.textarea)
	if !active {
		t.Fatal("no selection before the theme change")
	}
	want := ed.textarea.Value()[from:to]

	ed.Update(dialog.ThemeChangedMsg{})

	from, to, _, active = ed.vimHandler.Selection(&ed.textarea)
	if !active {
		t.Fatal("selection lost across the theme change")
	}
	if got := ed.textarea.Value()[from:to]; got != want {
		t.Errorf("selection = %q after the theme change, want %q", got, want)
	}
}
