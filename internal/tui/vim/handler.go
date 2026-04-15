package vim

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

// UndoEntry stores a snapshot for undo.
type UndoEntry struct {
	Text string
	Line int
	Col  int
}

const maxUndoStack = 100

// Handler integrates the vim state machine with a textarea.Model.
type Handler struct {
	state      VimState
	persistent PersistentState
	undoStack  []UndoEntry
}

// NewHandler creates a new vim handler starting in INSERT mode.
func NewHandler() *Handler {
	return &Handler{
		state:      CreateInitialVimState(),
		persistent: CreateInitialPersistentState(),
	}
}

// Mode returns the current vim mode.
func (h *Handler) Mode() VimMode {
	return h.state.Mode
}

// CommandState returns the current command state (only meaningful in NORMAL mode).
func (h *Handler) CommandStateValue() CommandState {
	return h.state.Command
}

// ConsumesCtrlC returns true when the vim handler should consume Ctrl+C
// instead of letting the app-level quit handler process it.
func (h *Handler) ConsumesCtrlC() bool {
	if h.state.Mode == ModeInsert {
		return true
	}
	if h.state.Mode == ModeNormal {
		_, isIdle := h.state.Command.(CommandIdle)
		return !isIdle
	}
	return false
}

// HandleKey processes a key event and applies it to the textarea.
// Returns true if the key was handled, any resulting command, and whether mode changed.
func (h *Handler) HandleKey(msg tea.KeyPressMsg, ta *textarea.Model) (handled bool, cmd tea.Cmd, modeChanged bool) {
	keyStr := msg.String()

	// Ctrl+key combos always delegate to base handler (except Ctrl+C)
	if strings.HasPrefix(keyStr, "ctrl+") && keyStr != "ctrl+c" {
		return false, nil, false
	}

	// Escape or Ctrl+C in INSERT mode → switch to NORMAL
	if h.state.Mode == ModeInsert && (keyStr == "esc" || keyStr == "ctrl+c") {
		h.switchToNormal(ta)
		return true, nil, true
	}

	// Escape in NORMAL mode → cancel pending command
	if h.state.Mode == ModeNormal && keyStr == "esc" {
		h.state.Command = CommandIdle{}
		return true, nil, false
	}

	// Ctrl+C in NORMAL mode
	if h.state.Mode == ModeNormal && keyStr == "ctrl+c" {
		_, isIdle := h.state.Command.(CommandIdle)
		if !isIdle {
			// Cancel pending command
			h.state.Command = CommandIdle{}
			return true, nil, false
		}
		// Idle NORMAL + Ctrl+C → don't handle, let app show quit dialog
		return false, nil, false
	}

	// Return key handling
	if keyStr == "enter" || keyStr == "ctrl+s" {
		if h.state.Mode == ModeInsert {
			// Let the editor handle send
			return false, nil, false
		}
		// NORMAL mode: Enter maps to j (move down)
		h.handleNormalInput("j", ta)
		return true, nil, false
	}

	// INSERT mode: pass keys to textarea, track inserted text
	if h.state.Mode == ModeInsert {
		if keyStr == "backspace" || keyStr == "delete" {
			if len(h.state.InsertedText) > 0 {
				h.state.InsertedText = h.state.InsertedText[:len(h.state.InsertedText)-1]
			}
		} else if msg.Text != "" {
			h.state.InsertedText += msg.Text
		}
		// Let the textarea handle the key
		return false, nil, false
	}

	// NORMAL mode
	if h.state.Mode == ModeNormal {
		// Map arrow keys
		vimInput := h.mapKey(msg)

		h.handleNormalInput(vimInput, ta)
		return true, nil, h.state.Mode != ModeNormal
	}

	return false, nil, false
}

// mapKey maps special keys to vim equivalents in NORMAL mode.
func (h *Handler) mapKey(msg tea.KeyPressMsg) string {
	keyStr := msg.String()

	switch keyStr {
	case "left":
		return "h"
	case "right":
		return "l"
	case "up":
		return "k"
	case "down":
		return "j"
	case "backspace":
		if h.expectsMotion() {
			return "h"
		}
		return ""
	case "delete":
		if h.expectsMotion() {
			_, isCount := h.state.Command.(CommandCount)
			if !isCount {
				return "x"
			}
		}
		return ""
	}

	// Use msg.Text for printable characters
	if msg.Text != "" {
		return msg.Text
	}
	return keyStr
}

