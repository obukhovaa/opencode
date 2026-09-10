# chat-editor-layout Specification

## Purpose
Invariants governing how the TUI chat editor component reserves horizontal and vertical
space: the prompt-width reservation that prevents the textarea from overflowing its
container, the prohibition on state mutation during render, and the no-overflow contract
that regression tests must enforce.
## Requirements
### Requirement: The editor textarea SHALL NOT exceed the container width

After `SetSize(w, h)` is called on the editor component, the visible width of a
fully-rendered editor view SHALL be less than or equal to `w` for all values of `w` at
or above the **minimum render floor**.

The minimum render floor is defined as:

    floor = promptColumnWidth() + textarea_minimum

where `promptColumnWidth()` is the column width of the rendered left-side prompt widget
(currently 2 columns: 1 left-padding + 1 prompt character), and `textarea_minimum` is
the smallest width the `bubbles` textarea can produce, imposed by the library's internal
clamp:

    inputWidth = max(w_requested, reservedInner + reservedOuter + 1)

With `Prompt = " "` (1 col) and no border (`reservedOuter = 0`), `textarea_minimum = 2`
(the 1-column internal prompt plus 1 minimum content column). The **current concrete
floor is 4 columns**. This value is a dependency-imposed constraint, not a choice this
component makes; a future change to the prompt string or to the `bubbles` library's
minimum-width logic MUST re-derive the floor and update the regression tests accordingly.

The editor renders a left-side prompt widget joined horizontally to the textarea. The
textarea allocation MUST reserve at least the column width of the rendered prompt so
that the combined render fits within `w` columns for all `w >= floor`. The exact
reservation (prompt width alone, or prompt width plus an additional margin) is an
implementation decision recorded in `design.md`; the normative requirement is the
no-overflow invariant, not the arithmetic used to achieve it.

The reservation MUST be derived from a single authoritative source shared by `SetSize`
and `View` — a standalone helper or stored field — so that the two sites cannot diverge
independently. Magic-number literals that appear at only one call site are prohibited.

The reservation is invariant across normal mode, shell mode (`$` prompt), and vim-normal
mode: all three vary the prompt's color but not its column width. Should a future change
introduce a multi-character prompt the reservation MUST be re-derived and the regression
test MUST be updated at the same time.

For widths below the floor, the following obligations apply instead of the `<= w`
invariant:

- The component MUST NOT panic.
- The textarea width MUST be clamped to zero (no negative allocation passed to the
  library).
- The component MUST NOT add any overflow of its own beyond the unavoidable floor: the
  rendered width MUST NOT exceed `floor` even when `w < floor`. This keeps the
  requirement testable — it is what the regression test's `bound := max(w, floor)`
  assertion checks.

#### Scenario: Editor renders within its container width — normal input

- **GIVEN** an editor component with `SetSize(80, 10)` applied
- **AND** the editor is in normal (insert) mode with some text typed
- **WHEN** the view is rendered
- **THEN** `lipgloss.Width(view.Content) <= 80`

#### Scenario: Editor renders within its container width — shell mode

- **GIVEN** an editor component with `SetSize(80, 10)` applied and shell mode active
- **WHEN** the view is rendered
- **THEN** `lipgloss.Width(view.Content) <= 80`

#### Scenario: Editor renders within its container width — with attachments

- **GIVEN** an editor component with `SetSize(80, 10)` applied
- **AND** one or more file attachments present
- **WHEN** the view is rendered
- **THEN** `lipgloss.Width(view.Content) <= 80`

#### Scenario: No overflow across a range of container widths

- **GIVEN** an editor with `SetSize(w, 10)` for each w in {1, 2, 3, 5, 10, 20, 40, 80, 120}
- **WHEN** the view is rendered at each width
- **THEN** for each `w >= floor` (currently 4): `lipgloss.Width(view.Content) <= w`
- **AND** for each `w < floor`: `lipgloss.Width(view.Content) <= floor` and no panic

#### Scenario: Degenerate width does not panic

- **GIVEN** an editor with `SetSize(1, 10)` applied
- **WHEN** the view is rendered
- **THEN** no panic occurs
- **AND** `lipgloss.Width(view.Content) <= floor` (currently 4 — the bubbles-imposed
  minimum, not the container width; `<= 1` is unachievable at this width because the
  prompt widget and textarea each have fixed minimums that sum to `floor`)

#### Scenario: A line of w characters wraps rather than overflows

- **GIVEN** an editor with `SetSize(w, 10)` applied
- **AND** a string of exactly `w` printable ASCII characters entered as input
- **WHEN** the view is rendered
- **THEN** no visible line of the rendered output exceeds `w` columns
- **AND** the input wraps onto a second rendered line rather than extending beyond the boundary

