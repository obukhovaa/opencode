## Why

Three defects make the TUI's input line unreliable, and the first one visibly corrupts
the screen:

1. **An interactive command run from `!` shell mode destroys the TUI.** The persistent
   shell (`internal/llm/tools/shell/shell.go`) is spawned with no `SysProcAttr`, so it
   stays in opencode's session and inherits its **controlling terminal**. Redirecting the
   eval'd command's stdin from `/dev/null` does not help: `sudo`, `ssh`, `gpg`, and every
   other credential-prompting tool deliberately falls back to opening `/dev/tty` when
   stdin is not a terminal. That write lands directly on opencode's inline-rendered TUI,
   and the read races Bubble Tea for keystrokes — so the password is never entered, the
   prompt repeats ("Password: / Sorry, try again."), and the frame is left interleaved
   with foreign output. The same leak exists on the agent's `bash` tool, where nobody is
   watching to type the password at all: the run simply burns the 2-minute timeout.
2. **A pasted `!command` is not recognised as a shell command.** Shell mode is entered
   only by a literal `!` keypress on an empty input (`editor.go:553`). Bracketed paste
   arrives as `tea.PasteMsg` and falls straight through to the textarea, so the same text
   that works when typed becomes an ordinary chat message when pasted — the user has to
   retype the `!` by hand.
3. **Vim mode has no VISUAL mode.** `internal/tui/vim` implements only INSERT and NORMAL
   (`types.go:6-9`). `v`, `V`, `o`, `gv` and visual-mode operators are unhandled input,
   so selecting a range before acting on it is impossible.

## What Changes

### Shell process isolation (fixes 1, benefits the `bash` tool too)

- The persistent shell is spawned in its **own session** (`Setsid` on POSIX), so it has no
  controlling terminal. `/dev/tty` can no longer resolve to opencode's terminal from any
  descendant — the TUI becomes structurally incorruptible by shell output, rather than
  incidentally safe.
- Non-interactive prompting is disabled at the environment level (`GIT_TERMINAL_PROMPT=0`
  and friends) so credential-prompting tools fail fast with an actionable message instead
  of blocking until the timeout.
- Command termination signals the whole descendant tree with SIGTERM and escalates to
  SIGKILL after a grace period, instead of SIGTERM to direct children only. Today a
  cancelled or timed-out command leaves grandchildren running.
