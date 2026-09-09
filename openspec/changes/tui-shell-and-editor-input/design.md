# Design: tui-shell-and-editor-input

## Context

See `proposal.md — Why` for motivation. The current state that shapes the approach:

- **`internal/llm/tools/shell/shell.go`** owns one `PersistentShell` per working
  directory, shared by the agent `bash` tool and the TUI's `!` mode. It is started with
  `exec.Command(shellPath, "-l")`, a pipe on stdin, and **no `SysProcAttr`**. Each command
  is written into that pipe as `eval '<cmd>' < /dev/null > out 2> err`, and completion is
  detected by polling for a status file.
- Because there is no `SysProcAttr`, the shell shares opencode's **session**, and
  therefore its **controlling terminal**. `/dev/null` on stdin does not close the hole:
  `sudo`, `ssh`, `gpg` and similar open `/dev/tty` precisely when stdin is not a terminal.
  The TUI is rendered **inline** (`cmd/root.go:268` — `tea.NewProgram` with no
  `WithAltScreen`), so those writes land in the same scrollback opencode is painting, and
  the reads race Bubble Tea's raw-mode reader for the user's keystrokes.
- `killChildren` (`shell.go:239`) sends SIGTERM to the direct children reported by
  `pgrep -P`; grandchildren survive, and there is no escalation to SIGKILL.
- `internal/tui/components/chat/editor.go:553` enters shell mode only on a literal `!`
  `tea.KeyPressMsg` with an empty draft. `tea.PasteMsg` is routed to the editor by
  `internal/tui/page/chat.go:228` and falls through to `m.textarea.Update`, so pasted text
  never sees that branch.
- `editor.go:519` returns early from `update` for **every** `tea.KeyPressMsg` while
  `shellExecuting` is set, so a running command cannot be cancelled and the editor is
  frozen until `tools.DefaultTimeout` (2 minutes) elapses.
- `openEditor` (`editor.go:222`) already hands the terminal to a child through
  `tea.ExecProcess`, which is the proven in-repo precedent for releasing and restoring the
  terminal around a foreign process.
- `internal/tui/vim` is a state machine over `CommandState` with `Transition`
  (`transitions.go:19`) dispatching per state, and operator execution behind
  `OperatorContext` (`operators.go:9`). Modes are `INSERT` and `NORMAL` only
  (`types.go:6`). Mode is surfaced as a plain string through `chat.VimModeChangedMsg` and
  compared against the literal `"INSERT"` in `page/chat.go:258`, `page/chat.go:522` and
  `core/status.go:214`.
- **`charm.land/bubbles/v2` textarea has no selection or highlight API.** It also has no
  public accessor for its wrapped-line layout: `wrap` and `memoizedWrap`
  (`textarea.go:1607`, `textarea.go:1698`) are unexported. The public surface that does
  expose layout is `LineInfo()` (`RowOffset`, `ColumnOffset`, `CharOffset`, `Height`,
  `StartColumn`, `Width`) and `ScrollYOffset()`.
- The editor already post-processes the textarea's rendered output in `textareaView()`
  (`editor.go:684`), using `styles.ForceReplaceBackgroundWithLipgloss` — an ANSI-aware
  rewrite of the rendered string. `github.com/charmbracelet/x/ansi` (already a direct
  dependency) provides `Cut`, `Strip`, and `StringWidth` for cell-accurate slicing.

## Goals / Non-Goals

**Goals:**

- Make TUI corruption by shell output **structurally impossible**, not merely unlikely —
  the guarantee must hold for commands nobody anticipated.
- Make the three entry paths into shell mode (typed, pasted, submitted) agree by
  construction rather than by three parallel implementations.
- Keep the captured-command path byte-for-byte compatible for commands that do not touch a
  terminal; this is the hot path for the agent's `bash` tool.
- Add VISUAL mode without forking the existing operator/motion implementations.
- Keep the rendered editor byte-identical when no visual mode is active.

**Non-Goals (design level):**

- No terminal emulator, no vt-sequence parsing, no capture of interactive output.
- No change to how captured output is truncated or written to the chat.
- No block-visual (`ctrl+v`) selection model.
- No new global key binding. `ctrl+o` is already model selection (`README.md:596`) and
  most other `ctrl+` keys are taken, so the interactive force is a text prefix rather than
  a chord.

## Decisions

### D1. Isolate with `Setsid`, not `Setpgid`

`Setpgid: true` gives the child its own **process group** but leaves it in opencode's
**session**, and the controlling terminal is a property of the session. `/dev/tty` would
still resolve. `Setsid: true` makes the child a session leader with **no controlling
terminal**, so `open("/dev/tty")` returns `ENXIO` for it and every descendant. That is the
property the fix needs.

