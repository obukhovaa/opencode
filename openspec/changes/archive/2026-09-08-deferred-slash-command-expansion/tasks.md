## 1. Argument parsing and binding (`internal/skill`, `internal/slashcmd`)

- [x] 1.1 Make `skill.splitArgs` quote-aware: single and double quotes group a value,
      backslash escapes inside double quotes, unbalanced input falls back to
      `strings.Fields`. Table-driven tests covering bare words, `"two words"`,
      `'two words'`, embedded quotes, empty string, and malformed input.
- [x] 1.2 Add `skill.QuoteArg(string) string` — the inverse of 1.1 (quote when the
      value holds whitespace, a quote, or is empty) — plus a round-trip test asserting
      `splitArgs(strings.Join(QuoteArg(v)…)) == values` over the 1.1 table.
- [x] 1.3 Add `slashcmd.NamedPlaceholders(content string) []string` returning `$FOO`
      names in first-appearance order with dedup (extracted from
      `dialog.ParameterizedCommandHandler`, which then calls it), and test that the
      dialog's field order equals the binding order.
- [x] 1.4 Add `CommandInfo.IsAction()` (`TUIOnly && Content == ""`) with a test
      asserting the classification of every entry of `BuiltinCommands()` and of a
      custom command with an empty body.

## 2. The expander (`internal/slashcmd/expand.go`)

