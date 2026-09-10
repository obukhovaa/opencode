package vim

import (
	"testing"
	"unicode/utf8"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

// newVisualTestHarness returns a handler in NORMAL mode over a textarea holding
// text, with the cursor at the given byte offset.
func newVisualTestHarness(t *testing.T, text string, cursor int) (*Handler, *textarea.Model) {
	t.Helper()
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = " "
	ta.CharLimit = -1
	ta.SetWidth(80)
	ta.SetHeight(5)
	ta.SetValue(text)
	ta.Focus()

	h := NewHandler()
	h.state = VimState{Mode: ModeNormal, Command: CommandIdle{}}
	h.setCursorPosition(&ta, text, cursor)
	return h, &ta
}

// press feeds a sequence of single-character keys through the handler.
func press(h *Handler, ta *textarea.Model, keys ...string) {
	for _, k := range keys {
		msg := tea.KeyPressMsg{Text: k}
		if len(k) == 1 {
			msg.Code = rune(k[0])
		}
		switch k {
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "ctrl+c":
			msg = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
		}
		h.HandleKey(msg, ta)
	}
}

func cursorOffset(h *Handler, ta *textarea.Model) int {
	return lineColToOffset(ta.Value(), ta.Line(), ta.Column())
}

// ---- mode transitions -------------------------------------------------------

func TestVisualModeTransitions(t *testing.T) {
	t.Run("v enters VISUAL anchored at the cursor", func(t *testing.T) {
		h, ta := newVisualTestHarness(t, "one two three", 4)
		press(h, ta, "v")
		if h.Mode() != ModeVisual {
			t.Fatalf("mode = %s, want VISUAL", h.Mode())
		}
		if h.state.Anchor != 4 {
			t.Errorf("anchor = %d, want 4", h.state.Anchor)
		}
	})

	t.Run("V enters VISUAL LINE", func(t *testing.T) {
		h, ta := newVisualTestHarness(t, "one\ntwo", 1)
		press(h, ta, "V")
		if h.Mode() != ModeVisualLine {
			t.Fatalf("mode = %s, want V-LINE", h.Mode())
		}
	})

	t.Run("esc returns to NORMAL leaving the text alone", func(t *testing.T) {
		h, ta := newVisualTestHarness(t, "one two three", 4)
		press(h, ta, "v", "l", "l", "esc")
		if h.Mode() != ModeNormal {
			t.Fatalf("mode = %s, want NORMAL", h.Mode())
		}
		if got := ta.Value(); got != "one two three" {
			t.Errorf("text = %q, want it unchanged", got)
		}
	})

	t.Run("ctrl+c returns to NORMAL", func(t *testing.T) {
		h, ta := newVisualTestHarness(t, "one two three", 4)
		press(h, ta, "v", "ctrl+c")
		if h.Mode() != ModeNormal {
			t.Errorf("mode = %s, want NORMAL", h.Mode())
		}
	})

	t.Run("v in VISUAL exits", func(t *testing.T) {
		h, ta := newVisualTestHarness(t, "one two", 0)
		press(h, ta, "v", "v")
		if h.Mode() != ModeNormal {
			t.Errorf("mode = %s, want NORMAL", h.Mode())
		}
	})

	t.Run("V in VISUAL switches to V-LINE keeping the anchor", func(t *testing.T) {
		h, ta := newVisualTestHarness(t, "one two three", 4)
		press(h, ta, "v", "l", "l")
		anchor := h.state.Anchor
		press(h, ta, "V")
		if h.Mode() != ModeVisualLine {
			t.Fatalf("mode = %s, want V-LINE", h.Mode())
		}
		if h.state.Anchor != anchor {
			t.Errorf("anchor = %d, want it preserved at %d", h.state.Anchor, anchor)
		}
	})

	t.Run("ConsumesCtrlC in visual modes", func(t *testing.T) {
		h, ta := newVisualTestHarness(t, "one", 0)
		press(h, ta, "v")
		if !h.ConsumesCtrlC() {
			t.Error("VISUAL must consume ctrl+c so it never raises the quit dialog")
		}
	})
}

// ---- selection extension ----------------------------------------------------

func TestVisualSelectionExtension(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		cursor   int
		keys     []string
		wantFrom int
		wantTo   int
	}{
		{
			name: "single character", text: "abc", cursor: 0,
			keys: []string{"v"}, wantFrom: 0, wantTo: 1,
		},
		{
			name: "extend right", text: "abcdef", cursor: 0,
			keys: []string{"v", "l", "l"}, wantFrom: 0, wantTo: 3,
		},
		{
			name: "extend left from the middle", text: "abcdef", cursor: 3,
			keys: []string{"v", "h", "h"}, wantFrom: 1, wantTo: 4,
		},
		{
			name: "word motion", text: "one two three", cursor: 0,
			keys: []string{"v", "w"}, wantFrom: 0, wantTo: 5,
		},
		{
			name: "counted motion", text: "abcdef", cursor: 0,
			keys: []string{"v", "3", "l"}, wantFrom: 0, wantTo: 4,
		},
		{
			name: "end of line", text: "one two", cursor: 0,
			keys: []string{"v", "$"}, wantFrom: 0, wantTo: 7,
		},
		{
			name: "find character", text: "one two three", cursor: 0,
			keys: []string{"v", "f", "t"}, wantFrom: 0, wantTo: 5,
		},
		{
			name: "linewise covers the whole line", text: "hello\nworld", cursor: 2,
			keys: []string{"V"}, wantFrom: 0, wantTo: 6,
		},
		{
			name: "linewise across two lines", text: "hello\nworld", cursor: 2,
			keys: []string{"V", "j"}, wantFrom: 0, wantTo: 11,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, ta := newVisualTestHarness(t, tt.text, tt.cursor)
			press(h, ta, tt.keys...)

			from, to, _, active := h.Selection(ta)
			if !active {
				t.Fatal("no active selection")
			}
			if from != tt.wantFrom || to != tt.wantTo {
				t.Errorf("selection = [%d,%d) %q, want [%d,%d) %q",
					from, to, tt.text[from:to], tt.wantFrom, tt.wantTo, tt.text[tt.wantFrom:tt.wantTo])
			}
		})
	}
}

