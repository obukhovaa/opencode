# Tasks: fix-chat-editor-input-wrapping

## 1. Fix the horizontal overflow (primary defect)

- [x] 1.1 Add unexported helper `promptColumnWidth() int` on `*editorCmp`
  (`internal/tui/components/chat/editor.go`) that returns
  `lipgloss.Width(style.Render(promptChar))` where `style` and `promptChar` are the same
  values used by `View()`. Add a comment stating the assumption: all current modes vary
  color only, not column width, so a single value is valid for normal, shell, and
  vim-normal modes; a future multi-character prompt MUST update this helper.

- [x] 1.2 In `editorCmp.SetSize` (`editor.go:448-455`), replace the two `SetWidth` calls
  with a single call: `m.textarea.SetWidth(max(0, width-m.promptColumnWidth()-1))`.
  The `- 1` is the deliberate right-margin guard (see `design.md` Decision 4);
  it MUST be preserved as a named constant or an explicit comment so a future reader
  cannot mistake it for rounding error. Remove the dead `m.textarea.SetWidth(width)`
  line entirely. Replace the existing `// account for the prompt and padding right`
  comment with a reference to `promptColumnWidth()` and the right-margin rationale.

- [x] 1.3 Confirm that `CreateTextArea(existing)` (`editor.go:491-521`) copies
  `existing.Width()` — which, after the fix, is the correct reduced width. No change
  needed; add a brief comment noting this for reviewers if not already present.

## 2. Fix the state mutation in View (secondary defect)

- [x] 2.1 Add unexported helper `syncTextareaHeight()` on `*editorCmp` that sets
  `m.textarea.SetHeight(m.height - 1)` when `len(m.attachments) > 0` and
  `m.textarea.SetHeight(m.height)` otherwise.

- [x] 2.2 Call `m.syncTextareaHeight()` at the end of `editorCmp.SetSize` (after the
  `SetWidth` fix from task 1.2) and at the end of every `Update` branch that modifies
  `m.attachments` (attachment add, attachment delete, all-attachments-cleared, and any
  other path that changes `len(m.attachments)`).

- [x] 2.3 Remove the `m.textarea.SetHeight(m.height - 1)` call from `editorCmp.View()`
  (`editor.go:440`). `View()` MUST NOT call any method on `m.textarea` that mutates
  state.

- [x] 2.4 Verify that attachment removal correctly restores textarea height immediately
  (not only on the next `SetSize`) by tracing all code paths that remove from
  `m.attachments` and confirming `syncTextareaHeight()` is called on each.

## 3. Regression tests

- [x] 3.1 Create `internal/tui/components/chat/editor_test.go`. Confirm whether
  `NewEditorCmp` requires a non-nil `*app.App`; if so, provide the minimum required
  fields or add an unexported test constructor that bypasses the requirement. Do not
  change the production API.

- [x] 3.2 Write `TestEditorCmpNoOverflow`: for each width in `{1, 2, 3, 5, 20, 40, 80, 120}`,
  in both normal mode and shell mode, call `editor.SetSize(w, 10)` then
  `editor.View()` and assert `lipgloss.Width(view.Content) <= bound` where
  `bound = max(w, minRender)` and `minRender = promptColumnWidth() + 2` (the
  bubbles-imposed floor; see `design.md – Discovered Constraint`). For `w >= 4`
  (the current floor) this reduces to `<= w`; for `w < 4` the floor binds instead.
  Use `t.Run(fmt.Sprintf("w=%d/mode=%s", w, mode), ...)` as subtests.
  (The original spec said `<= w` for all widths including 1–3; that was corrected once
  implementation confirmed the bubbles textarea's `max(w, reservedInner+1)` clamp makes
  the strict bound unachievable below the floor. The deviation is now folded into the
  spec rather than standing as an unresolved deviation.)

- [x] 3.3 Within the same test matrix, add a subcase with one attachment present and
  assert `lipgloss.Width(view.Content) <= w` (verifies both the width and height paths).

- [x] 3.4 Write `TestEditorCmpNoPanic`: call `editor.SetSize(1, 10)` and `editor.View()`
  and assert the call does not panic. Width 1 exercises the `max(0, 1-3) == 0` clamping
  path (prompt 2 cols + right margin 1 col). The original spec also asserted
  `lipgloss.Width(view.Content) <= 1`; that claim was corrected — the achievable bound
  at `w=1` is `<= floor` (4), not `<= 1`. `TestEditorCmpNoPanic` verifies only the
  no-panic obligation; the bound assertion for degenerate widths is covered by
  `TestEditorCmpNoOverflow` via the `max(w, minRender)` formulation.

- [x] 3.5 Write `TestEditorCmpHeightSync`: assert that after `SetSize(80, 10)` with no
  attachments, `m.textarea.Height() == 10`; after processing an
  `AttachmentAddedMsg`, `m.textarea.Height() == 9`; after the attachment is removed,
  `m.textarea.Height() == 10` again — without a second `SetSize` call.

- [x] 3.6 Write `TestEditorCmpLineWrap`: set `SetSize(w, 10)`, enter a value string of
  exactly `w` printable ASCII characters via `m.textarea.SetValue(...)`, call `View()`,
  split the rendered content by newline, and assert that no individual line exceeds `w`
  columns (using `lipgloss.Width` per line). Cover at least `w ∈ {20, 40, 80}`.

## 4. Verification

- [x] 4.1 Run `go test ./internal/tui/components/chat/` and confirm all new tests pass.

- [x] 4.2 Run `go build ./...` and confirm no compilation errors.

- [x] 4.3 Run `make test` (per `CLAUDE.md`) and confirm the full test suite and
  formatters pass, including `gofmt`.

- [ ] 4.4 Manual smoke test: launch `opencode` in a terminal narrower than the default
  (e.g. 60 columns), type a long message, and confirm text wraps before the right edge
  of the terminal rather than disappearing. Verify with and without a file attachment.
  Additionally: type until the cursor reaches the last usable column and confirm the
  cursor remains visible and correctly positioned (not duplicated or clipped) in at
  least two terminal emulators (e.g. iTerm2 and the macOS built-in Terminal.app or
  a Linux VTE build). This confirms the right-margin guard is effective.
