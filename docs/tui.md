# TUI

Running `opencode` with no `-p` starts the terminal UI. This page lists its keyboard shortcuts and the auto-approve mode. Slash commands and the command palette are covered in [custom commands](custom-commands.md).

## Keyboard shortcuts

### Global

| Shortcut | Action |
|----------|--------|
| `Ctrl+C` | Quit |
| `Ctrl+H`  | Toggle help |
| `Ctrl+L` | View logs |
| `Ctrl+A` | Switch session |
| `Ctrl+N` | New session |
| `Ctrl+P` | Prune session |
| `Ctrl+K` | Command dialog |
| `Ctrl+O` | Expand / collapse tool output ([details](#tool-output)) |
| `Ctrl+X` | Cancel generation |
| `Tab` | Switch primary agent |
| `Esc` | Close dialog / exit mode |

### Editor

| Shortcut | Action |
|----------|--------|
| `i` | Focus editor |
| `Ctrl+S` / `Enter` | Send message |
| `Ctrl+E` | Open external editor |
| `Esc` | Blur editor |
| `↑` / `↓` | Recall submitted text at the first / last input row |
| `Alt+↑` | Focus background tasks, subagents and flow activity |

### Shell mode

| Shortcut | Action |
|----------|--------|
| `!` | Enter shell mode (also recognised when a `!command` is pasted or submitted) |
| `!!command` | Run with the terminal handed over — for passwords, SSH, editors ([details](tools.md#shell)) |
| `Enter` | Run the command |
| `↑` / `↓` | Shell history |
| `Esc` / `Ctrl+C` | Cancel a running command, or leave shell mode |

### Vim mode

Enabled with `tui.vimMode`, or toggled from the command dialog. The editor starts
in `INSERT`; `Esc` switches to `NORMAL`.

| Shortcut | Action |
|----------|--------|
| `v` / `V` | Charwise / linewise VISUAL mode |
| motions | Extend the selection (`h j k l w b e 0 ^ $ G gg f t`, with counts) |
| `o` | Swap which end of the selection the cursor moves |
| `iw` / `i"` / `ab` … | Extend the selection over a text object |
| `d` `x` `c` `s` `y` | Delete / change / yank the selection |
| `~` `u` `U` | Toggle / lower / upper case over the selection |
| `>` `<` `J` `p` | Indent / unindent / join / replace with the register |
| `gv` | Restore the last selection |
| `Esc` / `Ctrl+C` | Back to `NORMAL` |

### Dialogs

Question dialogs occupy 80% of the terminal width, centered with roughly 10%
space on each side. Long questions and options wrap inside the border. When
several dialogs are pending, only the one receiving keyboard input is shown;
permission prompts take priority over questions, which remain pending.

| Shortcut | Action |
|----------|--------|
| `↑`/`k`, `↓`/`j` | Navigate items |
| `←`/`h`, `→`/`l` | Switch tabs/providers |
| `Enter` | Select |
| `a` / `A` / `d` | Allow / Allow for session / Deny (permissions) |

## Tool output

Tool calls are shown collapsed: the tool name and its main parameters (for `bash`, the description when the model gave one and the command on one line), then the first 3 lines of the result with a `… +N lines (ctrl+o to expand)` note under it. Diffs and markdown results (`task`, `web_search`) are cut after rendering, so the 3 lines are rendered rows. A subagent lists only its last 3 tool calls under the task, after a `… +N earlier tool uses` note. Errors show as a single truncated line.

`Ctrl+O` expands tool calls in place — the full bash command and task prompt, full results and errors, every subagent call — and pressing it again collapses them. Expansion is asynchronous and starts with messages in and near the viewport; scrolling expands newly visible calls while already expanded calls stay stable, so long sessions stay responsive. An expanded result still stops at 500 lines (`… +N lines not shown`). The toggle lasts for the TUI process and applies to `!` shell-mode output too; todo checklists are never collapsed.

Model selection, which used to be on `Ctrl+O`, is the `/model` command (also in the `Ctrl+K` command dialog). It has no shortcut: `Ctrl+M` sends the same byte as Enter in most terminals, so binding it would submit messages.

## Auto-approve

Auto-approve mode skips interactive permission dialogs for `ask`-resolved permissions during a session. `deny` rules and disabled tools are still enforced — auto-approve only promotes `ask` decisions to `allow`.

- **Toggle in TUI**: type `/auto-approve` to enable/disable for the current session
- **CLI flag**: `opencode --auto-approve` starts the TUI with auto-approve on the first session
- **Scope**: per-session only — new sessions start without auto-approve
- **Subagents**: child task sessions inherit auto-approve from the parent
- **Non-interactive mode**: already auto-approves all permissions, flag is ignored
- **Questions still ask**: auto-approve covers tool permissions, not decisions — the `question` tool keeps prompting you (TUI dialog, chat bridge, API). It answers itself with the first (recommended) option only where nobody could answer: a headless `opencode -p` run, a flow step, or a cron job firing on an unwatched session

## Service views

The transcript uses the full available width. `/mcp` opens live MCP controls,
`/lsp` opens language-server activation, and `/modified-files` shows files
changed in the current session and its child sessions. Use arrows or j/k to
select an entry, PgUp/PgDn to scroll its details, and Escape to return to chat.

## Running activities

`/tasks` lists background shell commands and monitors for the current session;
`/subagents` lists running and completed child agents. `/agents` continues to
show configured agent types. Use Up/Down or `j`/`k` to select an activity, Enter
to inspect its command or prompt and current output, and `x` to cancel it.
Escape returns from details to the list, then to chat. Output previews are bounded.

An activity strip above the editor shows running task and subagent counts. Press
Up with an empty editor, or `k` in vim NORMAL mode, to focus it. Left/Right chooses
a category, Enter opens it, and Down/Escape returns to editing. Draft text stays
intact. Activities remain inspectable while the main agent is working.

## Messages during flows

Messages sent while a flow runs are saved in its durable inbox. The TUI confirms
acceptance, shows the pending flow count above the editor, and lists the messages
with a `[flow]` label in **Ctrl+G**. The count and viewer refresh as inputs are
delivered, including after reopening the session. Storage errors restore the
unsaved text to the editor.

Steering reaches the agent at the next step or iteration boundary, before that
step starts; it does not interrupt a running model turn. If the flow is waiting
on a question, new directions can instead be delivered to that waiting step.
The queue viewer's **Ctrl+X** discards ordinary chat messages only; accepted flow
steering remains durable until delivery. Use the flow page to pause or terminate
the workflow.

## Input history

Up/Down recall the last 200 submitted texts for the current working directory
and profile, including slash commands in their original form. Edit a recalled
message and press Enter to resend. Down past the newest entry restores the draft
and staged attachments you had before browsing. Attachments from old submissions
are not replayed. Arrows inside multiline or wrapped text still move the cursor;
shell mode keeps its separate command history, and Vim visual selection is unchanged.

History survives restarts in private files under `~/.agents/tui/history/`
(or `~/.agents-<profile>/tui/history/`). Blank submissions, consecutive duplicates,
shell commands and individual inputs larger than 64 KiB are not retained.

The activity strip animates a spinner beside running tasks, subagents and flows.
Animation is local to the TUI; it does not increase database polling frequency,
and stops when no work is running.
