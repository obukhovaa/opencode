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
| `Ctrl+O` | Model selection |
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

| Shortcut | Action |
|----------|--------|
| `↑`/`k`, `↓`/`j` | Navigate items |
| `←`/`h`, `→`/`l` | Switch tabs/providers |
| `Enter` | Select |
| `a` / `A` / `d` | Allow / Allow for session / Deny (permissions) |

## Auto-approve

Auto-approve mode skips interactive permission dialogs for `ask`-resolved permissions during a session. `deny` rules and disabled tools are still enforced — auto-approve only promotes `ask` decisions to `allow`.

- **Toggle in TUI**: type `/auto-approve` to enable/disable for the current session
- **CLI flag**: `opencode --auto-approve` starts the TUI with auto-approve on the first session
- **Scope**: per-session only — new sessions start without auto-approve
- **Subagents**: child task sessions inherit auto-approve from the parent
- **Non-interactive mode**: already auto-approves all permissions, flag is ignored
- **Questions still ask**: auto-approve covers tool permissions, not decisions — the `question` tool keeps prompting you (TUI dialog, chat bridge, API). It answers itself with the first (recommended) option only where nobody could answer: a headless `opencode -p` run, a flow step, or a cron job firing on an unwatched session