- [x] 2.1 Add `Registry`, `Kind`, `Invocation`, `ExpandOptions`, `Expansion` and
      `Scan(text, reg) []Invocation`: column-0 anchor, ``` fence tracking, `\/`
      escape, first-token resolution against commands (id and base name) and
      `skill:<name>`, args = rest of line.
- [x] 2.2 `Scan` tests: single leading invocation; two invocations with prose between;
      indented `/x` ignored; mid-line mention ignored; lines inside a fence ignored;
      unclosed fence swallows the rest; `\/x` unescaped and not expanded; unknown name
      unresolved; `/skill:<name>` for a non-user-invocable skill flagged.
- [x] 2.3 Implement `Expand`: per-invocation binding (named → `$ARGUMENTS[N]` →
      `$ARGUMENTS` → `$N` → append `ARGUMENTS:`), `${SKILL_DIR}` / `${SESSION_ID}`,
      `<skill_content>` wrapping for skills, optional `ShellExpand`, in-place splice
      preserving surrounding text verbatim.
- [x] 2.4 Implement the error paths: non-user-invocable skill, `TUIOnly` command with
      `Interactive: false`, more than `MaxInvocationsPerMessage` (10) invocations, and
      an action command combined with any other non-empty content. Each returns a
      distinct sentinel error and an empty `Expansion`.
- [x] 2.5 Implement the action path: a message that is exactly one action invocation
      returns `Expansion{Action, ActionArgs}` with an empty `Prompt`.
- [x] 2.6 `Expand` tests for 2.3–2.5, including a two-skill-plus-prose message asserting
      exact output ordering, a skill whose own body contains a line starting with
      `/review` (asserting it is not re-expanded), and `ShellExpand` being applied only
      inside expanded blocks.
- [x] 2.7 Delete `HasOnlyArgumentsPlaceholder` and `SubstituteArgs` once their callers
      are gone (after task 4), and drop their tests.

## 3. Staging (TUI dialogs)

- [x] 3.1 Add `dialog.StageInvocationMsg{Text string}`.
- [x] 3.2 Split `CloseMultiArgumentsDialogMsg` handling in `tui.go`: `loop` / `rename`
      keep emitting `CommandRunCustomMsg`; every other command id emits
      `StageInvocationMsg` with `"/" + id + " " + quoted args` built via
      `skill.QuoteArg`. Skill ids keep their `skill:` prefix.
- [x] 3.3 Rework `chatPage`'s `CompletionSelectedMsg` branch for the command provider:
      prompt invocations (skills and commands with content) either open the argument
      dialog or emit `StageInvocationMsg{"/" + name + " "}`; action commands keep
      running their handler immediately. Remove the send-on-select paths.
- [x] 3.4 Handle `StageInvocationMsg` in `editorCmp.Update`: insert at the cursor,
      prefixing a newline when the cursor is not at column 0, and leave existing text
      and attachments untouched.
- [x] 3.5 Remove the `CommandRunCustomMsg` prompt/skill branch from `chatPage.Update`,
      leaving only the `loop` / `rename` consumers in `tui.go`; drop the busy-reject
      guard that branch carried (staging is always allowed).
- [x] 3.6 Tests: argument-dialog submit produces the expected staged text for a
      positional skill, a named-placeholder command, and a value containing spaces;
      cancelling stages nothing; selecting an action command emits no
      `StageInvocationMsg`.

## 4. Single-point expansion on submit

- [x] 4.1 Add `chat.SubmissionExpander` and the `NewEditorCmp(app, expand)` parameter;
      restructure `NewChatPage` to build `p`, then the editor container from
      `p.expandSubmission`, then `p.layout`.
- [x] 4.2 Implement `chatPage.expandSubmission`: build the `Registry` from `p.commands`
      and `skill.All()`, pass `p.session.ID`, `Interactive: true`, and a `ShellExpand`
      closure over `format.ExpandShellMarkup` + `config.WorkingDirectory()`.
- [x] 4.3 Call the expander at the top of `editorCmp.send()`, before the queue/dispatch
      fork: on error warn and keep the textarea and attachments; on `Action` reset the
      textarea and return the action's command; otherwise route `Expansion.Prompt`
      through the existing fork unchanged.
- [x] 4.4 Route `openEditor()` through `send()` via a new internal
      `editorContentMsg{Text}` instead of emitting `chat.SendMsg` directly.
- [x] 4.5 Delete `chatPage.resolveInlineSlash` and `joinPositionalArgs`; the
      `chat.SendMsg` handler now calls `p.sendMessage` directly, and `chat.SendMsg`
      is documented as carrying already-expanded content.
- [x] 4.6 Tests: a message typed while the session is busy is enqueued **expanded**
      (assert the queued `QueuedMessage.Text` contains `<skill_content>` and not
      `/skill:`); a failed expansion leaves the textarea content intact and enqueues
      nothing; a bare action command clears the textarea and runs its handler; a mixed
      action command is rejected with the text preserved.

## 5. Transcript rendering

- [x] 5.1 Add `collapseSkillBlocks(text string) string` in
      `internal/tui/components/chat/message.go` rendering each
      `<skill_content name="X">…</skill_content>` region as `⚡ skill:X · N lines`,
      and call it from `renderUserMessage` before `renderMessage`.
- [x] 5.2 Tests: one block; two blocks with prose between (order and prose preserved);
      an unterminated `<skill_content` left verbatim; a message with no block byte-identical
      to its input; the stored `message.Message` content unchanged by rendering.

## 6. Non-interactive parity

- [x] 6.1 Rewrite `cmd/flow.go resolveSlashPrompt` on top of `slashcmd.Expand` with
      `Interactive: false`, dropping its local `namedArgPattern` blanking step; keep
      returning the input unchanged when nothing resolves and keep surfacing
      `ErrTUIOnly` as a run error.
- [x] 6.2 Add an e2e script under `scripts/test/` driving a non-interactive prompt that
      carries two invocations plus prose, asserting both blocks appear in the recorded
      prompt and that prose position is preserved.

## 7. Docs and final checks

- [x] 7.1 Update `docs/skills.md` (and `docs/` slash-command coverage if present):
      selecting a command stages it for editing, multiple invocations per message, the
      column-0 / fence / `\/` rules, the 10-invocation cap, action commands staying
      immediate, and shell markup resolving at submit time.
- [x] 7.2 Run `go test ./...`, including the un-archived queue change's tests
      (`internal/app`, `internal/tui/...`), then `make test`.
- [x] 7.3 Cover each smoke behavior with an automated test instead of a manual pass:
      two staged invocations plus prose submit as one expanded message
      (`TestEditor_StageInvocationAccumulates`); a submission while the agent is busy
      is enqueued expanded (`TestEditor_send_BusyEnqueuesExpandedPrompt`); a bare
      action command still acts (`TestEditor_send_BareActionCommandRunsAction`); ctrl+e
      content is expanded (`TestEditor_ExternalEditorContentIsExpanded`); a fenced
      `/review` is not expanded (`TestScan`, `scripts/test/slash_expansion.sh`).

> Not performed: an interactive keyboard pass in a real terminal — it needs a TTY the
> agent does not have. The behaviors above are covered by the tests named, but the
> visual result of staging (cursor position after insert, the collapsed transcript line
> in a live theme) has not been eyeballed.