func TestVisualSwapEnds(t *testing.T) {
	h, ta := newVisualTestHarness(t, "abcdef", 1)
	press(h, ta, "v", "l", "l") // anchor 1, cursor 3

	from, to, _, _ := h.Selection(ta)
	press(h, ta, "o")

	if got := cursorOffset(h, ta); got != 1 {
		t.Errorf("cursor after o = %d, want the former anchor (1)", got)
	}
	if h.state.Anchor != 3 {
		t.Errorf("anchor after o = %d, want the former cursor (3)", h.state.Anchor)
	}
	gotFrom, gotTo, _, _ := h.Selection(ta)
	if gotFrom != from || gotTo != to {
		t.Errorf("o changed the selected range: [%d,%d) -> [%d,%d)", from, to, gotFrom, gotTo)
	}
	// Motions now move the end that used to be the anchor.
	press(h, ta, "h")
	gotFrom, _, _, _ = h.Selection(ta)
	if gotFrom != 0 {
		t.Errorf("after o then h, selection starts at %d, want 0", gotFrom)
	}
}

// ---- operators --------------------------------------------------------------

func TestVisualOperators(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		cursor       int
		keys         []string
		wantText     string
		wantMode     VimMode
		wantRegister string
		wantLinewise bool
	}{
		{
			name: "delete a selection", text: "one two three", cursor: 4,
			keys:     []string{"v", "l", "l", "l", "d"},
			wantText: "one three", wantMode: ModeNormal, wantRegister: "two ",
		},
		{
			name: "x deletes like d", text: "abcdef", cursor: 0,
			keys:     []string{"v", "l", "x"},
			wantText: "cdef", wantMode: ModeNormal, wantRegister: "ab",
		},
		{
			name: "change leaves INSERT", text: "one two", cursor: 0,
			keys:     []string{"v", "l", "l", "c"},
			wantText: " two", wantMode: ModeInsert, wantRegister: "one",
		},
		{
			name: "s changes like c", text: "one two", cursor: 0,
			keys:     []string{"v", "l", "s"},
			wantText: "e two", wantMode: ModeInsert, wantRegister: "on",
		},
		{
			name: "yank leaves the text alone", text: "one two", cursor: 0,
			keys:     []string{"v", "l", "l", "y"},
			wantText: "one two", wantMode: ModeNormal, wantRegister: "one",
		},
		{
			name: "linewise yank marks the register linewise", text: "hello\nworld", cursor: 0,
			keys:     []string{"V", "y"},
			wantText: "hello\nworld", wantMode: ModeNormal, wantRegister: "hello\n", wantLinewise: true,
		},
		{
			name: "linewise delete", text: "one\ntwo\nthree", cursor: 4,
			keys:     []string{"V", "d"},
			wantText: "one\nthree", wantMode: ModeNormal, wantRegister: "two\n", wantLinewise: true,
		},
		{
			name: "uppercase", text: "hello world", cursor: 0,
			keys:     []string{"v", "l", "l", "l", "l", "U"},
			wantText: "HELLO world", wantMode: ModeNormal,
		},
		{
			name: "lowercase", text: "HELLO world", cursor: 0,
			keys:     []string{"v", "l", "l", "l", "l", "u"},
			wantText: "hello world", wantMode: ModeNormal,
		},
		{
			name: "toggle case", text: "hello", cursor: 0,
			keys:     []string{"v", "l", "~"},
			wantText: "HEllo", wantMode: ModeNormal,
		},
		{
			name: "indent", text: "one\ntwo", cursor: 0,
			keys:     []string{"V", "j", ">"},
			wantText: "  one\n  two", wantMode: ModeNormal,
		},
		{
			name: "unindent", text: "  one\n  two", cursor: 0,
			keys:     []string{"V", "j", "<"},
			wantText: "one\ntwo", wantMode: ModeNormal,
		},
		{
			name: "join", text: "one\ntwo\nthree", cursor: 0,
			keys:     []string{"V", "j", "J"},
			wantText: "one two\nthree", wantMode: ModeNormal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, ta := newVisualTestHarness(t, tt.text, tt.cursor)
			press(h, ta, tt.keys...)

			if got := ta.Value(); got != tt.wantText {
				t.Errorf("text = %q, want %q", got, tt.wantText)
			}
			if h.Mode() != tt.wantMode {
				t.Errorf("mode = %s, want %s", h.Mode(), tt.wantMode)
			}
			if tt.wantRegister != "" {
				if got, linewise := h.persistent.Register, h.persistent.Linewise; got != tt.wantRegister {
					t.Errorf("register = %q (linewise=%v), want %q", got, linewise, tt.wantRegister)
				}
			}
			if tt.wantLinewise && !h.persistent.Linewise {
				t.Error("register not marked linewise")
			}
		})
	}
}