`Setsid` also makes the shell a process-group leader with `pgid == pid`, so a
group-targeted signal is available if wanted later. It is not used for termination (see
D3) because the persistent shell itself is in that group.

Implementation follows the established build-tagged shape in `internal/task/procgroup_*.go`
and `internal/hooks/runner_*.go`: `detachFromTerminal(cmd)` in `session_posix.go`, a no-op
in `session_windows.go`.

*Alternative rejected:* keeping the process attached and setting `SUDO_ASKPASS` /
`SSH_ASKPASS` to a helper that pops a TUI password dialog. That would leave the
`/dev/tty` hole open for every tool that does not honour an askpass variable — the
guarantee would be a list, not an invariant — and it would put opencode in the business of
handling plaintext credentials.

*Alternative rejected:* running every command under a PTY the TUI owns. That is the
"embedded terminal emulator" the proposal rules out.

### D2. Environment disables prompting; it never answers one

Add `GIT_TERMINAL_PROMPT=0` alongside the existing `GIT_EDITOR=true`. Do **not** set
`SUDO_ASKPASS`, `SSH_ASKPASS`, or `GIT_ASKPASS` to anything: an askpass helper that
returns an empty string turns a clear "no terminal" failure into a confusing
"authentication failed", and one that returns anything real is a credential-handling
feature nobody asked for. The environment's job here is to make failure fast and legible.

### D3. Termination walks the descendant tree, then escalates

Replace the single `pgrep -P <shellpid>` sweep with a bounded breadth-first walk over
`pgrep -P` from the shell's direct children downwards, collecting the descendant set, then
SIGTERM every pid deepest-first, wait a short grace period, and SIGKILL whatever is still
alive. The persistent shell itself is never signalled — it must survive to run the next
command.

A group-targeted `kill(-pgid)` is not used precisely because, after D1, the shell shares
that group and would be killed with its children.

*Trade-off:* `pgrep` is an external process and the walk is not atomic — a process
spawned mid-walk can be missed. That is acceptable: the current behavior misses every
grandchild unconditionally, and this is a cancellation path, not a security boundary.

### D4. Interactive commands get the real terminal via `tea.ExecProcess`

The handoff reuses the mechanism `ctrl+e` already uses. `tea.ExecProcess` restores the
terminal to cooked mode, runs the child with `os.Stdin/Stdout/Stderr` inherited, and
re-enters when it exits.

The child is a **fresh** `exec.Command(shellPath, "-lc", wrapper)` — not the persistent
shell — because the persistent shell's stdin is a pipe and cannot be handed a terminal.
Critically, this fresh command is **not** detached (no `Setsid`): owning the terminal is
the entire point. The two spawn paths therefore have opposite terminal policies, and that
opposition is the design, not an inconsistency: the captured path may never touch the
terminal, and the interactive path exists only to.

Because the TUI is rendered inline, the command's output stays in the terminal scrollback
above the restored TUI, which is the behavior `ctrl+e` already trains users to expect.

*Alternative rejected:* `tea.Suspend` plus manual terminal juggling — more code for the
same result, and `ExecProcess` is already proven in this repo.

### D5. `cd` continuity is a wrapper plus a replay

The handoff wrapper is:

```
cd <shell-cwd> && ( <command> ); printf %s $? > <status>; pwd > <cwd>
```

After the child exits, the recorded `pwd` is written back to the persistent shell by
sending it a `cd <newcwd>` command. So `cd` behaves identically on both paths and the two
never drift.

The status file is used rather than `cmd.ProcessState` because the wrapper's own exit
status would otherwise mask the command's.

### D6. Classification promotes; it never demotes, and never retries

`shell.ClassifyInteractive(command) bool` inspects the command's leading program name
after skipping leading `VAR=value` assignments, and matches it against a built-in set
(`sudo`, `su`, `ssh`, `scp`, `sftp`, `gpg`, `passwd`, `vi`, `vim`, `nvim`, `nano`, `emacs`,
`less`, `more`, `man`, `top`, `htop`, `psql`, `mysql`, `sqlite3`, `redis-cli`, `ftp`,
`telnet`, `tmux`, `screen`, `crontab`, `visudo`) plus `shell.interactive` from config.
`docker`/`kubectl` are matched only with an explicit `-it`/`-ti` flag.

The classifier is deliberately a **first-token** check. It does not parse pipelines,
`&&` chains, or subshells: a command whose interactivity is buried inside a pipeline is a
command the user should force with `!!`. A parser that tried to be clever here would
produce false positives that silently drop output the user expected in the chat.

