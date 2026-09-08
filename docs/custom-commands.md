# Custom Commands

## Overview

Custom commands are predefined prompts stored as Markdown files that you can quickly execute via `Ctrl+K` (command palette) or by typing `/` in the chat editor (slash commands with autocomplete).

## Locations

Commands are discovered from the following directories, in order:

**User commands** (prefix `user:`):

- `$XDG_CONFIG_HOME/opencode/commands/` (defaults to `~/.config/opencode/commands/`)
- `$HOME/.opencode/commands/`
- `$HOME/.agents/commands/`
- `$HOME/.claude/commands/`

**Project commands** (prefix `project:`):

- `.opencode/commands/`
- `.agents/commands/`
- `.claude/commands/`

Project commands are discovered by walking up from the current working directory to the Git worktree root, checking each directory along the way.

Each `.md` file becomes a command. The filename (without extension) becomes the command ID.

## Frontmatter

Commands support optional YAML frontmatter to define a human-readable title and description. The command ID is always auto-generated from the file path.

```markdown
---
title: Prime Context
description: Load key project files and git state into context
---
RUN git ls-files
READ README.md
```

Without frontmatter, the title defaults to the full command ID (e.g., `user:prime-context`) and the description shows the source file path.

## Slash Commands

Type `/` in the chat input to open an autocomplete popup with all available commands (built-in and custom). The popup supports:

- **Fuzzy search**: keep typing after `/` to filter commands
- **Tab / Shift+Tab**: cycle through matches
- **Enter**: select the command
- **Esc / Backspace**: dismiss the popup

### Selecting a command stages it — it is not sent

Selecting a **prompt command** (any command that contributes text: `/init`, `/review`, `/commit`, and every custom command) — or a skill — writes the invocation back into the editor as ordinary editable text:

```
> /review HEAD~3 
```

Nothing has been sent yet. From here you can:

- type your own instructions around it,
- add another invocation (each on its own line),
- edit the arguments, or
- delete the line and start over.

Press **Enter** to submit. Every invocation in the message is expanded in place, and your prose keeps its position between the expanded blocks:

```
> /skill:flow-creator build a review flow
  make it run on MR events only
  /review HEAD~3
```

becomes one message: the `flow-creator` skill content, then your sentence, then the `/review` prompt.

**Action commands** (`/new`, `/reset`, `/compact`, `/agents`, `/auto-approve`, `/vim`, `/sessions-cleanup`, `/rename`, `/loop`, `/crons`) do something in the app rather than adding text, so they still run the moment you select them and cannot be combined with other content. Submitting `/new and then look at the diff` is rejected with a warning instead of clearing your session and dropping the sentence.

### What counts as an invocation

On submit, a line is expanded only when **all** of the following hold:

- it starts with `/` at column 0 — no leading spaces (`  /review` is plain text);
- it is **not** inside a fenced code block (```` ``` ```` or `~~~`);
- the first word resolves to an installed command or `skill:<name>`.

Everything else is left byte-for-byte alone, so `remember that /commit runs the git flow` and pasted diffs are safe. To write a literal command line, escape it: a line beginning `\/review` is sent as `/review`.

At most **10** invocations are expanded per message; more than that is rejected so a large paste cannot silently inline ten skill bodies.

If expansion fails for any reason — a rejected action command, a skill that is not user-invocable, over the cap — the warning appears and **your text stays in the editor**.

### Arguments and timing

- Arguments are split on whitespace, honouring quotes: `/review HEAD~3 "src/internal tools"` is two arguments.
- Argument prompts (see below) write their values back into the staged line, quoting anything that contains spaces, so what you typed in the dialog is what gets bound.
- `` !`command` `` shell markup and `${SESSION_ID}` resolve **at submit time**, inside the expanded content only — never in your own prose. If the message waits in the queue behind a busy agent, the values captured are the ones from when you pressed Enter.

## Named Arguments

Commands support named placeholders in the format `$NAME` (uppercase letters, numbers, underscores; must start with a letter). OpenCode prompts you for each unique placeholder when you select the command, and binds them **positionally** in the order they first appear in the command body — so the same command can also be invoked directly as `/fetch-issue 123 alice`.

```markdown
---
title: Fetch Issue Context
description: Gather context for a GitHub issue
---
# Fetch Context for Issue $ISSUE_NUMBER

RUN gh issue view $ISSUE_NUMBER --json title,body,comments
RUN git grep --author="$AUTHOR_NAME" -n .
RUN grep -R "$SEARCH_PATTERN" $DIRECTORY
```

## Subdirectories

Organize commands in subdirectories — the path becomes part of the command ID with `:` separators:

```
~/.config/opencode/commands/git/commit.md → user:git:commit
.agents/commands/deploy/staging.md        → project:deploy:staging
```

## Built-in Commands

| Command | Slash | Description |
|---------|-------|-------------|
| List Agents | `/agents` | List all available agents and their configuration |
| Initialize Project | `/init` | Creates or updates the `AGENTS.md` memory file |
| Compact Session | `/compact` | Manually triggers session summarization |
| New Session | `/new`, `/reset` | Start a fresh session (same as `ctrl+n`) |
| Review Code | `/review` | Reviews code using a provided commit hash or branch |
| Commit and Push | `/commit` | Commit changes to git using conventional commits and push |
| Auto-Approve | `/auto-approve` | Toggle auto-approve mode for the current session (skip permission dialogs) |