func TestVisualPasteReplacesSelection(t *testing.T) {
	h, ta := newVisualTestHarness(t, "one two", 0)
	h.persistent.Register = "XYZ"

	press(h, ta, "v", "l", "l", "p")

	if got := ta.Value(); got != "XYZ two" {
		t.Errorf("text = %q, want %q", got, "XYZ two")
	}
	// Vim's swap behavior: the replaced text lands in the register.
	if got := h.persistent.Register; got != "one" {
		t.Errorf("register = %q, want the replaced text %q", got, "one")
	}
}

func TestVisualPasteWithEmptyRegisterKeepsText(t *testing.T) {
	h, ta := newVisualTestHarness(t, "one two", 0)
	press(h, ta, "v", "l", "l", "p")
	if got := ta.Value(); got != "one two" {
		t.Errorf("text = %q; an empty register must not silently delete the selection", got)
	}
}

func TestVisualTextObjectExtendsSelection(t *testing.T) {
	h, ta := newVisualTestHarness(t, "one two three", 5)
	press(h, ta, "v", "i", "w", "d")
	if got := ta.Value(); got != "one  three" {
		t.Errorf("text = %q, want %q", got, "one  three")
	}
}

// ---- undo, gv, dot-repeat ---------------------------------------------------

func TestVisualOperatorUndoesInOneStep(t *testing.T) {
	const original = "one two three"
	h, ta := newVisualTestHarness(t, original, 4)

	press(h, ta, "v", "l", "l", "l", "d")
	if ta.Value() == original {
		t.Fatal("delete had no effect")
	}

	press(h, ta, "u")
	if got := ta.Value(); got != original {
		t.Errorf("after one undo text = %q, want the original %q", got, original)
	}
}

func TestGvRestoresTheLastSelection(t *testing.T) {
	h, ta := newVisualTestHarness(t, "one two three", 4)
	press(h, ta, "v", "l", "l")
	wantAnchor, wantCursor := h.state.Anchor, cursorOffset(h, ta)
	press(h, ta, "esc")

	press(h, ta, "g", "v")

	if h.Mode() != ModeVisual {
		t.Fatalf("mode = %s, want VISUAL", h.Mode())
	}
	if h.state.Anchor != wantAnchor {
		t.Errorf("anchor = %d, want %d", h.state.Anchor, wantAnchor)
	}
	if got := cursorOffset(h, ta); got != wantCursor {
		t.Errorf("cursor = %d, want %d", got, wantCursor)
	}
}

