## 1. Overlays consume input, not just key presses

- [x] 1.1 Add `consumesInput(msg tea.Msg) bool` in `internal/tui/tui.go` covering
      `tea.KeyPressMsg`, `tea.PasteMsg`, `tea.PasteStartMsg`, `tea.PasteEndMsg`, and
      replace the `tea.KeyPressMsg`-only guard at every overlay dispatch site (13).
- [x] 1.2 Add a `tea.PasteMsg, tea.PasteStartMsg, tea.PasteEndMsg` case that forwards
      to the multi-arguments dialog while it is open (it is routed separately from the
      overlay block) and falls through otherwise.
- [x] 1.3 Tests: `consumesInput` classification; paste while the argument dialog is
      open reaches the focused field, verified through the values the dialog reports on
      submit.

## 2. Recognition hint

- [x] 2.1 Add `chat.InvocationScanner` and the `NewEditorCmp(app, expand, scan)`
      parameter; add `chatPage.scanInvocations` over an extracted
      `chatPage.slashRegistry()` shared with `expandSubmission`.
- [x] 2.2 Cache recognition on the editor (`recognized`, `scannedDraft`) and refresh it
      from a single site: make `Update` a wrapper over the previous body that calls
      `refreshRecognition()`. Short-circuit when the draft is unchanged, in shell mode,
      or when no line begins with `/`.
- [x] 2.3 Add `styles.SkillIcon`; render `recognitionContent(budget)` with the success
      colour for invocations that expand and the info colour for action commands, an
      omitted-chip `+N` marker in the muted colour, and no chip for anything
      unresolved.
- [x] 2.4 Replace the attachment-count check in `syncTextareaHeight` with
      `hasAffordanceRow()`; render `affordanceRow()` (attachments then hint) and pad it
      to the container width with the theme background.
- [x] 2.5 Tests: recognition per draft shape (resolvable, bare name, unknown, indented,
      fenced, escaped, action, several in order); the hint clears when edited away;
      shell mode and a nil scanner recognize nothing; refresh happens through `Update`;
      the affordance row reserves exactly one line for one or both affordances; the
      no-overflow invariant holds with the hint present at several widths.

## 3. Checks

- [x] 3.1 `go test ./...`, `go vet ./...`, `make test`, and the
      `scripts/test/slash_expansion.sh` e2e.
- [x] 3.2 Render the affordance row at a narrow width and confirm the chips, the `+N`
      marker, and the background padding by inspecting the emitted ANSI.

> Not performed: an interactive keyboard pass in a real terminal (no TTY). The paste
> routing and the hint are covered by the tests above, and the row was inspected via
> its rendered ANSI, but neither has been exercised with a real paste keystroke.
