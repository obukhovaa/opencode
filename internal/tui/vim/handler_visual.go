package vim

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textarea"
)

// enterVisual switches into a visual mode, anchoring the selection at the
// cursor. Re-entering from the other visual mode keeps the anchor, so `v` and
// `V` swap the selection's shape without losing it.
func (h *Handler) enterVisual(mode VimMode, ta *textarea.Model, anchor, cursor int) {
	h.state.Mode = mode
	h.state.Anchor = anchor
	h.state.VisualCount = ""
	h.visualPending = ""
	h.setCursorPosition(ta, ta.Value(), cursor)
}

// leaveVisual returns to NORMAL, remembering the selection for `gv`.
func (h *Handler) leaveVisual(ta *textarea.Model) {
	text := ta.Value()
	cursor := lineColToOffset(text, ta.Line(), ta.Column())
	h.persistent.LastVisual = &VisualRecord{
		Mode:   h.state.Mode,
		Anchor: h.state.Anchor,
		Cursor: cursor,
	}
	h.state = VimState{Mode: ModeNormal, Command: CommandIdle{}}
	h.visualPending = ""
}

// restoreVisual implements `gv`. A selection that has not been made yet leaves
// the editor exactly as it was.
func (h *Handler) restoreVisual(ta *textarea.Model) bool {
	rec := h.persistent.LastVisual
	if rec == nil {
		return false
	}
	text := ta.Value()
	h.enterVisual(rec.Mode, ta, clampOffset(text, rec.Anchor), clampOffset(text, rec.Cursor))
	return true
}

// visualCount returns the pending count, defaulting to 1.
func (h *Handler) visualCount() int {
	if h.state.VisualCount == "" {
		return 1
	}
	n, err := strconv.Atoi(h.state.VisualCount)
	if err != nil || n < 1 {
		return 1
	}
	return min(n, MaxVimCount)
}

// handleVisualInput is the visual-mode key handler.
//
// Visual mode has no operator-pending machinery: the selection IS the operand,
// so an operator applies immediately and there is nothing to wait for. The only
// pending states are the ones that need one more keystroke to name a target —
// a find character, `g`, and a text-object scope.
func (h *Handler) handleVisualInput(input string, ta *textarea.Model) {
	if input == "" {
		return
	}

	text := ta.Value()
	cursor := lineColToOffset(text, ta.Line(), ta.Column())

	// Resolve a pending prefix first: its argument is whatever key just arrived.
	if pending := h.visualPending; pending != "" {
		h.visualPending = ""
		h.resolveVisualPending(pending, input, ta, text, cursor)
		return
	}

	// Counts. `0` is the start-of-line motion unless it continues a count.
	if len(input) == 1 && input[0] >= '0' && input[0] <= '9' {
		if input != "0" || h.state.VisualCount != "" {
			n, _ := strconv.Atoi(h.state.VisualCount + input)
			h.state.VisualCount = strconv.Itoa(min(n, MaxVimCount))
			return
		}
	}

	count := h.visualCount()

	switch input {
	case "v":
		if h.state.Mode == ModeVisual {
			h.leaveVisual(ta)
		} else {
			h.enterVisual(ModeVisual, ta, h.state.Anchor, cursor)
		}
		return
	case "V":
		if h.state.Mode == ModeVisualLine {
			h.leaveVisual(ta)
		} else {
			h.enterVisual(ModeVisualLine, ta, h.state.Anchor, cursor)
		}
		return
	case "o":
		// Swap the ends so motions move the other side of the selection.
		h.state.Anchor, cursor = cursor, h.state.Anchor
		h.state.VisualCount = ""
		h.setCursorPosition(ta, text, clampOffset(text, cursor))
		return
	case "f", "F", "t", "T", "g", "i", "a":
		h.visualPending = input
		return
	}

	if IsSimpleMotion(input) || input == "0" {
		h.moveVisualCursor(ta, text, ResolveMotion(input, text, cursor, count))
		return
	}
	if input == "G" {
		if h.state.VisualCount == "" {
			h.moveVisualCursor(ta, text, startOfLastLine(text))
		} else {
			h.moveVisualCursor(ta, text, goToLine(text, count))
		}
		return
	}
	if input == ";" || input == "," {
		if rec := h.persistent.LastFind; rec != nil {
			ft := rec.Type
			if input == "," {
				ft = reverseFind(ft)
			}
			if target := findCharacter(text, cursor, rec.Char, ft, count); target != -1 {
				h.moveVisualCursor(ta, text, target)
				return
			}
		}
		h.state.VisualCount = ""
		return
	}

	h.applyVisualOperator(input, ta, text, cursor)
}

