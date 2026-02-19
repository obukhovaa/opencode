# Custom Commands

## Overview

Custom commands are predefined prompts stored as Markdown files that you can quickly send to the AI assistant via `Ctrl+K`.

## Locations

| Location | Prefix | Path |
|----------|--------|------|
| User commands | `user:` | `$XDG_CONFIG_HOME/opencode/commands/` or `$HOME/.opencode/commands/` |
| Project commands | `project:` | `<PROJECT_DIR>/.opencode/commands/` |

Each `.md` file becomes a command. The filename (without extension) becomes the command ID.

## Example

Create `~/.config/opencode/commands/prime-context.md`:

```markdown
RUN git ls-files
READ README.md
```

This creates a command called `user:prime-context`.

## Named Arguments

Commands support named placeholders in the format `$NAME` (uppercase letters, numbers, underscores; must start with a letter). OpenCode prompts you for each unique placeholder at runtime.

```markdown
# Fetch Context for Issue $ISSUE_NUMBER

RUN gh issue view $ISSUE_NUMBER --json title,body,comments
RUN git grep --author="$AUTHOR_NAME" -n .
RUN grep -R "$SEARCH_PATTERN" $DIRECTORY
```

## Subdirectories

Organize commands in subdirectories:

```
~/.config/opencode/commands/git/commit.md → user:git:commit
```

## Built-in Commands

| Command | Description |
|---------|-------------|
| Initialize Project | Creates or updates the `AGENTS.md` memory file |
| Compact Session | Manually triggers session summarization |
| Review Code | Reviews code using a provided commit hash or branch |
