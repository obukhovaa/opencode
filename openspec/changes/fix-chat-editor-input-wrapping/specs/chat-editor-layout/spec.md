## Purpose

Invariants governing how the TUI chat editor component reserves horizontal and vertical
space: the prompt-width reservation that prevents the textarea from overflowing its
container, the prohibition on state mutation during render, and the no-overflow contract
that regression tests must enforce.

## ADDED Requirements

### Requirement: The editor textarea SHALL NOT exceed the container width

After `SetSize(w, h)` is called on the editor component, the visible width of a
fully-rendered editor view SHALL be less than or equal to `w` for all values of `w`
greater than or equal to zero.

The editor renders a left-side prompt widget joined horizontally to the textarea. The
textarea allocation MUST reserve at least the column width of the rendered prompt so
that the combined render fits within `w` columns. The exact reservation (prompt width
alone, or prompt width plus an additional margin) is an implementation decision recorded
in `design.md`; the normative requirement is the no-overflow invariant, not the
arithmetic used to achieve it.

The reservation MUST be derived from a single authoritative source shared by `SetSize`
and `View` — a standalone helper or stored field — so that the two sites cannot diverge
independently. Magic-number literals that appear at only one call site are prohibited.

The reservation is invariant across normal mode, shell mode (`$` prompt), and vim-normal
mode: all three vary the prompt's color but not its column width. Should a future change
introduce a multi-character prompt the reservation MUST be re-derived and the regression
test MUST be updated at the same time.

Degenerate widths (w less than the minimum reservation) MUST NOT cause a negative
allocation or a panic; the textarea width MUST be clamped to zero.

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
- **THEN** `lipgloss.Width(view.Content) <= w` for every width tested

#### Scenario: Degenerate width does not panic

- **GIVEN** an editor with `SetSize(1, 10)` applied
- **WHEN** the view is rendered
- **THEN** no panic occurs and `lipgloss.Width(view.Content) <= 1`

#### Scenario: A line of w characters wraps rather than overflows

- **GIVEN** an editor with `SetSize(w, 10)` applied
- **AND** a string of exactly `w` printable ASCII characters entered as input
- **WHEN** the view is rendered
- **THEN** no visible line of the rendered output exceeds `w` columns
- **AND** the input wraps onto a second rendered line rather than extending beyond the boundary

### Requirement: Editor height SHALL be computed in Update, not mutated in View

The editor component's effective textarea height MUST be a function of model state
computed when that state changes — specifically when attachments are added or removed.
It MUST NOT be computed or mutated inside `View()`.

When attachments are present, the textarea height SHALL be `m.height - 1` to leave room
for the attachment row. When no attachments are present the textarea height SHALL be
`m.height`. The transition between the two states MUST happen in response to attachment
add and remove messages processed by `Update`.

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

#### Scenario: View is a pure projection

- **GIVEN** any editor state
- **WHEN** `View()` is called
- **THEN** no method on the textarea model that mutates state is invoked during the call