// resolveVisualPending handles the second key of a two-key visual command.
func (h *Handler) resolveVisualPending(pending, input string, ta *textarea.Model, text string, cursor int) {
	count := h.visualCount()

	if ft, ok := IsFindKey(pending); ok {
		if target := findCharacter(text, cursor, input, ft, count); target != -1 {
			h.persistent.LastFind = &FindRecord{Type: ft, Char: input}
			h.moveVisualCursor(ta, text, target)
			return
		}
		h.state.VisualCount = ""
		return
	}

	if pending == "g" {
		switch input {
		case "g":
			if h.state.VisualCount == "" {
				h.moveVisualCursor(ta, text, startOfFirstLine())
			} else {
				h.moveVisualCursor(ta, text, goToLine(text, count))
			}
		case "j", "k":
			h.moveVisualCursor(ta, text, ResolveMotion("g"+input, text, cursor, count))
		case "u", "U":
			// `gu` / `gU` are the explicit case operators; plain u/U in visual
			// mode mean the same thing, so both spellings work.
			h.runVisualEdit(ta, text, cursor, func(from, to int, linewise bool, ctx *OperatorContext) {
				ExecuteVisualCase(rune(input[0]), from, to, linewise, ctx)
			})
		default:
			h.state.VisualCount = ""
		}
		return
	}

	// Text object: `i` or `a` followed by the object type.
	scope, _ := IsTextObjScopeKey(pending)
	if !IsTextObjType(input) {
		h.state.VisualCount = ""
		return
	}
	r := FindTextObject(text, cursor, input, scope == ScopeInner)
	if r == nil {
		h.state.VisualCount = ""
		return
	}
	// A text object in visual mode extends the selection to cover it rather
	// than acting on it; the operator comes next.
	h.state.Anchor = r.Start
	h.state.VisualCount = ""
	h.moveVisualCursor(ta, text, max(r.Start, r.End-1))
}

// moveVisualCursor moves the selection's moving end and clears the count.
func (h *Handler) moveVisualCursor(ta *textarea.Model, text string, target int) {
	h.state.VisualCount = ""
	h.setCursorPosition(ta, text, clampOffset(text, target))
}

// applyVisualOperator runs an operator over the selection and returns to NORMAL
// (or INSERT, for the change operators).
func (h *Handler) applyVisualOperator(input string, ta *textarea.Model, text string, cursor int) {
	switch input {
	case "d", "x", "X":
		h.runVisualEdit(ta, text, cursor, func(from, to int, linewise bool, ctx *OperatorContext) {
			ExecuteVisualOperator(OpDelete, from, to, linewise, ctx)
		})
	case "c", "s":
		h.runVisualEdit(ta, text, cursor, func(from, to int, linewise bool, ctx *OperatorContext) {
			ExecuteVisualChange(from, to, linewise, ctx)
		})
	case "y":
		h.runVisualEdit(ta, text, cursor, func(from, to int, linewise bool, ctx *OperatorContext) {
			ExecuteVisualOperator(OpYank, from, to, linewise, ctx)
		})
	case "~", "u", "U":
		h.runVisualEdit(ta, text, cursor, func(from, to int, linewise bool, ctx *OperatorContext) {
			ExecuteVisualCase(rune(input[0]), from, to, linewise, ctx)
		})
	case ">", "<":
		h.runVisualEdit(ta, text, cursor, func(from, to int, linewise bool, ctx *OperatorContext) {
			ExecuteVisualIndent(rune(input[0]), from, to, ctx)
		})
	case "J":
		h.runVisualEdit(ta, text, cursor, func(from, to int, linewise bool, ctx *OperatorContext) {
			ExecuteVisualJoin(from, to, ctx)
		})
	case "p", "P":
		h.runVisualEdit(ta, text, cursor, func(from, to int, linewise bool, ctx *OperatorContext) {
			ExecuteVisualPaste(from, to, linewise, ctx)
		})
	default:
		// Unbound key: swallow it (visual mode owns the keyboard) and drop any
		// half-typed count so the next command starts clean.
		h.state.VisualCount = ""
	}
}

