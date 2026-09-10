## 1. Shell process isolation (`internal/llm/tools/shell/`)

- [x] 1.1 Add `session_posix.go` (`//go:build !windows`) with `detachFromTerminal(cmd *exec.Cmd)`
  setting `SysProcAttr.Setsid = true`, and `session_windows.go` with a no-op, following the
  shape of `internal/task/procgroup_{posix,windows}.go`. Document in the POSIX file *why*
  `Setsid` and not `Setpgid` (controlling terminal is a session property — design D1).
- [x] 1.2 Call `detachFromTerminal(cmd)` in `newPersistentShell` before `cmd.Start()`.
- [x] 1.3 Add `GIT_TERMINAL_PROMPT=0` to the shell's env next to the existing
  `GIT_EDITOR=true`. Do NOT set any `*_ASKPASS` variable (design D2) — add a comment saying
  so, because "helpfully" adding one later would reopen the hole.
- [x] 1.4 Replace `killChildren` with a descendant-tree terminate: breadth-first `pgrep -P`
  walk from the shell pid collecting descendants (bounded depth, e.g. 32, to guard against a
  pgrep loop), SIGTERM deepest-first, wait a grace period (e.g. 2s), SIGKILL survivors. The
  persistent shell pid itself MUST NOT be signalled.
- [x] 1.5 Add `(*PersistentShell) Cwd() string` returning the tracked cwd under the mutex.
- [x] 1.6 Test: a command that opens `/dev/tty` fails rather than reaching the terminal —
  run `sh -c 'exec 3<>/dev/tty'` (or `tty`) through the shell and assert non-zero exit /
  error on stderr. Skip on Windows.
- [x] 1.7 Test: the captured contract is unchanged — stdout, stderr, exit code and cwd
  tracking for a plain command, and a `cd` that persists to the next command.
- [x] 1.8 Test: cancelling a command whose child spawns its own child leaves no process in
  the tree alive after the grace period. Skip on Windows.
- [x] 1.9 Test: an env var not named by the prompting policy survives into commands.

## 2. Interactive classification (`internal/llm/tools/shell/`)

- [x] 2.1 Add `interactive.go` with `ClassifyInteractive(command string, extra []string) bool`:
  skip leading `VAR=value` assignments, take the first token, basename it, match against the
  built-in set from design D6 plus `extra`; match `docker`/`kubectl` only when an `-it`/`-ti`
  (or `-i -t`) flag is present.
- [x] 2.2 Add `NeedsTerminal(stderr string) bool` recognising the "no terminal" signatures:
  sudo's `no tty present`, `a terminal is required to read the password`, ssh's
  `Host key verification failed` / `no tty`, git's `terminal prompts disabled`,
  `not a terminal`, `Inappropriate ioctl for device`.
- [x] 2.3 Table-driven tests for both, including the negatives that matter:
  `echo sudo`, `git commit -m "sudo this"`, `docker ps` (not interactive), `docker exec -it x sh`
  (interactive), `FOO=1 sudo -v` (interactive).

## 3. Shell config (`internal/config/`, `cmd/schema/`)

- [x] 3.1 Add `Interactive []string \`json:"interactive,omitempty"\`` to `ShellConfig`.
- [x] 3.2 Declare the field in `cmd/schema/main.go` (array of string, description, no enum).
- [x] 3.3 Regenerate `opencode-schema.json` via `go run cmd/schema/main.go > opencode-schema.json`
  and commit it in this change (repository schema contract in `CLAUDE.md`).
- [x] 3.4 Add a `viper.Unmarshal` round-trip test under `internal/config/` asserting
  `shell.interactive` survives loading.

## 4. Shell-invocation predicate and entry paths (`internal/tui/components/chat/editor.go`)

- [x] 4.1 Add `shellInvocation(text string) (command string, ok bool)` implementing design
  D8 exactly (leading `!` at index 0, non-empty trimmed remainder, `text[1]` not `[` or `=`).
- [x] 4.2 Rewrite the existing `!`-keypress branch to call `shellInvocation` so there is one
  rule, not two.
- [x] 4.3 Add a `tea.PasteMsg` case to `update`: when `mode == modeNormal`, the draft is
  empty, vim (if enabled) is in INSERT, and `shellInvocation` matches, enter shell mode and
  set the draft to the command; otherwise fall through to the textarea unchanged.
- [x] 4.4 In `send()`, before `expandSubmission`, check `shellInvocation` on the draft and
  route to the shell execution path instead of sending.
