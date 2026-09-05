## Why

The TUI chat input overflows its container by 2 columns: text and the cursor become
invisible as the user types toward the right edge of the terminal. A dead
`SetWidth(width)` call in `editorCmp.SetSize` silently overwrites the deliberately
corrected `SetWidth(width - 3)` one line above it — the bug is visible in the source and
was introduced when the two call sites were edited without being reconciled. A secondary
violation — `SetHeight` called inside `View()` — mutates state during render, which is
illegal under the Elm/Bubble Tea model and leaves height incorrect after attachments are
removed.

## What Changes

- **Delete the dead `SetWidth(width)` line** in `editorCmp.SetSize`
  (`internal/tui/components/chat/editor.go:453`) and replace the magic-number correction
  with a named constant or a shared derived value so the reservation and the prompt
  renderer cannot drift independently again.
- **Move the `SetHeight` call** out of `editorCmp.View()` and into `Update` in response
  to attachment add/remove messages, so height is always a function of model state
  computed in `Update`, never mutated during render.
- **Add a regression test** (`internal/tui/components/chat/editor_test.go`) asserting
  that `lipgloss.Width(editor.View().Content) <= w` after `SetSize(w, h)` for a range
  of widths including degenerate values, in both normal and shell mode, with and without
  attachments.

## Capabilities

### New Capabilities

- `chat-editor-layout`: invariants governing how the chat editor component reserves
  horizontal and vertical space — the prompt-width reservation, the prohibition on
  state mutation during render, and the no-overflow contract that regression tests must
  enforce.

### Modified Capabilities

<!-- No existing spec-level requirements change — this is a new capability. -->

## Impact

**`github.com/opencode-ai/opencode`**

- `internal/tui/components/chat/editor.go:448-455` (`SetSize`): remove dead
  `SetWidth(width)` overwrite; derive reservation from prompt geometry.
- `internal/tui/components/chat/editor.go:440` (`View`): remove `SetHeight` mutation;
  wire height recalculation into `Update`.
- `internal/tui/components/chat/editor_test.go` (new): regression test covering the
  no-overflow invariant across widths 1–120, both modes, and the attachment path.

**Explicit non-impacts (verified):**

- `internal/tui/page/chat.go:473` (`editorWidth, editorHeight := p.editor.GetSize()`):
  `p.editor` is the `layout.Container` wrapping the editorCmp; `container.GetSize()`
  returns the container's own allocated dimensions, which the fix does not change. The
  completion-dialog width is unaffected.
- No direct callers of `editorCmp.GetSize()` exist outside the component itself; the
  container's `GetSize()` is the only method observed at the call site.
- `CreateTextArea(existing)` (`editor.go:491-521`) inherits whatever width `SetSize`
  last set via `existing.Width()`; it is automatically correct once `SetSize` is fixed.
- No changes to `internal/tui/layout/split.go`, the split-pane ratio, or the
  message-list layout.
- Dynamic editor height (`DynamicHeight` in bubbles) is out of scope; height remains a
  fixed panel controlled by the split-pane's `verticalRatio`.