func TestGvWithNoPriorSelectionIsANoOp(t *testing.T) {
	h, ta := newVisualTestHarness(t, "one two", 0)
	press(h, ta, "g", "v")
	if h.Mode() != ModeNormal {
		t.Errorf("mode = %s, want NORMAL", h.Mode())
	}
	if got := ta.Value(); got != "one two" {
		t.Errorf("text = %q, want it unchanged", got)
	}
}

func TestVisualDotRepeatsTheSameSizedRegion(t *testing.T) {
	h, ta := newVisualTestHarness(t, "abcdefghij", 0)

	// Delete three characters via a visual selection.
	press(h, ta, "v", "l", "l", "d")
	if got := ta.Value(); got != "defghij" {
		t.Fatalf("after visual delete text = %q, want %q", got, "defghij")
	}

	// `.` repeats a three-character delete at the cursor, not at the original
	// coordinates — that is what vim does.
	press(h, ta, ".")
	if got := ta.Value(); got != "ghij" {
		t.Errorf("after dot-repeat text = %q, want %q", got, "ghij")
	}
}

// ---- SelectionRange unit coverage -------------------------------------------

func TestSelectionRange(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		anchor   int
		cursor   int
		linewise bool
		wantFrom int
		wantTo   int
	}{
		{name: "cursor after anchor", text: "abcdef", anchor: 1, cursor: 3, wantFrom: 1, wantTo: 4},
		{name: "cursor before anchor", text: "abcdef", anchor: 3, cursor: 1, wantFrom: 1, wantTo: 4},
		{name: "same position", text: "abcdef", anchor: 2, cursor: 2, wantFrom: 2, wantTo: 3},
		{name: "at end of text", text: "abc", anchor: 3, cursor: 3, wantFrom: 3, wantTo: 3},
		{name: "multibyte is not split", text: "aé b", anchor: 1, cursor: 1, wantFrom: 1, wantTo: 3},
		{name: "linewise single line", text: "one\ntwo", anchor: 1, cursor: 2, linewise: true, wantFrom: 0, wantTo: 4},
		{name: "linewise last line has no newline", text: "one\ntwo", anchor: 5, cursor: 5, linewise: true, wantFrom: 4, wantTo: 7},
		{name: "offsets clamped", text: "abc", anchor: -5, cursor: 99, wantFrom: 0, wantTo: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			from, to := SelectionRange(tt.text, tt.anchor, tt.cursor, tt.linewise)
			if from != tt.wantFrom || to != tt.wantTo {
				t.Errorf("SelectionRange = [%d,%d), want [%d,%d)", from, to, tt.wantFrom, tt.wantTo)
			}
		})
	}
}

// ---- review regressions -----------------------------------------------------

// TestVisualOperatorsOnNonASCII is the regression for a rune/byte confusion that
// silently destroyed text: the textarea indexes columns by rune, the vim package
// by byte, and the conversion between them counted bytes. On any non-ASCII draft
// the cursor landed elsewhere than it was drawn and operators cut mid-character.
func TestVisualOperatorsOnNonASCII(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		cursor   int
		keys     []string
		wantText string
	}{
		{
			name: "case toggle preserves every character", text: "日本語", cursor: 0,
			keys: []string{"l", "v", "~"}, wantText: "日本語",
		},
		{
			name: "delete removes the character under the cursor", text: "aébc", cursor: 0,
			keys: []string{"l", "l", "v", "d"}, wantText: "aéc",
		},
		{
			name: "uppercase across a multi-byte run", text: "aébc", cursor: 0,
			keys: []string{"v", "l", "l", "U"}, wantText: "AÉBc",
		},
		{
			name: "emoji is not split", text: "a🙂b", cursor: 0,
			keys: []string{"l", "v", "d"}, wantText: "ab",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, ta := newVisualTestHarness(t, tt.text, tt.cursor)
			press(h, ta, tt.keys...)
			if got := ta.Value(); got != tt.wantText {
				t.Errorf("text = %q, want %q", got, tt.wantText)
			}
			if !utf8.ValidString(ta.Value()) {
				t.Errorf("operation produced invalid UTF-8: %q", ta.Value())
			}
		})
	}
}