- [x] 4.5 Tests: `!ls -la` typed, pasted, and submitted all reach the same command; `![x](y)`,
  ` !ls`, `!`, `!=` do not; a paste into a non-empty draft inserts text; a paste of prose
  inserts text.

## 5. Interactive handoff (`internal/tui/components/chat/`, `internal/tui/page/chat.go`)

- [x] 5.1 In shell mode, strip a leading `!` from the draft and mark the run forced
  (design D7). Decide interactive = forced ∨ `ClassifyInteractive(cmd, cfg.Shell.Interactive)`.
- [x] 5.2 Add `executeShellInteractive` returning a `tea.ExecProcess` command: fresh
  `exec.Command(shell.GetShellPath(), "-lc", wrapper)` with `Stdin/Stdout/Stderr = os.Std*`,
  `Dir = sh.Cwd()`, and **no** `detachFromTerminal` (design D4 — add the comment, this is the
  one spawn that must own the terminal). Wrapper per design D5, writing exit status and `pwd`
  to temp files.
- [x] 5.3 On completion: read the status and cwd files, send `cd <newcwd>` into the persistent
  shell to resync, remove the temp files, and emit `ShellResultMsg` with a new
  `Interactive bool` field set.
- [x] 5.4 Extend `ShellResultMsg` in `chat.go` with `Interactive bool` and `Hint string`.
- [x] 5.5 In `handleShellResult` (`page/chat.go:368`): for an interactive result, write the
  command echo plus a record naming the exit code and stating the output went to the terminal;
  for a captured result whose stderr satisfies `NeedsTerminal`, append the `!!` hint.
- [x] 5.6 Tests: classification routes `sudo -v` to the interactive path and `ls` to the
  captured path (assert on which command the editor returns, not by running `sudo`); the force
  prefix is stripped from the recorded command; `NeedsTerminal` stderr produces the hint and
  unrelated stderr does not.

## 6. Cancellation while a command runs (`internal/tui/components/chat/editor.go`)

- [x] 6.1 Store a `shellCancel context.CancelFunc` on the editor; build the command's context
  with `context.WithCancel` in `executeShell` instead of `context.Background()`.
- [x] 6.2 In the `shellExecuting` early-return branch, honour `esc` and `ctrl+c`: call
  `shellCancel`, clear the executing state, stay in shell mode with an empty draft.
- [x] 6.3 Ensure `page/chat.go` and `tui.go` do not consume those keys first while a shell
  command is running (the shell-mode branches already exist; extend the running-state check
  alongside `IsShellMode`).
- [x] 6.4 Emit a cancelled `ShellResultMsg` so the chat records the cancellation.
- [x] 6.5 Tests: `ctrl+c` and `esc` while executing invoke the cancel func and leave the
  editor in shell mode; no quit dialog is raised; a non-cancel key is still ignored.

## 7. Vim VISUAL mode state (`internal/tui/vim/`)

- [x] 7.1 `types.go`: add `ModeVisual VimMode = "VISUAL"` and `ModeVisualLine VimMode = "V-LINE"`;
  add `Anchor int` to `VimState` and `LastVisual *VisualRecord` (mode + anchor + cursor) to
  `PersistentState`.
- [x] 7.2 Add `visual.go`: `SelectionRange(text string, anchor, cursor int, linewise bool) (from, to int)`
  — inclusive charwise, whole-lines linewise.
- [x] 7.3 Add `ExecuteVisualOperator(op Operator, from, to int, linewise bool, ctx *OperatorContext)`
  delegating to the existing `applyOperator` (design D10 — do not duplicate operator logic).
- [x] 7.4 `handler.go`: `v`/`V` from NORMAL enter the visual modes anchoring at the cursor;
  `v`/`V` within them toggle/exit per the spec; `esc` returns to NORMAL and stores `LastVisual`.
- [x] 7.5 Visual-mode motions: route through `ResolveMotion` with counts, moving only the
  cursor; `o` swaps anchor and cursor.
- [x] 7.6 Visual-mode operators `d x c s y ~ u U > < J p`, each applying to the selection,
  setting the register with the right linewise flag, and landing in NORMAL (INSERT for `c`/`s`).
- [x] 7.7 `gv` in NORMAL restores `LastVisual`; a no-op when unset.
- [x] 7.8 Undo: push one undo entry per visual operator so `u` reverts it in a single step.
- [x] 7.9 Dot-repeat: add `RecordedChange` type `"visual"` carrying `(op, span, linewise)` and
  replay it relative to the cursor.
