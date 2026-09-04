# Design: fix-chat-editor-input-wrapping

## Context

See `proposal.md – Why` for motivation.

`editorCmp.SetSize` calls `m.textarea.SetWidth` twice: first with `width - 3` (the
correct value, with a comment stating intent) and then immediately with `width` (a dead
line that overwrites the first). The bubbles v2 textarea renders in exactly `w` columns
when `SetWidth(w)` is called — its internal 1-column `Prompt` is already accounted for
inside the widget. The editor renders the textarea joined horizontally to a 2-column
prompt widget (`lipgloss.Padding(0,0,0,1)` + one prompt character). With
`SetWidth(width)` in effect the combined render is `width + 2` columns wide in a
`width`-wide container.

The container wrapping the editor passes its inner content width directly — it has no
left/right border or horizontal padding, so `horizontalSpace == 0` and the editor sees
the full terminal width (verified in `container.SetSize`, `container.go:72-102`).

A secondary violation: `View()` calls `m.textarea.SetHeight(m.height - 1)` on the
attachments path (line 440), mutating model state during render.

## Goals / Non-Goals

**Goals:**
- The combined render of prompt + textarea fits within the container width for all
  inputs, including degenerate ones (`w == 0`, `w == 1`).
- The prompt-column reservation is expressed in one place and is derivable from the
  rendered prompt's actual column width — not a free-standing magic number.
- Editor height reflects attachment presence computed in `Update`; `View` makes no
  calls that mutate the textarea.
- A regression test in `editor_test.go` makes the invariant machine-verifiable.

**Non-Goals:**
- Dynamic editor height (`DynamicHeight`): height is a fixed panel; auto-grow is a
  separate enhancement outside this change.
- Changes to the split-pane ratio or message-list layout.
- Changing the prompt characters themselves (`>`, `$`) or adding mode badges; the prompt
  column width remains exactly 2 for all current modes.

## Decisions

### Decision 1: How to express the reservation — named constant vs. derived value

**Option A: Named constant** `promptWidth = 2` in `editor.go`.

- One import-free line that is impossible to misread.
- Fragile if a future change widens the prompt without updating the constant.

**Option B: Derive from `lipgloss.Width(style.Render(promptChar))`** in `SetSize`.

- Always agrees with what `View` actually renders.
- Costs a tiny allocation each time `SetSize` is called (negligible — `SetSize` is
  called on resize events, not per-frame).
- Eliminates the class of future bug the dossier documents: the dead line existed
  because arithmetic was edited in one site and not the other.

**Decision: Option B — derive from the rendered prompt.**

The rendered prompt is already computed in `View`; `SetSize` should compute the same
value (or store it when `View` runs and read it in `SetSize`, or vice versa). The
preferred approach is to add a small unexported helper `promptColumnWidth() int` that
both `SetSize` and `View` call, so the number has exactly one authoritative source.
This makes a future multi-character prompt (`##`, mode badge) impossible to introduce
without updating the allocation automatically.

The spec states that the invariant holds across normal, shell, and vim modes because all
vary only color, not column width. A concrete comment at the helper MUST state this so
reviewers understand the assumption.

### Decision 2: Attachment height — where to move the SetHeight call

Moving `SetHeight` from `View` to `Update` requires knowing which messages signal
attachment add/remove. The editor already handles `dialog.AttachmentAddedMsg` for adding
attachments (search confirms handling in `Update`). Removal paths should be checked and
handled symmetrically.

The height rule is: `m.height - 1` when `len(m.attachments) > 0`, else `m.height`.
This should be encapsulated in a small private method `syncTextareaHeight()` called at
the end of every `Update` branch that can change `m.attachments` or `m.height`, and
also at the end of `SetSize`. This way neither `View` nor a future `Update` branch needs
to remember to call it.

### Decision 3: Regression test approach

There is no `teatest` harness in `go.mod` and no golden-file infrastructure. The test
should be a plain Go unit test that:
1. Constructs an `editorCmp` directly (or via `NewEditorCmp` with a test `*app.App`
   if that constructor requires one — confirm whether a nil `app` is usable in tests).
2. Calls `SetSize(w, h)` and then `m.View()`.
3. Asserts `lipgloss.Width(view.Content) <= w`.
4. Covers widths `{1, 2, 3, 5, 20, 40, 80, 120}`, both modes, with/without attachments.
5. Covers the negative-width guard (`SetWidth(-1)` from `SetWidth(width - 2)` when
   `width == 1` would be `-1` — bubbles clamps internally, but the guard at the call
   site keeps intent explicit).

