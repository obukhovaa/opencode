# OpenCode Development Guide

## Build/Test Commands
- **Build**: `./scripts/snapshot` (uses goreleaser)
- **Test**: `go test ./...` (all packages) or `go test ./internal/llm/agent` (single package)
- **Final check**: run `make test` when work is done to run final checks, including all tests and fromatters
- **Generate schema**: `go run cmd/schema/main.go > opencode-schema.json`
- **Generate mocks**: `go generate ./...` (generates mocks for interfaces)
- **Database migrations**: Uses sqlc for SQL code generation from `internal/db/sql/`
- **Security check**: `./scripts/check_hidden_chars.sh` (detects hidden Unicode)

## Code Style Guidelines

### Imports
- Three groups: stdlib, external, internal (separated by blank lines)
- Sort alphabetically within groups
- Internal imports: `github.com/opencode-ai/opencode/internal/...`

### Naming
- Variables: camelCase (`filePath`, `contextWindow`)
- Functions: PascalCase exported, camelCase unexported
- Types/Interfaces: PascalCase, interfaces often end with "Service"
- Packages: lowercase single word (`agent`, `config`)

### Error Handling
- Named error variables: `var ErrRequestCancelled = errors.New(...)`
- Early returns: `if err != nil { return nil, err }`
- Error wrapping: `fmt.Errorf("context: %w", err)`

### Testing
- Table-driven tests with anonymous structs
- Subtests with `t.Run(name, func(t *testing.T) {...})`
- Test naming: `Test<FunctionName>`
- Use `go:generate mockgen` for interface mocks
- Mock files in `<package>/mocks/` directory

## Configuration

### Agent Configuration

Agents can be configured in `.opencode.json`:

```json
{
  "agents": {
    "coder": {
      "model": "vertexai.claude-sonnet-4-5-m",
      "maxTokens": 64000,
      "reasoningEffort": "medium",
      "permission": {
        "skill": {
          "internal-*": "allow",
          "experimental-*": "deny"
        }
      },
      "tools": {
        "skill": true
      }
    },
    "summarizer": {
      "model": "vertexai.gemini-3.0-flash",
      "maxTokens": 64000,
      "tools": {
        "skill": false
      }
    }
  }
}
```

**Agent fields:**
- `model`: Model ID to use for this agent
- `maxTokens`: Maximum response tokens
- `reasoningEffort`: For models that support it (`low`/`medium`/`high`)
- `mode`: `agent` (primary, switchable via tab) or `subagent` (invoked via task tool)
- `name`: Display name for the agent
- `description`: Short description of agent's purpose
- `prompt`: Custom system prompt (overrides builtin prompt)
- `color`: Badge color for subagent indication in TUI (e.g., `primary`, `secondary`, `warning`, `error`, `info`, `success`)
- `hidden`: If true, agent is not shown in TUI switcher or subagent lists
- `native`: Whether this is a built-in agent (set automatically, not user-configurable)
- `permission`: Agent-specific permission overrides (supports granular glob patterns per tool)
- `tools`: Enable/disable specific tools (e.g., `{"skill": false, "bash": false}`)

**Built-in agents:**
- `coder`: Main coding agent, mode=agent (uses all tools)
- `hivemind`: Supervisory agent, mode=agent (coordinates subagents via task tool)
- `explorer`: Codebase exploration subagent (read-only tools)
- `workhorse`: Autonomous coding subagent (all tools, invoked by coder/hivemind)
- `summarizer`: Session summarization subagent
- `descriptor`: Session title generation subagent

### Custom Agents via Markdown

Agents can also be defined as markdown files with YAML frontmatter (same format as skills). The registry discovers agents from these locations, in merge priority order (lowest to highest):

1. `~/.config/opencode/agents/*.md` (global)
2. `~/.agents/types/*.md` (global)
3. `.opencode/agents/*.md` (project)
4. `.agents/types/*.md` (project)
5. `.opencode.json` `agents` config (project — highest priority)

The file basename (without `.md`) becomes the agent ID. Example:

`.opencode/agents/reviewer.md`:
```markdown
---
name: Code Reviewer
description: Reviews code for quality, security, and best practices
mode: subagent
color: info
permission:
  bash:
    "*": deny
  edit:
    "*": deny
tools:
  bash: false
  write: false
---

You are a code review specialist. When given code to review...
```

Fields set in higher-priority sources override lower-priority ones. For native agents, markdown files can override `name`, `description`, `prompt`, `color`, `permission`, and `tools` while preserving built-in defaults.

### Skills System

Skills are reusable instruction sets that agents can load on-demand. See [Skills Guide](docs/skills.md) for details.

**Key concepts:**
- Skills are markdown files with YAML frontmatter
- Discovered from `.opencode/skills/`, `.agents/skills/`, `~/.config/opencode/skills/`, `~/.agents/skills/`, and custom paths
- Permissions control which skills agents can access
- Agent-specific permissions override global permissions

**Example skill structure:**
```
.opencode/skills/git-release/SKILL.md
```

**Permission patterns:**
- Exact match: `git-release: allow`
- Wildcards: `internal-*: deny`, `*-test: ask`
- Global: `*: ask`

### Permission System

Permissions use pattern matching with priority:

1. **Agent tool disable**: `agents.coder.tools.bash = false` → deny
2. **Agent-specific**: `agents.coder.permission.bash.{"git *": "allow"}` 
3. **Global**: `permission.rules.bash = "ask"` or `permission.skill.internal-* = deny`
4. **Default**: ask

**Actions:**
- `allow`: Execute immediately
- `deny`: Block access
- `ask`: Prompt user (default)

**Granular permissions** support both simple strings and glob-pattern objects per tool:

```json
{
  "permission": {
    "skill": { "*": "ask", "internal-*": "allow" },
    "rules": {
      "bash": { "*": "ask", "git *": "allow", "rm -rf *": "deny" },
      "edit": { "*": "allow", "*.env": "deny" },
      "read": { "*": "allow" },
      "task": { "*": "allow", "explorer": "allow" }
    }
  }
}
```

**Supported permission keys:**

| Key | Granular Pattern | Example |
|-----|-----------------|---------|
| `skill` | Skill name glob | `{"internal-*": "allow", "*": "ask"}` |
| `bash` | Command glob | `{"*": "ask", "git *": "allow"}` |
| `edit` | File path glob | `{"*": "deny", "src/**/*.go": "allow"}` |
| `read` | File path glob | `{"*": "allow", "*.env": "deny"}` |
| `task` | Subagent name glob | `{"*": "allow", "explorer": "allow"}` |

### TUI Agent Switching

Press `tab` to cycle through primary agents (mode=`agent`, hidden=false) in the TUI. The active agent is shown in the status bar. Agent switching applies to the next new session.
