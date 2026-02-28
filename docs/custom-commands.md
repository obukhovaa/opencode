# Custom Commands

## Overview

Custom commands are predefined prompts stored as Markdown files that you can quickly execute via `Ctrl+K` (command palette) or by typing `/` in the chat editor (slash commands with autocomplete).

## Locations

Commands are discovered from the following directories, in order:

**User commands** (prefix `user:`):

- `$XDG_CONFIG_HOME/opencode/commands/` (defaults to `~/.config/opencode/commands/`)
- `$HOME/.opencode/commands/`
- `$HOME/.agents/commands/`

**Project commands** (prefix `project:`):

- `<PROJECT_DIR>/.opencode/commands/`
- `<PROJECT_DIR>/.agents/commands/`

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
- **Enter**: execute the selected command
- **Esc / Backspace**: dismiss the popup

When a command is selected, the `/query` text is removed from the editor so you can continue typing your message.

## Named Arguments

Commands support named placeholders in the format `$NAME` (uppercase letters, numbers, underscores; must start with a letter). OpenCode prompts you for each unique placeholder at runtime.

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
| Initialize Project | `/init` | Creates or updates the `AGENTS.md` memory file |
| Compact Session | `/compact` | Manually triggers session summarization |
| Review Code | `/review` | Reviews code using a provided commit hash or branch |
| Commit and Push | `/commit` | Commit changes to git using conventional commits and push |