func (h *Handler) expectsMotion() bool {
	switch h.state.Command.(type) {
	case CommandIdle, CommandCount, CommandOperator, CommandOperatorCount:
		return true
	}
	return false
}

// handleNormalInput feeds input through the state machine and applies the result.
func (h *Handler) handleNormalInput(input string, ta *textarea.Model) {
	if input == "" {
		return
	}

	text := ta.Value()
	line := ta.Line()
	col := ta.Column()
	offset := lineColToOffset(text, line, col)

	// Track text/offset changes
	newText := text
	newOffset := offset
	insertMode := false
	insertOffset := 0

	ctx := &TransitionContext{
		OperatorContext: OperatorContext{
			Text:   text,
			Offset: offset,
			SetText: func(t string) {
				newText = t
			},
			SetOffset: func(o int) {
				newOffset = o
			},
			EnterInsert: func(o int) {
				insertMode = true
				insertOffset = o
			},
			GetRegister: func() (string, bool) {
				return h.persistent.Register, h.persistent.Linewise
			},
			SetRegister: func(content string, linewise bool) {
				h.persistent.Register = content
				h.persistent.Linewise = linewise
			},
			GetLastFind: func() *FindRecord {
				return h.persistent.LastFind
			},
			SetLastFind: func(ft FindType, char string) {
				h.persistent.LastFind = &FindRecord{Type: ft, Char: char}
			},
			RecordChange: func(change RecordedChange) {
				h.persistent.LastChange = &change
			},
		},
		OnUndo:      func() { h.undo(ta) },
		OnDotRepeat: func() { h.replayLastChange(ta) },
	}

	// Push undo before mutating operations
	if h.isMutatingInput(input) {
		h.pushUndo(text, line, col)
	}

	result := Transition(h.state.Command, input, ctx)

	if result.Execute != nil {
		result.Execute()
	}

	// Apply text changes
	if newText != text {
		ta.SetValue(newText)
	}

	// Apply cursor position
	if insertMode {
		h.switchToInsert(ta, insertOffset, newText)
	} else if newOffset != offset || newText != text {
		h.setCursorPosition(ta, newText, newOffset)
	}

	// Update command state (only if we didn't switch to INSERT)
	if h.state.Mode == ModeNormal {
		if result.Next != nil {
			h.state.Command = result.Next
		} else if result.Execute != nil {
			h.state.Command = CommandIdle{}
		}
	}
}

// switchToNormal switches from INSERT to NORMAL mode.
func (h *Handler) switchToNormal(ta *textarea.Model) {
	if h.state.Mode == ModeInsert && h.state.InsertedText != "" {
		h.persistent.LastChange = &RecordedChange{
			Type: "insert",
			Text: h.state.InsertedText,
		}
	}

	// Vim behavior: move cursor left by 1 when exiting insert mode
	text := ta.Value()
	line := ta.Line()
	col := ta.Column()
	offset := lineColToOffset(text, line, col)
	if offset > 0 && text[offset-1] != '\n' {
		offset--
		h.setCursorPosition(ta, text, offset)
	}

	h.state = VimState{Mode: ModeNormal, Command: CommandIdle{}}
}

// switchToInsert switches to INSERT mode at the given offset.
func (h *Handler) switchToInsert(ta *textarea.Model, offset int, text string) {
	h.setCursorPosition(ta, text, offset)
	h.state = VimState{Mode: ModeInsert, InsertedText: ""}
}

// setCursorPosition positions the cursor using delta-based navigation.
func (h *Handler) setCursorPosition(ta *textarea.Model, text string, offset int) {
	targetLine, targetCol := offsetToLineCol(text, offset)
	currentLine := ta.Line()
	currentCol := ta.Column()

	// Move vertically by delta
	if targetLine > currentLine {
		for i := 0; i < targetLine-currentLine; i++ {
			ta.CursorDown()
		}
	} else if targetLine < currentLine {
		for i := 0; i < currentLine-targetLine; i++ {
			ta.CursorUp()
		}
	}

	// Set column directly
	_ = currentCol
	ta.SetCursorColumn(targetCol)
}