- **BREAKING (behavioral)**: a `bash`-tool or `!`-mode command that previously hung on a
  hidden `/dev/tty` prompt now fails immediately with the tool's own error (e.g. `sudo: no
  tty present and no askpass program specified`). This is the intended outcome — the
  previous behavior was a hang plus a corrupted screen — but scripts that relied on a
  human noticing the stray prompt will now see a non-zero exit.

### TUI shell mode (fixes 1 and 2 from the user's side)

- **Interactive handoff.** A `!` command classified as interactive (built-in list plus a
  configurable `shell.interactive` list) is not captured: opencode releases the terminal
  through `tea.ExecProcess` — the same mechanism `ctrl+e` already uses for `$EDITOR` — and
  the command runs against the real tty with a real prompt. The TUI restores afterwards
  and the chat records the command and its exit code.
- **Explicit force.** A leading `!` inside shell mode (`!!cmd` as typed from the normal
  editor) forces the handoff for a command the classifier does not know. When a captured
  command fails with a recognisable "needs a terminal" signature, the output message says
  so and names the `!!` prefix.
- **`cd` continuity across the handoff.** The handoff runs in the persistent shell's
  current directory and the directory it leaves behind is applied back to the persistent
  shell, so `!!cd /x` behaves like `!cd /x`.
- **Paste and submit entry paths.** One shared predicate decides whether a draft is a
  shell invocation, and it is consulted on the `!` keypress, on `tea.PasteMsg`, and once
  more at submit time. Pasting `!ls -la` and pressing enter on a draft that begins with
  `!` both do what typing `!ls -la` does today. A guard keeps markdown image syntax
  (`![alt](url)`) and operators (`!=`, `!!` at submit) out of the predicate.
- **Cancellation.** `ctrl+c` and `esc` during `$ running...` cancel the in-flight command.
  Today `shellExecuting` swallows every key (`editor.go:519`), so a slow command locks the
  editor until the 2-minute timeout expires.

### Vim VISUAL mode (fixes 3)

- `v` (charwise) and `V` (linewise) enter VISUAL from NORMAL, anchoring a selection at the
  cursor. Motions extend it; `o` swaps the ends; `esc` returns to NORMAL; `gv` restores
  the last selection.
- `d`/`c`/`y`/`x`/`s`/`~`/`u`/`U`/`>`/`<`/`J`/`p` operate on the selection and return to
  NORMAL, reusing the existing operator implementations rather than duplicating them.
- The selection is rendered by restyling the selected cell range in the textarea's own
  rendered output — the textarea has no selection API — so the highlight follows the
  library's soft-wrap and scrolling exactly.
- `VISUAL` / `V-LINE` appear in the status badge, and the mode participates in the
  existing esc / ctrl+c routing (`chat.go:258`, `chat.go:522`) so esc in VISUAL returns to
  NORMAL rather than cancelling the agent.

### Non-goals

- **No embedded terminal emulator.** Interactive commands get the real terminal via
  `tea.ExecProcess`; opencode does not parse vt sequences, and interactive output is not
  captured into the chat log. The chat records the command and its exit status, and says
  the output went to the terminal.
- **No block-visual mode (`ctrl+v`).** Charwise and linewise only. Block visual requires a
  rectangular selection model that neither the operator layer nor the highlight renderer
  has today.
- **No shell-history persistence** across restarts, and no streaming of captured output
  while a command runs. Both are separate changes.

## Capabilities

### New Capabilities

- `shell-process-isolation`: how the shared persistent shell is spawned and torn down —
  session/controlling-terminal isolation, prompt-disabling environment, descendant
  termination, and the fail-fast contract that replaces silent `/dev/tty` hangs. Binds
  both the agent `bash` tool and TUI shell mode.
- `tui-shell-mode`: the chat editor's `!` mode — the shell-invocation predicate shared by
  the keypress, paste, and submit entry paths; captured vs interactive execution and the
  classifier that chooses between them; the `!!` force prefix; `cd` continuity across a
  handoff; cancellation while a command runs; and what is written to the chat.
- `tui-vim-mode`: the editor's vim mode model — the mode set including VISUAL and VISUAL
  LINE, selection anchoring and extension, the operators VISUAL supports, selection
  rendering, and how each mode participates in esc / ctrl+c routing and the status badge.

### Modified Capabilities

- `chat-editor-layout`: the no-overflow and no-mutation-in-View invariants must bind the
  new selection-highlight overlay — it post-processes the rendered textarea view, so it
  must be proven not to change the view's rendered width, and the display coordinates it
  needs must be computed in `Update` rather than derived during render.

## Impact

**`github.com/opencode-ai/opencode`**

- `internal/llm/tools/shell/shell.go`: `newPersistentShell` gains session isolation and
  the prompt-disabling environment; `killChildren` becomes a recursive descendant
  terminate with SIGTERM→SIGKILL escalation; a `Cwd()` accessor is added for the handoff.
- `internal/llm/tools/shell/session_posix.go` / `session_windows.go` (new): build-tagged
  `detachFromTerminal(cmd)`, following the existing `internal/task/procgroup_*.go` and
  `internal/hooks/runner_*.go` pattern.
- `internal/llm/tools/shell/interactive.go` (new): the classifier that decides whether a
  command needs a terminal, and the recogniser for "needs a tty" failure signatures.
- `internal/tui/components/chat/editor.go`: the shared shell-invocation predicate; a
  `tea.PasteMsg` branch; a submit-time shell check in `send()`; the interactive-handoff
  command; cancellation during `shellExecuting`; VISUAL-mode key routing and the selection
  overlay in `textareaView()`.
- `internal/tui/components/chat/chat.go`: `ShellResultMsg` gains the fields the interactive
  path needs (`Interactive`, `Hint`); a cancel message for a running command.
- `internal/tui/page/chat.go`: `handleShellResult` renders the interactive-run record and
  the needs-a-terminal hint; esc / ctrl+c routing learns the VISUAL modes
  (`chat.go:258`, `chat.go:522`).
- `internal/tui/components/core/status.go:212`: badge colour for the VISUAL modes.
- `internal/tui/vim/`: `types.go` gains `ModeVisual`/`ModeVisualLine` and the visual
  command states; `visual.go` (new) holds selection anchoring, extension, and operator
  application; `handler.go` routes visual keys and exposes the selection range.
- `internal/tui/styles/` : an ANSI-aware "restyle this cell range" helper alongside the
  existing `ForceReplaceBackgroundWithLipgloss`.
- `internal/config/config.go`: `ShellConfig` gains `interactive []string`; requires a
  matching `cmd/schema/main.go` update and a regenerated `opencode-schema.json` per the
  repository's schema contract.
- `README.md`: the `shell` config block and the Editor keyboard-shortcut table.
