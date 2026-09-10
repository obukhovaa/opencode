# tui-vim-mode Specification

## Purpose
Defines the chat editor's vim editing model — the set of modes it can be in, how VISUAL
and VISUAL LINE anchor and extend a selection, which operators act on that selection, and
how each mode participates in the editor's key routing and its status indicator.

## Requirements

### Requirement: The editor SHALL support VISUAL and VISUAL LINE modes

When vim mode is enabled, the editor's mode set SHALL include INSERT, NORMAL, VISUAL, and
VISUAL LINE. From NORMAL, `v` SHALL enter VISUAL and `V` SHALL enter VISUAL LINE, each
anchoring a selection at the cursor.

`esc` SHALL return from either visual mode to NORMAL, discarding the selection but
remembering it. `v` in VISUAL and `V` in VISUAL LINE SHALL return to NORMAL. `v` in VISUAL
LINE SHALL switch to VISUAL and `V` in VISUAL SHALL switch to VISUAL LINE, preserving the
anchor and cursor.

#### Scenario: Entering VISUAL
- **GIVEN** the editor is in NORMAL mode
- **WHEN** the user presses `v`
- **THEN** the editor is in VISUAL mode with a selection anchored at the cursor

#### Scenario: Leaving VISUAL with esc
- **GIVEN** the editor is in VISUAL mode with a selection
- **WHEN** the user presses `esc`
- **THEN** the editor is in NORMAL mode, the text is unchanged, and no selection is shown

#### Scenario: Switching between the visual modes
- **GIVEN** the editor is in VISUAL mode with a selection
- **WHEN** the user presses `V`
- **THEN** the editor is in VISUAL LINE mode and the selection covers the whole lines the
  previous selection touched

### Requirement: Motions SHALL extend the selection

In a visual mode, the motions the editor supports in NORMAL mode SHALL move the cursor and
extend the selection from the anchor to the new cursor position. Counts SHALL apply as
they do in NORMAL mode.

In VISUAL the selection covers the characters between anchor and cursor inclusive. In
VISUAL LINE it covers every line the anchor and cursor touch, in full.

`o` SHALL exchange the anchor and the cursor, so the opposite end of the selection becomes
the one that motions move.

#### Scenario: Extending with a word motion
- **GIVEN** the draft is `one two three` with the cursor at the start and the editor is in
  VISUAL mode
- **WHEN** the user presses `w`
- **THEN** the selection covers from the anchor through the new cursor position

#### Scenario: Counted motion in visual mode
- **GIVEN** the editor is in VISUAL mode
- **WHEN** the user presses `3l`
- **THEN** the cursor has moved three characters and the selection covers them

#### Scenario: Swapping ends
- **GIVEN** a selection extends rightwards from its anchor
- **WHEN** the user presses `o`
- **THEN** the cursor sits at the former anchor, the anchor sits at the former cursor, and
  the selected range is unchanged

#### Scenario: Line selection covers whole lines
- **GIVEN** a two-line draft and the cursor mid-way through the first line
- **WHEN** the user presses `V` then `j`
- **THEN** both lines are selected in full, including from the first column

### Requirement: Operators SHALL act on the selection and return to NORMAL

In a visual mode the system SHALL support at least: `d` and `x` (delete), `c` and `s`
(change), `y` (yank), `~` (toggle case), `u` (lowercase), `U` (uppercase), `>` and `<`
(indent), `J` (join), and `p` (replace the selection with the register).

Each SHALL apply to the current selection, record the register with the correct
characterwise or linewise flag, and leave the editor in NORMAL mode — except `c` and `s`,
which leave it in INSERT mode. Each SHALL be undoable by `u` in NORMAL mode as a single
step.

#### Scenario: Deleting a selection
- **GIVEN** a VISUAL selection covering `two `in the draft `one two three`
- **WHEN** the user presses `d`
- **THEN** the draft is `one three`, the editor is in NORMAL mode, and the register holds
  the deleted text characterwise

#### Scenario: Changing a selection
- **GIVEN** a VISUAL selection
- **WHEN** the user presses `c`
- **THEN** the selected text is removed and the editor is in INSERT mode at that position