**Never auto-retry.** When a captured command fails with a "needs a tty" signature, the
system reports it and stops. Re-running is unsafe because the first attempt may already
have had effects (a `sudo` that timed out after a partial script, an `ssh` that ran half a
remote command). The user reruns with `!!` if they want to.

*Alternative rejected:* probing with a dry-run. There is no general dry-run.

### D7. Force prefix instead of a key chord

`!` at the start of the shell-mode draft forces the handoff — `!!cmd` as typed from the
normal editor. This needs no key binding (all convenient `ctrl+` chords are taken, per
Non-Goals), it is visible in the draft before the user commits, and it survives the shell
history that already exists.

The `shellInvocation` predicate (D8) explicitly refuses to treat a submitted `!!…` draft
in the *normal* editor as an invocation of the command `!…` — the double bang only means
"force" once shell mode is already active, so the meaning of a prefix never depends on how
the user got there.

### D8. One predicate, three call sites

`shellInvocation(text string) (command string, ok bool)` in the editor package is the only
place the `!` rule lives:

```
ok  ⇔  text starts with '!' (no leading whitespace)
       ∧ the remainder, trimmed, is non-empty
       ∧ text[1] ∉ { '[' , '=' }        // ![alt](…) markdown image, != operator
```

Call sites: the existing `!` keypress branch, a new `tea.PasteMsg` branch, and `send()`.
`send()` runs the check **before** `expandSubmission`, so a `!` draft is never expanded or
queued.

The `'['` and `'='` exclusions are the minimum needed to keep pasted markdown and prose
out of the shell. A longer denylist would trade a rare false positive for a confusing
rule; anything the predicate refuses is still one keystroke away from working.

### D9. Cancellation is a context per command

`executeShell` currently builds `context.Background()` inside the returned `tea.Cmd`.
Replace it with a `context.WithCancel` whose cancel func is stored on the editor model.
`shellExecuting`'s early return in `update` grows an exception for `esc` and `ctrl+c`,
which call the stored cancel. The existing ctx-watching goroutine in
`PersistentShell.execCommand` already calls `killChildren` on `ctx.Done()`, so
cancellation lands on the improved termination path from D3 with no further plumbing.

`page/chat.go` and `tui.go` must not steal those keys while a command runs: the editor
already reports `IsShellMode()`, and the running state is exposed the same way.

### D10. VISUAL is a mode, not a command state

`ModeVisual` and `ModeVisualLine` join `ModeInsert`/`ModeNormal` in `VimMode`. The
selection lives on the handler as an anchor rune-offset plus the mode; the cursor offset is
read from the textarea as it already is.

Visual key handling reuses the existing pieces rather than forking them:

- **Motions** go through `ResolveMotion` unchanged; only the cursor moves, and the
  selection is `[min(anchor,cursor), max(anchor,cursor)]` — extended to whole lines in
  VISUAL LINE.
- **Operators** resolve the selection to a `(from, to, linewise)` triple and call the
  existing `applyOperator`, which is the same function `ExecuteOperatorMotion` and
  `ExecuteOperatorTextObj` already call. No operator logic is duplicated.
- **Undo and dot-repeat** use the existing `pushUndo` / `RecordedChange` machinery; a new
  `RecordedChange` type `"visual"` records `(op, from, to, linewise)` relative to the
  cursor so `.` repeats the same-sized operation, which is vim's own behavior.

`gv` restores the last selection from a `LastVisual` field on `PersistentState`, alongside
the existing `LastFind` and `LastChange`.

*Alternative rejected:* modelling visual as a `CommandState` inside NORMAL. The mode
changes what every key means and must be visible in the status badge and in esc routing —
it is a peer of INSERT and NORMAL, not a pending-operator sub-state.

### D11. Display coordinates come from a probe textarea, not a re-implemented wrap

To highlight a selection we need, for a rune offset, its **display row** and **display
column** — which requires the textarea's soft-wrap layout. That layout is unexported.

Two rejected options first:

- *Re-implement `wrap`.* ~60 vendored lines that drift silently on any `bubbles` bump.
- *Probe the real textarea by moving its cursor.* `LineInfo()` is public and would answer
  exactly, but `CursorUp`/`CursorDown` call `repositionView`, which mutates the viewport —
  and `Model.viewport` is a **pointer**, so even a copied `Model` shares it. Probing would
  scroll the real input as a side effect.

