# Runtime profiles

One opencode installation can run isolated runtime profiles — e.g. a personal
("home") setup and a work setup — selected per launch, the way
`CLAUDE_CONFIG_DIR` selects a Claude Code profile.

```bash
opencode --profile piano            # TUI
opencode serve --profile piano      # server
OPENCODE_PROFILE=piano opencode     # env form (same effect)
```

With no profile (or an invalid name), behaviour is exactly as before this
feature existed — the default profile *is* the classic setup.

## What a profile scopes

With `OPENCODE_PROFILE=<name>` active:

| thing | default (no profile) | with profile `<name>` |
|---|---|---|
| global config file | `~/.opencode.json` (then XDG variants) | `~/.config/opencode-<name>/.opencode.json` first, base files as fallback |
| global skills | `~/.config/opencode/{skill,skills}`, `~/.agents/skills`, `~/.claude/skills` | `~/.config/opencode-<name>/{skill,skills}` only |
| global agents | `~/.config/opencode/agents`, `~/.agents/types` | `~/.config/opencode-<name>/agents` only |
| global flows | `~/.config/opencode/flows`, `~/.agents/flows` | `~/.config/opencode-<name>/flows` only |
| custom commands | XDG `opencode/commands`, `~/.opencode/commands`, `~/.agents/commands`, `~/.claude/commands` | XDG `opencode-<name>/commands` only |

Config writes (`UpdateCfgFile`) go to the profile file: when no config file
exists yet, a profile run creates `~/.config/opencode-<name>/.opencode.json`
(dir `0700`) instead of `~/.opencode.json`.

Project-level `.opencode.json` still merges over the profile's global file.
The per-project data dir (`<cwd>/.opencode`, sessions DB) is deliberately
**shared** across profiles — same project, same sessions. Memory is selected
independently with `AGENTS_MEMORY_HOME` (see `memory.md`).

Profile names must match `^[A-Za-z0-9][A-Za-z0-9_-]*$`; anything else (path
separators, `..`, whitespace) is ignored, never fatal.

## With env-referenced secrets

Profiles combine with [env references in config](config-env.md) to keep
credentials out of the files entirely: the profile config holds
`"${ATLASSIAN_TOKEN}"`-style references, and the launching shell provides the
values (e.g. a nouwaland shell profile that exports `OPENCODE_PROFILE` and
sources the matching `~/.secrets` services per directory).

```jsonc
// ~/.config/opencode-piano/.opencode.json
{
  "providers": { "vertexai": { "apiKey": "${OPEN_AI_PIANO_TOKEN}", ... } },
  "mcpServers": { "datadog": { "headers": { "DD_API_KEY": "${DD_API_KEY}" } } }
}
```