#### Scenario: Linewise yank
- **GIVEN** a VISUAL LINE selection covering one whole line
- **WHEN** the user presses `y`
- **THEN** the register holds that line marked linewise and the editor is in NORMAL mode

#### Scenario: Undoing a visual-mode operator
- **GIVEN** a visual-mode delete has just been applied
- **WHEN** the user presses `u` in NORMAL mode
- **THEN** the draft is restored to its state before the delete, in one step

### Requirement: The last selection SHALL be restorable

`gv` in NORMAL mode SHALL re-enter the visual mode that was last active, restoring its
anchor and cursor. When no selection has been made since vim mode was enabled, `gv` SHALL
do nothing.

#### Scenario: Restoring a selection
- **GIVEN** the user made a VISUAL selection and pressed `esc`
- **WHEN** the user presses `gv`
- **THEN** the editor is in VISUAL mode with the same anchor and cursor as before

#### Scenario: No prior selection
- **GIVEN** no selection has been made
- **WHEN** the user presses `gv`
- **THEN** the editor stays in NORMAL mode and the draft is unchanged

### Requirement: The selection SHALL be visible

While a visual mode is active, the selected text SHALL be rendered with a distinct
background drawn from the active theme, so the user can see the extent of the selection
before acting on it.

The highlight SHALL track the editor's own text layout: it MUST remain correct across soft
wrapping, across scrolling within the input, and at every width the editor is rendered at.
When no visual mode is active, the rendered editor MUST be byte-identical to what it was
before this capability existed.

#### Scenario: Selection is highlighted
- **GIVEN** the editor is in VISUAL mode with a selection
- **WHEN** the editor is rendered
- **THEN** the selected cells carry the selection background and the unselected cells do
  not

#### Scenario: Selection spanning a soft wrap
- **GIVEN** a draft long enough to soft-wrap in the editor's width
- **AND** a selection that starts on one display row and ends on a later one
- **WHEN** the editor is rendered
- **THEN** every display row the selection covers is highlighted across the correct
  columns, with no highlighted cell outside the selection

#### Scenario: No selection, no change
- **GIVEN** the editor is in NORMAL or INSERT mode
- **WHEN** the editor is rendered
- **THEN** the output is identical to the output produced before selection rendering
  existed

### Requirement: Visual modes SHALL participate in key routing and the status badge

The status indicator SHALL show `VISUAL` and `V-LINE` when those modes are active, styled
distinctly from `INSERT` and `NORMAL`.

`esc` in a visual mode SHALL be consumed by the editor to return to NORMAL; it MUST NOT
cancel a running agent request or open the quit dialog. `ctrl+c` in a visual mode SHALL
likewise return to NORMAL rather than opening the quit dialog.

Mode-dependent behavior elsewhere in the TUI MUST treat the visual modes explicitly rather
than falling into the branch meant for NORMAL.

#### Scenario: Status badge in VISUAL
- **GIVEN** the editor is in VISUAL mode
- **WHEN** the status bar is rendered
- **THEN** it shows `VISUAL`

#### Scenario: Esc in VISUAL while the agent is running
- **GIVEN** the agent is running and the editor is in VISUAL mode
- **WHEN** the user presses `esc`
- **THEN** the editor returns to NORMAL mode and the agent request is not cancelled

#### Scenario: Ctrl+C in VISUAL
- **GIVEN** the editor is in VISUAL mode
- **WHEN** the user presses `ctrl+c`
- **THEN** the editor returns to NORMAL mode and no quit dialog is shown

### Requirement: Shell mode SHALL NOT be entered from a visual mode

The `!` key SHALL enter shell mode only from the editor's normal (non-vim) mode or from
vim INSERT mode. In NORMAL, VISUAL, or VISUAL LINE, `!` SHALL be handled as vim input.

#### Scenario: Bang in VISUAL
- **GIVEN** the editor is in VISUAL mode
- **WHEN** the user presses `!`
- **THEN** the editor does not enter shell mode
