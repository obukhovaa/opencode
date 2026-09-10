## ADDED Requirements

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