### Requirement: Editor height SHALL be computed in Update, not mutated in View

The editor component's effective textarea height MUST be a function of model state
computed when that state changes — specifically when the affordance row above the
textarea appears or disappears. It MUST NOT be computed or mutated inside `View()`.

The affordance row is a single line above the textarea shared by every affordance
the editor draws there: file attachments and the slash-invocation recognition hint.
When at least one affordance is present the textarea height SHALL be `m.height - 1`
to leave room for that one row; when none is present the height SHALL be `m.height`.
The reservation SHALL remain one row when several affordances are active at once.
The transition between the two states MUST happen in response to the messages
processed by `Update` that change an affordance — attachment add and remove, and a
change to the draft that alters which invocations are recognized.

Mutating `textarea.SetHeight` inside `View()` is prohibited because:
1. It violates the Elm/Bubble Tea model — `View` is a pure projection of state, not a
   place to produce side effects.
2. It leaves the textarea height incorrect when attachments are removed (the shrunken
   height persists until the next `SetSize` call).

#### Scenario: Attachment added shrinks textarea height

- **GIVEN** an editor component with `SetSize(80, 10)` applied and no attachments
- **WHEN** an attachment is added (the relevant message is processed by `Update`)
- **THEN** the textarea height becomes `m.height - 1` (9 in this example)
- **AND** subsequent calls to `View()` reflect this height without any additional mutation

#### Scenario: Attachment removed restores textarea height

- **GIVEN** an editor with one attachment and textarea height `m.height - 1`
- **WHEN** the last attachment is removed (the relevant message is processed by `Update`)
- **THEN** the textarea height reverts to `m.height`
- **AND** `View()` does not need to set height to render correctly

#### Scenario: A recognized invocation reserves the same single row

- **GIVEN** an editor component with `SetSize(80, 10)` applied, no attachments, and an
  empty draft
- **WHEN** the draft becomes a recognized slash invocation
- **THEN** the textarea height becomes `m.height - 1`
- **AND** adding an attachment while that invocation is still recognized leaves the
  height at `m.height - 1`, because both affordances share one row

#### Scenario: Editing the invocation away releases the row

- **GIVEN** an editor whose only affordance is a recognized slash invocation
- **WHEN** the draft is edited so that nothing is recognized
- **THEN** the textarea height reverts to `m.height` without a further `SetSize` call

#### Scenario: View is a pure projection

- **GIVEN** any editor state
- **WHEN** `View()` is called
- **THEN** no method on the textarea model that mutates state is invoked during the call

### Requirement: Post-render overlays SHALL preserve the rendered width

The editor may post-process the textarea's rendered output before returning it from
`View()` — for example to repair unstyled background cells or to draw a selection
highlight. Any such overlay SHALL preserve the visible cell width of every line it
rewrites.

This binds the overlay to the same no-overflow contract as the textarea itself: the
no-overflow invariant is asserted against the editor's final rendered output, so an
overlay that widened a line by even one cell would break it at every width. An overlay
MUST therefore operate on display cells, accounting for ANSI escape sequences and for
characters whose display width is not one column.

#### Scenario: Selection highlight does not widen the view

- **GIVEN** an editor rendering a draft with a selection highlight active
- **WHEN** `View()` is called at any width at or above the minimum render floor
- **THEN** the visible width of every rendered line equals its width with the highlight
  absent, and the no-overflow invariant still holds

#### Scenario: Wide characters survive the overlay

- **GIVEN** a draft containing characters whose display width is two columns
- **WHEN** an overlay rewrites the line containing them
- **THEN** the rendered line's visible width is unchanged and no character is split

### Requirement: Display coordinates for overlays SHALL be computed in Update

Any display-cell coordinate an overlay needs — such as the display rows and column spans
covered by a selection — MUST be derived when the state it depends on changes, inside
`Update`, and stored on the model. It MUST NOT be derived inside `View()`.

This follows the existing prohibition on state mutation during render for the same reason
and with an additional one: deriving display coordinates requires interrogating the
textarea's own layout, and doing so during render risks mutating the textarea's scroll
position as a side effect of the query.

#### Scenario: Coordinates recomputed on selection change

- **GIVEN** an editor with an active selection
- **WHEN** a key that changes the selection is processed by `Update`
- **THEN** the stored display coordinates reflect the new selection before `View()` runs

#### Scenario: Render does not disturb the textarea

- **GIVEN** an editor with an active selection and a draft long enough to scroll
- **WHEN** `View()` is called repeatedly
- **THEN** the textarea's scroll offset and cursor position are unchanged by rendering