- [x] 7.10 `ConsumesCtrlC` returns true in the visual modes.
- [x] 7.11 Expose `Selection() (from, to int, linewise, active bool)` for the renderer.
- [x] 7.12 Tests in the existing table-driven style: mode transitions, selection extension
  (charwise and linewise, with counts), `o`, every operator, `gv`, undo-as-one-step, dot-repeat.

## 8. Selection rendering (`internal/tui/styles/`, `internal/tui/components/chat/editor.go`)

- [x] 8.1 Add `styles.RestyleRange(line string, from, to int, style lipgloss.Style) string`
  using `x/ansi` `Cut`/`Strip` (design D12). It MUST preserve the line's visible cell width.
- [x] 8.2 Tests for `RestyleRange`: width preserved; ranges at line start/end/whole-line;
  wide (2-column) characters not split; a line already carrying ANSI styling.
- [x] 8.3 Add the probe textarea to the editor (`textarea.New()`, own viewport) plus
  `selectionRows(from, to int) []selectionSpan` computing `(viewRow, colFrom, colTo)` per
  design D11, kept in sync with the real textarea's width/height/value.
- [x] 8.4 Compute and store the spans in `Update` whenever the selection or draft changes —
  never in `View` (the `chat-editor-layout` delta requires this).
- [x] 8.5 Apply the spans in `textareaView()`; return the view untouched when no visual mode
  is active.
- [x] 8.6 Conformance test: for a set of values and widths, the probe's `LineInfo` matches the
  real textarea's at the same cursor positions — this is what pins the highlight to `bubbles`'
  wrap (design D11).
- [x] 8.7 Test: a selection spanning a soft wrap highlights every covered display row across
  the correct columns and nothing outside the selection.
- [x] 8.8 Test: with no visual mode active, `View()` output is byte-identical to a run with
  the selection code compiled in but inactive (guards the "no change when inactive" clause).
- [x] 8.9 Extend the existing no-overflow tests (`TestEditorCmpNoOverflow`) to cover the
  visual modes at every width, and `TestEditorViewCellsCarryBackground` to cover a highlighted
  view.

## 9. Mode plumbing outside the editor

- [x] 9.1 `internal/tui/components/core/status.go`: render `VISUAL` / `V-LINE` badges with a
  distinct background from `INSERT`/`NORMAL`.
- [x] 9.2 `internal/tui/page/chat.go`: the esc branch (`:258`) and `ConsumesCtrlC` (`:522`)
  must treat the visual modes like INSERT — editor-consumed, never agent-cancel or quit.
  Replace the bare `== "INSERT"` string comparisons with a helper so a future mode cannot
  slip through the same gap.
- [x] 9.3 The `!`-to-shell-mode guard must exclude the visual modes as well as NORMAL.
- [x] 9.4 Tests: esc in VISUAL with a busy agent does not cancel the agent; ctrl+c in VISUAL
  raises no quit dialog; `!` in VISUAL does not enter shell mode.

## 10. Docs and final checks

- [x] 10.1 `README.md`: document `shell.interactive` in the Shell config section; add the
  `!` / `!!` shell-mode rows and the vim VISUAL rows to the Editor shortcut table.
- [x] 10.2 Add a release note for the behavioral break: commands that silently prompted on the
  terminal now fail fast, and how to run them (`!!`, or `shell.interactive`).
- [x] 10.3 Add an e2e script under `scripts/test/` exercising the isolation invariant end to
  end (a `/dev/tty` write from a shell command must not reach the terminal), per the
  `make test-e2e` convention in `CLAUDE.md`.
- [x] 10.4 Run `make test` and fix everything it reports (tests plus formatters).
- [x] 10.5 Run `./scripts/check_hidden_chars.sh`.

## 11. Session-ending commands (`internal/llm/tools/shell/`)

- [x] 11.1 Record the shell process's exit status from `cmd.Wait` before marking it
  dead, so a command that ended the session can report the status it asked for.
- [x] 11.2 Distinguish "the session ended" from "interrupted" in `execCommand`: report
  the recorded status and an explanation, never the bogus "timed out or was
  interrupted" a completed command used to get.
- [x] 11.3 Start a replacement shell in the previous one's working directory when that
  directory still exists; fall back to the configured working directory otherwise.
- [x] 11.4 Give `cwd` its own `RWMutex` so `Cwd()` never blocks behind a running
  command — the interactive handoff reads it from the TUI event loop.
- [x] 11.5 Tests: an `exit 3` command reports code 3, not interrupted, output preserved,
  with an explanation; a replacement shell starts in the previous directory; `Cwd()`
  returns promptly while a command runs.
