# Vim Mode for Chat Text Input

**Date**: 2026-04-14
**Status**: Draft
**Author**: AI-assisted

## Overview

Add vim-style keybindings to the TUI chat text input, mirroring the Claude Code implementation. The vim state machine supports INSERT and NORMAL modes with standard vim motions, operators, text objects, find commands, dot-repeat, and a yank register. The current vim mode is displayed in the status bar. Both Escape and Ctrl+C exit INSERT mode (Ctrl+C is needed when OpenCode runs inside a terminal embedded in Neovim, where raw Escape may be intercepted).

## Motivation

### Current State

The chat editor (`internal/tui/components/chat/editor.go`) uses the standard bubbles textarea with no modal editing. Users accustomed to vim keybindings must either open an external editor (`ctrl+e`) or use the default Emacs-style readline bindings.

### Desired State

- A `tui.vimMode` boolean in `.opencode.json` enables vim keybindings for the chat input.
- When enabled, the editor starts in INSERT mode (matching Claude Code's default).
- Pressing Escape **or Ctrl+C** switches to NORMAL mode.
- NORMAL mode supports standard vim motions (h/j/k/l, w/b/e, 0/^/$, G/gg), operators (d/c/y), text objects (iw, aw, i", a(, etc.), find (f/F/t/T), replace (r), dot-repeat (.), yank register (p/P), and undo (u).
- The status bar shows the current vim mode (e.g., `NORMAL`, `INSERT`).
- Shell mode, completion dialogs, and attachment delete mode continue to work correctly alongside vim mode.

## Research Findings

### Claude Code Reference Implementation

Source: `/Users/nouwa/Development/open-source-fork/claude-code-source-code/src/vim/`

| File | Purpose |
|------|---------|
| `types.ts` | State machine types: `VimState` (INSERT/NORMAL), `CommandState` (idle/count/operator/find/g/replace/indent + operator variants), `PersistentState` (lastChange, lastFind, register), `RecordedChange` |
| `transitions.ts` | Pure state transition table: `transition(state, input, ctx) -> {next?, execute?}` — dispatches to per-state handlers |
| `motions.ts` | Pure motion resolution: h/l/j/k/w/b/e/W/B/E/0/^/$/gg/G with count support |
| `operators.ts` | Operator execution: delete/change/yank ranges, line ops (dd/cc/yy), x, paste (p/P), replace (r), toggle case (~), join (J), indent (>>/<<), open line (o/O) |
| `textObjects.ts` | Text object boundary finding: word/WORD objects, quoted strings, bracket pairs — each returns `{start, end}` |

**Key design decisions from Claude Code:**
- Two modes only (INSERT/NORMAL) — no VISUAL mode
- State starts in INSERT mode
- Escape in INSERT → NORMAL (cursor moves left 1); Escape in NORMAL → reset to idle
- Ctrl+key combos always delegate to base handler (Ctrl+C, Ctrl+V, etc.)
- Pure functional transitions — `transition()` returns next state + execute function
- Persistent state (register, lastFind, lastChange) survives across commands

### Integration Pattern

In Claude Code, `useVimInput` hook wraps the base `useTextInput` hook:
1. Ctrl+key → delegate to base
2. Escape → mode switch or cancel pending command
3. Return → delegate to base (submit)
4. INSERT mode → track `insertedText`, pass to base
5. NORMAL mode → feed to `transition()` state machine

Arrow keys are mapped to hjkl in NORMAL mode. Backspace maps to `h`, Delete maps to `x`.

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Configuration key | `tui.vimMode: true` in `.opencode.json` | Consistent with existing `tui.theme`; simple boolean toggle |
| Initial mode | INSERT | Matches Claude Code; natural for a chat input — user expects to type immediately |
| Mode display | Status bar widget (leftmost, before help) | Always visible; does not clutter editor area |
| Escape + Ctrl+C for exit | Both switch INSERT→NORMAL | Ctrl+C needed when running inside Neovim terminal where Escape may be captured. Ctrl+C in NORMAL mode resets to idle (cancels pending command) |
| Scope: two modes only | INSERT + NORMAL (no VISUAL) | Matches Claude Code; VISUAL mode adds complexity with minimal value in a chat input |
| Undo strategy | Custom undo stack via `SetValue()` snapshots | The bubbles textarea has **no built-in undo API** — no undo method or KeyMap binding. The vim handler maintains a simple undo stack: snapshot the full text + cursor position before each mutating command (operators, replace, join, indent, paste). `u` pops the stack and calls `SetValue()` + `SetCursorColumn()`. Stack depth capped at 100 entries. |
| Shell mode interaction | Vim mode inactive during shell mode | Shell mode has its own key handling; vim should not interfere |
| Completion dialog interaction | Escape/Ctrl+C in completion closes dialog first | Dialogs take priority over vim mode switching |
| State machine architecture | Go port of Claude Code's TypeScript state machine | Proven design; pure functions; easy to test |
| Package location | `internal/tui/vim/` | Mirrors Claude Code's `src/vim/` structure |
| Textarea cursor control | Delta-based cursor positioning via `Line()`/`Column()` accessors | The bubbles textarea has **no `SetCursorOffset(pos)` method** — only `SetCursorColumn(col)`, `CursorUp()`, `CursorDown()`, `CursorStart()`, `CursorEnd()`, plus `Line()` and `Column()` read accessors. The vim handler uses a delta approach: read current `(Line(), Column())`, compute target `(line, col)` from the motion offset, then call `CursorUp()`/`CursorDown()` only the necessary delta times and `SetCursorColumn(col)`. This avoids resetting to buffer start on every motion (which could reset scroll position) and is O(delta) instead of O(line). Pure motion/operator functions work on `(text string, offset int)` and return a new offset; the handler layer converts offsets to `(line, col)` using `offsetToLineCol()`. |
| Enter in NORMAL mode | No-op (does not send message) | In real vim, Enter moves cursor down. Sending on Enter in NORMAL would surprise vim users. Enter only sends in INSERT mode. In NORMAL, Enter is mapped to `j` (down one line) for consistency. |

## Architecture

### Package Structure

```
internal/tui/vim/
├── types.go         # State machine types (VimState, CommandState, PersistentState, RecordedChange)
├── transitions.go   # Pure state transition table
├── motions.go       # Motion resolution functions
├── operators.go     # Operator execution functions
├── textobjects.go   # Text object boundary finding
└── handler.go       # VimHandler: integrates state machine with textarea.Model
```

### State Machine (Go Port)

```go
// VimMode represents the current editing mode
type VimMode string
const (
    ModeInsert VimMode = "INSERT"
    ModeNormal VimMode = "NORMAL"
)

// VimState is the top-level state
type VimState struct {
    Mode         VimMode
    InsertedText string       // tracked in INSERT mode for dot-repeat
    Command      CommandState // active in NORMAL mode
}

// CommandState represents the NORMAL mode sub-state machine.
//
// Go lacks discriminated unions, so this uses an interface with concrete
// types per state. This prevents invalid field combinations at the type
// level (e.g., Scope set on a 'find' state) and makes switch exhaustiveness
// checkable via linters.
type CommandState interface {
    commandState() // sealed marker method
}

type CommandIdle struct{}
type CommandCount struct{ Digits string }
type CommandOperator struct{ Op Operator; Count int }
type CommandOperatorCount struct{ Op Operator; Count int; Digits string }
type CommandOperatorFind struct{ Op Operator; Count int; Find FindType }
type CommandOperatorTextObj struct{ Op Operator; Count int; Scope TextObjScope }
type CommandFind struct{ Find FindType; Count int }
type CommandG struct{ Count int }
type CommandOperatorG struct{ Op Operator; Count int }
type CommandReplace struct{ Count int }
type CommandIndent struct{ Dir rune; Count int }

// Each type implements commandState() to satisfy the sealed interface.

// PersistentState survives across commands
type PersistentState struct {
    LastChange      RecordedChange
    LastFind        *FindRecord
    Register        string
    RegisterLinewise bool
}
```

### Integration Flow

```
editorCmp.Update(tea.KeyPressMsg)
  │
  ├── shellMode? → shell handling (unchanged)
  ├── deleteMode? → attachment handling (unchanged)
  ├── vimEnabled?
  │     ├── vimHandler.HandleKey(msg) → (tea.Model, tea.Cmd)
  │     │     ├── Ctrl+key → delegate to textarea (Ctrl+C special: also switch to NORMAL)
  │     │     ├── Escape/Ctrl+C → mode switch or cancel command
  │     │     ├── Return + INSERT mode → delegate (send message)
  │     │     ├── Return + NORMAL mode → mapped to 'j' (move down one line, no send)
  │     │     ├── INSERT mode → track text, pass to textarea
  │     │     └── NORMAL mode → transition state machine
  │     │           ├── motion → move cursor
  │     │           ├── operator+motion → modify text
  │     │           └── insert command (i/a/o/etc.) → switch to INSERT
  │     └── emit VimModeChangedMsg if mode changed
  └── !vimEnabled → pass to textarea (unchanged)
```

### Status Bar Integration

```
┌─────────────────────────────────────────────────────────────────────┐
│ NORMAL │ ctrl+h help │ tab agents │ ctx: 110K, cst: $0.15 │ model │
└─────────────────────────────────────────────────────────────────────┘
```

New message type:
```go
// VimModeChangedMsg is emitted when vim mode changes
type VimModeChangedMsg struct {
    Mode vim.VimMode  // "INSERT" or "NORMAL"
}
```

The status bar receives this message and renders a widget at the leftmost position when vim mode is active.

### Editor Prompt Indicator

In NORMAL mode, the editor prompt changes from `>` to a block-style indicator. The cursor style should also change if the terminal supports it (block cursor in NORMAL, line cursor in INSERT), but this is a stretch goal — the bubbles textarea may not expose cursor style control.

## Implementation Plan

### Phase 1: State Machine Core (`internal/tui/vim/`)

- [ ] **1.1** Create `types.go`: Define `VimMode`, `VimState`, `CommandState` (sealed interface), concrete command structs (`CommandIdle`, `CommandCount`, `CommandOperator`, `CommandOperatorCount`, `CommandOperatorFind`, `CommandOperatorTextObj`, `CommandFind`, `CommandG`, `CommandOperatorG`, `CommandReplace`, `CommandIndent`), `Operator`, `FindType`, `TextObjScope`, `PersistentState`, `RecordedChange` types. Define constant sets (`SimpleMotions`, `FindKeys`, `TextObjTypes`, `OperatorKeys`).
- [ ] **1.2** Create `motions.go`: Implement `ResolveMotion(motion string, text string, cursorPos int, count int) int` — returns new cursor offset for h/l/j/k/w/b/e/W/B/E/0/^/$/G/gg. Operates on raw string + offset (no textarea dependency).
- [ ] **1.3** Create `textobjects.go`: Implement `FindTextObject(objType string, scope TextObjScope, text string, cursorPos int) (start, end int)` — returns boundaries for word/WORD, quoted strings, bracket pairs.
- [ ] **1.4** Create `transitions.go`: Implement `Transition(state CommandState, input string, ctx TransitionContext) TransitionResult` — pure state machine matching Claude Code's transition table. Each state handler returns `{Next *CommandState, Execute func()}`.
- [ ] **1.5** Create `operators.go`: Implement operator execution functions that modify text and cursor position via an `OperatorContext` interface: `DeleteRange`, `ChangeRange`, `YankRange`, `ExecuteX`, `ExecutePaste`, `ExecuteReplace`, `ExecuteToggleCase`, `ExecuteJoin`, `ExecuteIndent`, `ExecuteOpenLine`, `ExecuteLineOp`.

### Phase 2: Handler Integration (`internal/tui/vim/handler.go`)

- [ ] **2.1** Create `VimHandler` struct that holds `VimState`, `PersistentState`, undo stack (`[]UndoEntry` where `UndoEntry = {Text string, Line int, Col int}`), and provides `HandleKey(msg tea.KeyPressMsg, ta *textarea.Model) (handled bool, cmd tea.Cmd, modeChanged bool)`.
- [ ] **2.2** Implement key preprocessing: map arrow keys to hjkl, Backspace to h, Delete to x in NORMAL mode.
- [ ] **2.3** Implement INSERT mode handling: track `InsertedText`, pass keys to textarea, handle Escape/Ctrl+C → switch to NORMAL.
- [ ] **2.4** Implement NORMAL mode handling: feed input to `Transition()`, execute returned function, apply cursor/text changes to textarea.
- [ ] **2.5** Implement mode switching: `switchToNormal()` records insertedText as lastChange, moves cursor left; `switchToInsert(offset)` clears insertedText tracking.
- [ ] **2.6** Implement undo stack: `pushUndo(text, line, col)` called before each mutating operation. `u` command pops stack and restores via `SetValue()` + cursor positioning. Stack capped at 100 entries.
- [ ] **2.7** Implement `setCursorPosition(ta *textarea.Model, targetLine, targetCol int)` helper: reads current position via `ta.Line()` and `ta.Column()`, computes delta `(targetLine - currentLine)`, calls `CursorDown()` or `CursorUp()` delta times, then `SetCursorColumn(targetCol)`. This is O(delta) not O(totalLines) and avoids scroll position resets. Implement `offsetToLineCol(text string, offset int) (line, col int)` converter using `strings.Count(text[:offset], "\n")` and `offset - strings.LastIndex(text[:offset], "\n") - 1`.

### Phase 3: Configuration

- [ ] **3.1** Add `VimMode bool` field to `TUIConfig` in `internal/config/config.go`.
- [ ] **3.2** Add JSON schema entry for `tui.vimMode`.

### Phase 4: Editor Integration

- [ ] **4.1** In `editorCmp`, add `vimHandler *vim.VimHandler` field initialized based on config.
- [ ] **4.2** In `editorCmp.Update()`, when vim mode is enabled and not in shell mode: intercept `tea.KeyPressMsg` and route through `vimHandler.HandleKey()` before the textarea.
- [ ] **4.3** Emit `VimModeChangedMsg` when the handler reports a mode change.
- [ ] **4.4** In `editorCmp.View()`, optionally change prompt indicator based on vim mode (e.g., block for NORMAL).
- [ ] **4.5** Ensure Escape/Ctrl+C in completion dialogs is handled before vim mode (dialog takes priority). In the chat page, when completion dialogs are open, Escape/Ctrl+C closes the dialog rather than switching vim modes.
- [ ] **4.6** Add `ConsumesCtrlC() bool` method to `editorCmp` — returns true when vim handler is active AND (mode is INSERT OR command state is not idle). Expose via chat page method.
- [ ] **4.7** In `tui.go`, add a `pageConsumesCtrlC()` check to the `keys.Quit` handler, mirroring the existing `pageIsShellMode()` pattern. When true, `break` so Ctrl+C flows to the editor's vim handler instead of toggling the quit dialog.

### Phase 5: Status Bar

- [ ] **5.1** Add `vimMode vim.VimMode` field to `statusCmp`.
- [ ] **5.2** Handle `VimModeChangedMsg` in `statusCmp.Update()`.
- [ ] **5.3** Render vim mode widget in `statusCmp.View()` at the leftmost position (before help widget) when vim mode is active. Style: `NORMAL` with primary background, `INSERT` with secondary.

### Phase 6: Testing

- [ ] **6.1** Unit tests for `motions.go`: verify each motion with various cursor positions and counts.
- [ ] **6.2** Unit tests for `textobjects.go`: verify word, quote, and bracket text objects.
- [ ] **6.3** Unit tests for `transitions.go`: verify state transitions for all command types (idle→operator→motion, counts, finds, text objects, g commands, replace, indent).
- [ ] **6.4** Unit tests for `operators.go`: verify delete/change/yank modify text correctly.
- [ ] **6.5** Integration test: verify dot-repeat replays recorded changes correctly.

## Edge Cases

### Vim Mode + Shell Mode

1. User is in NORMAL vim mode, types `!` — should this enter shell mode?
2. **Decision**: Shell mode activation (`!` on empty input) only triggers from INSERT vim mode. In NORMAL mode, `!` is not a vim command we support so it's a no-op. User must be in INSERT mode to activate shell.
3. When shell mode is active, vim handler is bypassed entirely.

### Vim Mode + Completion Dialogs

1. User types `@` in INSERT mode → completion dialog opens.
2. User presses Escape → dialog closes (not vim mode switch).
3. User presses Escape again → now switches to NORMAL mode.
4. **Implementation**: Chat page checks `showCompletionDialog` / `showCommandCompletionDialog` before routing Escape to the editor.

### Vim Mode + Attachment Delete Mode

1. User presses Ctrl+R → enters delete mode (handled before vim).
2. Vim handler should not process keys during delete mode.
3. **Implementation**: Delete mode check happens before vim handler in `Update()`.

### Ctrl+C Behavior — App-Level Intercept Conflict

**Problem**: The app-level `tui.go` handler (`keys.Quit` bound to `ctrl+c`) intercepts Ctrl+C **before** it reaches the chat page or editor. This means the vim handler would never see Ctrl+C unless the dispatch order changes. Shell mode already has a special case for this — `tui.go:559-562` calls `pageIsShellMode()` to skip the quit dialog.

**Solution**: Add a `pageIsVimInsertMode()` check (or generalized `pageConsumesCtrlC()`) to the app-level Ctrl+C handler in `tui.go`, mirroring the existing `pageIsShellMode()` pattern:

```go
case key.Matches(msg, keys.Quit):
    // In shell mode, ctrl+c exits shell mode instead of showing quit dialog
    if a.pageIsShellMode() {
        break
    }
    // In vim INSERT mode, ctrl+c switches to NORMAL instead of showing quit dialog
    if a.pageIsVimInsertMode() {
        break
    }
    a.showQuit = !a.showQuit
```

When the app-level handler `break`s, the key flows down to the chat page → editor → vim handler.

**Ctrl+C behavior by state:**
1. **INSERT mode + no dialog**: App-level check detects vim INSERT, breaks. Key flows to editor. Vim handler switches to NORMAL mode.
2. **NORMAL mode + pending command**: App-level check detects vim is NOT in INSERT, so it would show quit dialog. **Alternative**: Extend the check to also skip when vim has a pending command (`pageConsumesCtrlC()` returns true for INSERT mode OR pending command). This is cleaner.
3. **NORMAL mode + idle**: App-level handler is NOT bypassed → quit dialog shows. This is correct — Ctrl+C in idle NORMAL should behave as the app default.

**Implementation**: `editorCmp` exposes a `ConsumesCtrlC() bool` method returning true when vim mode is enabled AND (mode is INSERT OR command state is not idle). The chat page exposes this via a method the app model can query, identical to the `IsShellMode()` pattern already in use.

### Multiline Text

1. The textarea supports multiline input (via `\` continuation).
2. j/k motions should move between visual lines.
3. `gg` goes to first line, `G` to last line.
4. Line operations (dd, cc, yy, J, o, O) work on the current line within the multiline buffer.

### Empty Input

1. In NORMAL mode with empty text, most motions/operators are no-ops.
2. `i`, `a`, `o`, `O` should still switch to INSERT mode.
3. `dd` on empty input is a no-op.

### Count Overflow

1. Maximum count is 10000 (matching Claude Code's `MAX_VIM_COUNT`).
2. Counts beyond this are clamped.

### Paste (p/P) with Linewise Content

1. Linewise yank (dd, yy) stores text with `registerLinewise = true`.
2. `p` with linewise content inserts a new line below; `P` inserts above.
3. Non-linewise paste inserts at/before cursor position.

## Supported Commands Reference

### Mode Switching
| Key | Action |
|-----|--------|
| `Esc` | INSERT→NORMAL; NORMAL: cancel pending command |
| `Ctrl+C` | Same as Escape (INSERT→NORMAL; NORMAL: cancel pending) |
| `i` | Insert at cursor |
| `I` | Insert at line start |
| `a` | Append after cursor |
| `A` | Append at line end |
| `o` | Open line below |
| `O` | Open line above |

### Motions
| Key | Motion |
|-----|--------|
| `h` / `Left` | Left |
| `l` / `Right` | Right |
| `j` / `Down` | Down |
| `k` / `Up` | Up |
| `w` / `W` | Next word / WORD |
| `b` / `B` | Previous word / WORD |
| `e` / `E` | End of word / WORD |
| `0` | Start of line |
| `^` | First non-blank |
| `$` | End of line |
| `gg` | First line |
| `G` | Last line |
| `f{c}` / `F{c}` | Find char forward / backward |
| `t{c}` / `T{c}` | To char forward / backward |
| `;` / `,` | Repeat last find / reverse |

### Operators
| Key | Operator |
|-----|----------|
| `d{motion}` | Delete |
| `c{motion}` | Change (delete + insert) |
| `y{motion}` | Yank |
| `dd` / `cc` / `yy` | Line operation |
| `D` / `C` / `Y` | To end of line / line |

### Text Objects (with operators)
| Key | Object |
|-----|--------|
| `iw` / `aw` | Inner/around word |
| `iW` / `aW` | Inner/around WORD |
| `i"` / `a"` | Inner/around double quotes |
| `i'` / `a'` | Inner/around single quotes |
| `i(` / `a(` | Inner/around parens |
| `i[` / `a[` | Inner/around brackets |
| `i{` / `a{` | Inner/around braces |
| `i<` / `a<` | Inner/around angle brackets |

### Other Commands
| Key | Action |
|-----|--------|
| `x` | Delete char under cursor |
| `r{c}` | Replace char under cursor |
| `~` | Toggle case |
| `p` / `P` | Paste after/before |
| `.` | Repeat last change |
| `u` | Undo |
| `J` | Join lines |
| `>>` / `<<` | Indent / unindent |

## Resolved Questions

1. **Should vim mode be persisted across sessions or only configured in `.opencode.json`?**
   - **Decision**: Config-only via `.opencode.json`. No runtime toggle needed initially.

2. **Should the prompt character change in NORMAL mode?**
   - **Decision**: Yes. `>` in INSERT, colored `>` in NORMAL. Status bar widget is the primary indicator.

3. **Should `Ctrl+C` in NORMAL idle mode pass through to the app (quit dialog)?**
   - **Decision**: Yes. The app-level `tui.go` handler calls `pageConsumesCtrlC()` which returns false when vim is in NORMAL idle — so Ctrl+C shows the quit dialog. See "Ctrl+C Behavior" edge case for full details.

4. **How does Enter behave in NORMAL mode?**
   - **Decision**: Enter in NORMAL maps to `j` (move down). Enter only sends in INSERT mode. This prevents accidental message submission.

5. **How does the textarea expose cursor positioning for arbitrary offsets?**
   - **Decision**: It doesn't — `SetCursorOffset()` does not exist. The handler converts offsets to `(line, col)` via `offsetToLineCol()`, then uses delta-based positioning: read current `(Line(), Column())`, call `CursorUp()`/`CursorDown()` only the delta times, then `SetCursorColumn()`. See handler Phase 2.7.

6. **How does undo work without textarea undo support?**
   - **Decision**: Custom undo stack (snapshots of text + cursor position) maintained by the vim handler. Capped at 100 entries. See handler Phase 2.6.

## Success Criteria

- [ ] `tui.vimMode: true` in `.opencode.json` enables vim keybindings
- [ ] Editor starts in INSERT mode — user can type immediately
- [ ] Escape and Ctrl+C switch from INSERT to NORMAL
- [ ] All motions (hjkl, w/b/e, 0/^/$, gg/G, f/t) work with counts
- [ ] Operators (d/c/y) combine with motions and text objects
- [ ] Dot-repeat (`.`) replays last change
- [ ] Yank register (y/p/P) works for copy-paste within the editor
- [ ] Status bar shows current vim mode
- [ ] Shell mode, completion dialogs, attachment delete mode unaffected
- [ ] Enter key still sends messages in INSERT mode
- [ ] External editor (`ctrl+e`) still works
- [ ] No regression when vim mode is disabled (default)
- [ ] Unit tests cover motions, text objects, transitions, and operators

## References

- Claude Code vim implementation: `/Users/nouwa/Development/open-source-fork/claude-code-source-code/src/vim/`
- Claude Code vim hook: `/Users/nouwa/Development/open-source-fork/claude-code-source-code/src/hooks/useVimInput.ts`
- OpenCode editor: `internal/tui/components/chat/editor.go`
- OpenCode status bar: `internal/tui/components/core/status.go`
- OpenCode config: `internal/config/config.go`
- OpenCode chat page: `internal/tui/page/chat.go`
- Bubbles textarea: `charm.land/bubbles/v2/textarea`