The chosen approach is a **probe model**: one `textarea.Model` kept on the editor,
constructed by `textarea.New()` so it owns its own viewport, kept in sync with the real
one's width, height, and value. Its cursor is driven freely; its `LineInfo()` is
authoritative for the real textarea because both run the same unexported `wrap` at the
same width.

For an offset `o` at logical line `L`, column `c`:

```
absoluteRow = Σ(i<L) heightOf(i) + LineInfo(L,c).RowOffset
displayCol  = LineInfo(L,c).CharOffset + promptWidth
viewRow     = absoluteRow − realTextarea.ScrollYOffset()
```

`heightOf(i)` is `LineInfo().Height` with the probe's cursor on line `i`. The draft in a
chat editor is a handful of lines, so the O(lines) probing is negligible, and it runs in
`Update` only when the selection or the draft changes (per the `chat-editor-layout`
delta), never during render.

A conformance test pins the probe to the real widget: for a set of values and widths, the
probe's `LineInfo` must equal the real textarea's at the same cursor positions. If
`bubbles` changes its wrap, that test fails rather than the highlight drifting.

### D12. The highlight is an ANSI-aware cell-range restyle

`styles.RestyleRange(line string, from, to int, style lipgloss.Style) string` rebuilds a
rendered line as `Cut(0,from) + style.Render(Strip(Cut(from,to))) + Cut(to,∞)`, using
`x/ansi`. Stripping the middle discards the textarea's own styling inside the selection,
which is acceptable because the editor renders a single foreground colour there; the
selection background plus the theme text colour is the intended appearance.

`textareaView()` applies it to the rows the stored coordinates name. When no visual mode is
active the function is untouched, satisfying the "byte-identical" requirement.

*Trade-off:* the virtual cursor cell inside a selection is restyled along with the rest,
so the cursor is located by the selection's edge rather than by its own colour while
VISUAL is active. This matches how most terminal vim colour schemes look and avoids
reconstructing the cursor's escape sequence.

### D13. Config addition follows the repository's schema contract

`ShellConfig` gains `Interactive []string \`json:"interactive,omitempty"\``. Per
`CLAUDE.md`, that requires a matching declaration in `cmd/schema/main.go`, a regenerated
`opencode-schema.json` committed in the same change, and a `viper.Unmarshal` round-trip
test — the field is a list rather than a map, so key case-folding does not apply, but the
round-trip test is cheap insurance and the contract asks for it whenever config grows.

## Risks / Trade-offs

- **[A command that used to "work" by prompting on the tty now fails.]** → Intended, and
  the failure is legible: the classifier routes the common cases to the interactive path
  automatically, `!!` covers the rest, and the captured path names `!!` in its error when
  it recognises a tty failure. Called out as a behavioral break in the proposal.

- **[`Setsid` changes the shell's process topology for every existing user.]** → The shell
  is non-interactive with a pipe on stdin; it has no job control to lose. The captured
  path's observable contract (stdout, stderr, exit code, cwd) is asserted unchanged by
  existing `bash` tool tests, which run against the same `PersistentShell`.

- **[The classifier is a first-token heuristic and will miss cases.]** → It only ever
  promotes to interactive, so a miss degrades to today's behavior *minus* the corruption:
  a fast, explained failure with the fix named in the message. `shell.interactive` lets a
  user fix their own case without a release.

- **[`tea.ExecProcess` while the agent is streaming.]** → The handoff blanks the TUI for
  the command's duration; messages that arrive meanwhile are queued by Bubble Tea and
  render on restore. The same is already true of `ctrl+e`. Not gated on agent idleness,
  because a shell command and an agent run are independent — but the interactive record
  written to the chat goes through the same `ensureSession` path as today's shell result,
  so ordering with agent messages is unchanged.

- **[The probe textarea doubles the wrap work on every selection change.]** → Bounded by
  draft size (a chat message), recomputed only in `Update` and only while a visual mode is
  active. No cost at all in INSERT/NORMAL.

- **[`bubbles` could change its wrap algorithm and break the highlight.]** → The
  conformance test in D11 turns that from a silent visual bug into a red test on the
  dependency bump.

- **[Restyling strips inner styling in the selection.]** → Accepted; see D12. If the
  editor ever renders multi-coloured draft text, the helper would need to merge attributes
  rather than strip them, and the trade-off should be revisited then.

## Migration Plan

No data migration, no config migration. `shell.interactive` is optional and additive;
absent it, the built-in list applies. Rollback is a revert — nothing persists state that a
previous version could not read.

The one user-visible break (captured interactive commands now failing fast) needs a
release note, phrased as the proposal phrases it: the previous behavior was a hang plus a
corrupted screen.