func TestVisualYankOnNonASCIIRegisterIsValid(t *testing.T) {
	h, ta := newVisualTestHarness(t, "日本語です", 0)
	press(h, ta, "l", "v", "y")
	if got := h.persistent.Register; !utf8.ValidString(got) || got != "本" {
		t.Errorf("register = %q, want %q and valid UTF-8", got, "本")
	}
}

// A case operator must never alter the bytes of a rune it cannot classify.
func TestExecuteVisualCaseLeavesInvalidBytesAlone(t *testing.T) {
	const text = "a\xa9b"
	got := text
	ctx := &OperatorContext{
		Text: text, Offset: 0,
		SetText: func(s string) { got = s }, SetOffset: func(int) {},
		EnterInsert: func(int) {},
		GetRegister: func() (string, bool) { return "", false },
		SetRegister: func(string, bool) {}, GetLastFind: func() *FindRecord { return nil },
		SetLastFind: func(FindType, string) {}, RecordChange: func(RecordedChange) {},
	}
	ExecuteVisualCase('~', 1, 2, false, ctx)
	if got != text {
		t.Errorf("case toggle rewrote an unclassifiable byte: %q -> %q (%d -> %d bytes)",
			text, got, len(text), len(got))
	}
}

func TestVisualLinewiseMatchesNormalMode(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		cursor   int
		keys     []string
		wantText string
	}{
		// `Vd` on the last line must not leave a blank line behind, the same
		// rule `dd` follows.
		{name: "delete last line", text: "one\ntwo", cursor: 4, keys: []string{"V", "d"}, wantText: "one"},
		{name: "delete to end", text: "one\ntwo\nthree", cursor: 4, keys: []string{"V", "j", "d"}, wantText: "one"},
		{name: "delete a middle line", text: "one\ntwo\nthree", cursor: 4, keys: []string{"V", "d"}, wantText: "one\nthree"},

		// `<` must remove at most one indent unit, like `<<`, not every space.
		{name: "unindent removes one level", text: "      deep", cursor: 0, keys: []string{"V", "<"}, wantText: "    deep"},

		// vim leaves empty lines alone when indenting.
		{name: "indent skips blank lines", text: "one\n\ntwo", cursor: 0, keys: []string{"V", "j", "j", ">"}, wantText: "  one\n\n  two"},

		// `J` must not double an existing trailing space.
		{name: "join does not double a space", text: "one \ntwo", cursor: 0, keys: []string{"V", "j", "J"}, wantText: "one two"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, ta := newVisualTestHarness(t, tt.text, tt.cursor)
			press(h, ta, tt.keys...)
			if got := ta.Value(); got != tt.wantText {
				t.Errorf("text = %q, want %q", got, tt.wantText)
			}
		})
	}
}

// Linewise change clears the lines rather than removing them: vim leaves an
// empty line with the cursor on it, so typing replaces the block instead of
// being prepended to whatever followed.
func TestVisualLinewiseChangeClearsTheLine(t *testing.T) {
	h, ta := newVisualTestHarness(t, "one\ntwo\nthree", 4)
	press(h, ta, "V", "c")

	if got := ta.Value(); got != "one\n\nthree" {
		t.Errorf("text = %q, want %q", got, "one\n\nthree")
	}
	if h.Mode() != ModeInsert {
		t.Errorf("mode = %s, want INSERT", h.Mode())
	}
}

// A non-mutating visual operation must not consume an undo step; otherwise the
// user's next `u` silently does nothing instead of undoing their last edit.
func TestNonMutatingVisualOpsDoNotBurnUndo(t *testing.T) {
	cases := []struct {
		name string
		keys []string
	}{
		{name: "yank", keys: []string{"v", "l", "l", "y"}},
		{name: "join on the last line", keys: []string{"V", "J"}},
		{name: "paste with an empty register", keys: []string{"v", "l", "p"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, ta := newVisualTestHarness(t, "one two three", 0)
			// Seed one real change with `~` rather than `x`: `x` yanks into the
			// register, which would make the empty-register paste case mutate
			// after all and test nothing.
			press(h, ta, "~")
			seeded := ta.Value()
			if seeded == "one two three" {
				t.Fatalf("seed change had no effect")
			}

			press(h, ta, tc.keys...)
			press(h, ta, "u")

			if got := ta.Value(); got != "one two three" {
				t.Errorf("after %s then u, text = %q, want the original restored; the no-op consumed the undo step (its own snapshot was %q)",
					tc.name, got, seeded)
			}
		})
	}
}