// runVisualEdit is the single place a visual-mode edit is applied. It resolves
// the selection, pushes one undo entry so the whole operation reverts in one
// step, runs the edit, and lands the editor in NORMAL or INSERT.
func (h *Handler) runVisualEdit(ta *textarea.Model, text string, cursor int, edit func(from, to int, linewise bool, ctx *OperatorContext)) {
	linewise := h.state.Mode == ModeVisualLine
	from, to := SelectionRange(text, h.state.Anchor, cursor, linewise)

	newText := text
	newOffset := from
	insertMode := false
	insertOffset := 0

	ctx := &OperatorContext{
		Text:        text,
		Offset:      cursor,
		SetText:     func(t string) { newText = t },
		SetOffset:   func(o int) { newOffset = o },
		EnterInsert: func(o int) { insertMode = true; insertOffset = o },
		GetRegister: func() (string, bool) { return h.persistent.Register, h.persistent.Linewise },
		SetRegister: func(content string, lw bool) {
			h.persistent.Register = content
			h.persistent.Linewise = lw
		},
		GetLastFind:  func() *FindRecord { return h.persistent.LastFind },
		SetLastFind:  func(ft FindType, char string) { h.persistent.LastFind = &FindRecord{Type: ft, Char: char} },
		RecordChange: func(change RecordedChange) { h.persistent.LastChange = &change },
	}

	// Snapshot BEFORE the edit but only keep it if the edit changed something.
	// A yank, a `J` on the last line, or a `p` with an empty register mutates
	// nothing, and burning an undo entry on them means the user's next `u`
	// silently does nothing instead of undoing their last real change.
	undoDepth := len(h.undoStack)
	h.pushUndo(text, ta.Line(), ta.Column())
	edit(from, to, linewise, ctx)
	if newText == text && len(h.undoStack) == undoDepth+1 {
		h.undoStack = h.undoStack[:undoDepth]
	}

	// Remember the selection before leaving: `gv` after an edit should restore
	// where the user was working.
	h.persistent.LastVisual = &VisualRecord{Mode: h.state.Mode, Anchor: h.state.Anchor, Cursor: cursor}

	if newText != text {
		ta.SetValue(newText)
	}
	if insertMode {
		h.switchToInsert(ta, insertOffset, newText)
		h.visualPending = ""
		return
	}

	h.state = VimState{Mode: ModeNormal, Command: CommandIdle{}}
	h.visualPending = ""
	h.setCursorPosition(ta, newText, clampOffset(newText, newOffset))
}

// reverseFind flips a find direction for the `,` repeat.
func reverseFind(ft FindType) FindType {
	switch ft {
	case FindF:
		return FindB
	case FindB:
		return FindF
	case FindT:
		return FindR
	case FindR:
		return FindT
	}
	return ft
}

// replayVisualChange repeats a visual-mode operator with `.`. Vim applies the
// repeat over a region of the same size starting at the cursor, not over the
// original coordinates — the selection itself is not part of the repeat.
func (h *Handler) replayVisualChange(change *RecordedChange, ta *textarea.Model) {
	text := ta.Value()
	cursor := lineColToOffset(text, ta.Line(), ta.Column())
	if change.Span <= 0 {
		return
	}

	from := cursor
	to := min(cursor+change.Span, len(text))
	if change.Linewise {
		from = startOfLine(text, from)
		if nl := strings.IndexByte(text[min(to, len(text)):], '\n'); nl >= 0 {
			to = min(to, len(text)) + nl + 1
		} else {
			to = len(text)
		}
	}
	if from >= to {
		return
	}

	newText := text
	newOffset := from
	ctx := &OperatorContext{
		Text:        text,
		Offset:      cursor,
		SetText:     func(t string) { newText = t },
		SetOffset:   func(o int) { newOffset = o },
		EnterInsert: func(int) {},
		GetRegister: func() (string, bool) { return h.persistent.Register, h.persistent.Linewise },
		SetRegister: func(content string, lw bool) {
			h.persistent.Register = content
			h.persistent.Linewise = lw
		},
		GetLastFind:  func() *FindRecord { return h.persistent.LastFind },
		SetLastFind:  func(FindType, string) {},
		RecordChange: func(RecordedChange) {}, // never re-record during replay
	}

	switch op := string(change.Op); {
	case op == string(OpDelete), op == string(OpChange), op == string(OpYank):
		ExecuteVisualOperator(change.Op, from, to, change.Linewise, ctx)
	case strings.HasPrefix(op, "case-"):
		ExecuteVisualCase(rune(op[len("case-")]), from, to, change.Linewise, ctx)
	case strings.HasPrefix(op, "indent-"):
		ExecuteVisualIndent(rune(op[len("indent-")]), from, to, ctx)
	case op == "join":
		ExecuteVisualJoin(from, to, ctx)
	case op == "paste":
		ExecuteVisualPaste(from, to, change.Linewise, ctx)
	default:
		return
	}

	if newText != text {
		ta.SetValue(newText)
	}
	h.setCursorPosition(ta, newText, clampOffset(newText, newOffset))
}