// pushUndo saves current state to the undo stack.
func (h *Handler) pushUndo(text string, line, col int) {
	h.undoStack = append(h.undoStack, UndoEntry{Text: text, Line: line, Col: col})
	if len(h.undoStack) > maxUndoStack {
		h.undoStack = h.undoStack[1:]
	}
}

// undo restores the last undo entry.
func (h *Handler) undo(ta *textarea.Model) {
	if len(h.undoStack) == 0 {
		return
	}
	entry := h.undoStack[len(h.undoStack)-1]
	h.undoStack = h.undoStack[:len(h.undoStack)-1]

	ta.SetValue(entry.Text)
	h.setCursorPosition(ta, entry.Text, lineColToOffset(entry.Text, entry.Line, entry.Col))
}

// replayLastChange replays the last recorded change (dot-repeat).
func (h *Handler) replayLastChange(ta *textarea.Model) {
	change := h.persistent.LastChange
	if change == nil {
		return
	}

	text := ta.Value()
	line := ta.Line()
	col := ta.Column()
	offset := lineColToOffset(text, line, col)

	newText := text
	newOffset := offset
	insertMode := false
	insertOffset := 0

	ctx := &OperatorContext{
		Text:   text,
		Offset: offset,
		SetText: func(t string) {
			newText = t
		},
		SetOffset: func(o int) {
			newOffset = o
		},
		EnterInsert: func(o int) {
			insertMode = true
			insertOffset = o
		},
		GetRegister: func() (string, bool) {
			return h.persistent.Register, h.persistent.Linewise
		},
		SetRegister: func(content string, linewise bool) {
			h.persistent.Register = content
			h.persistent.Linewise = linewise
		},
		GetLastFind: func() *FindRecord {
			return h.persistent.LastFind
		},
		SetLastFind: func(ft FindType, char string) {
			h.persistent.LastFind = &FindRecord{Type: ft, Char: char}
		},
		RecordChange: func(RecordedChange) {}, // Don't re-record during replay
	}

	h.pushUndo(text, line, col)

	switch change.Type {
	case "insert":
		if change.Text != "" {
			insertPos := offset
			newText = text[:insertPos] + change.Text + text[insertPos:]
			newOffset = insertPos + len(change.Text)
		}
	case "x":
		ExecuteX(change.Count, ctx)
	case "replace":
		ExecuteReplace(change.Char, change.Count, ctx)
	case "toggleCase":
		ExecuteToggleCase(change.Count, ctx)
	case "indent":
		ExecuteIndent(change.Dir, change.Count, ctx)
	case "join":
		ExecuteJoin(change.Count, ctx)
	case "openLine":
		ExecuteOpenLine(change.Direction, ctx)
	case "operator":
		ExecuteOperatorMotion(change.Op, change.Motion, change.Count, ctx)
	case "operatorFind":
		ExecuteOperatorFind(change.Op, change.Find, change.Char, change.Count, ctx)
	case "operatorTextObj":
		ExecuteOperatorTextObj(change.Op, change.Scope, change.ObjType, change.Count, ctx)
	}

	if newText != text {
		ta.SetValue(newText)
	}
	if insertMode {
		h.switchToInsert(ta, insertOffset, newText)
	} else if newOffset != offset || newText != text {
		h.setCursorPosition(ta, newText, newOffset)
	}
}

// isMutatingInput checks if the current state + input will mutate text.
func (h *Handler) isMutatingInput(input string) bool {
	switch h.state.Command.(type) {
	case CommandOperator, CommandOperatorCount, CommandOperatorFind, CommandOperatorTextObj:
		return true
	case CommandReplace:
		return true
	case CommandIndent:
		return true
	}
	// Simple mutating commands from idle/count
	return input == "x" || input == "~" || input == "J" || input == "p" || input == "P" ||
		input == "D" || input == "C" || input == "." || input == "o" || input == "O" || input == "Y"
}