If `NewEditorCmp` requires a fully initialized `*app.App`, stub out the minimal fields
or add an unexported constructor for tests. Do not change the production API.

## Discovered Constraint: bubbles textarea minimum-width floor

During implementation it was confirmed that `charm.land/bubbles/v2` `textarea.SetWidth`
enforces an internal minimum via:

    inputWidth = max(w_requested, reservedInner + reservedOuter + 1)

With the editor's configuration (`Prompt = " "`, no line numbers, no border):
- `reservedInner = uniseg.StringWidth(" ") = 1`
- `reservedOuter = 0` (no horizontal frame)
- minimum `inputWidth = max(w, 2)`, so the textarea always renders at least 2 columns

Combined with the 2-column editor prompt widget this gives a **minimum render floor of
4 columns** for the full editor view. Widths below 4 cannot satisfy `<= w` regardless
of how correctly the component is implemented — the floor is imposed by the dependency,
not by a design choice here.

This constraint is deliberately specified in `specs/chat-editor-layout/spec.md`:
- For `w >= 4`: the strict `lipgloss.Width(view.Content) <= w` invariant applies.
- For `w < 4`: no panic, no negative allocation (textarea SetWidth clamped at 0), and
  rendered width MUST NOT exceed the floor (the component adds no extra overflow).

The regression test implements this with `bound := max(w, minRender)` where
`minRender = promptColumnWidth() + 2`.

## Risks / Trade-offs

**[Risk] Derived prompt width adds a non-zero allocation per resize.**  
→ Resize is an infrequent event; the allocation is immaterial compared to Bubble Tea's
own rendering overhead.

**[Risk] Future multi-character prompt breaks the invariant silently if the helper is
bypassed.**  
→ The shared `promptColumnWidth()` helper + a test that asserts `<= w` (not `== w - 2`)
makes any widening caught by CI automatically.

**[Risk] Moving `SetHeight` to `Update` requires knowing every message path that
changes attachments.**  
→ Use `syncTextareaHeight()` called at the end of `SetSize` and at the end of every
`Update` branch, so coverage is structural rather than exhaustive. A missed branch
defaults to the last synced height rather than to silence.

**[Risk] `editorCmp.GetSize()` callers receive a 2-column-smaller width after the fix.**  
→ Verified: no caller of `editorCmp.GetSize()` exists outside the component. The
container's `GetSize()` is the only one called from `page/chat.go:473`, and it returns
container dimensions — unaffected by the fix.

### Decision 4: Reservation width — exact fill (`w - 2`) vs one-column right margin (`w - 3`)

The textarea allocation is `w - promptWidth - margin`, where `promptWidth == 2` and the
choice is `margin == 0` (exact fill) or `margin == 1` (right margin).

**Option A: Exact fill (`margin = 0`, allocation = `w - 2`)**

- Maximises usable text width; the textarea can render up to `w - 2` columns of content.
- Places the cursor in the terminal's rightmost column when the user types to the end of
  a line. VT100 specifies *deferred wrap* (pending-wrap state) there: the cursor sits
  visually in the last column, and the next character triggers the wrap. Several
  terminal emulators implement this inconsistently — iTerm2 and some Linux VTE builds
  duplicate the last glyph or clip the cursor in this state.

**Option B: One-column right margin (`margin = 1`, allocation = `w - 3`)**

- The cursor can never reach the terminal's final column during normal typing; wrapping
  occurs one column inward, well clear of the pending-wrap boundary.
- Costs one column of usable width — imperceptible in practice.
- The original code was `SetWidth(width - 3)` with the comment
  `// account for the prompt and padding right`. The `-3` was deliberate:
  2 columns for the prompt + 1 for right padding. The dead overwrite line that
  introduced the bug erased this intent; the fix should restore it, not silently
  discard it.

**Decision: Option B — keep the one-column right margin.**

The deferred-wrap glitch is a real, documented terminal behavior, the original author
chose the margin deliberately, and the cost is one column. The reservation in
`SetSize` becomes `promptColumnWidth() + 1`; the `+ 1` MUST be commented as the
right-margin guard so a future reader does not "optimize" it away.

## Open Questions

None. All decisions needed to write tasks are resolved above.
