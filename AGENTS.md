# OpenCode Development Guide

## Build/Test Commands
- **Build**: `./scripts/snapshot` (uses goreleaser)
- **Test**: `go test ./...` (all packages) or `go test ./internal/llm/agent` (single package)
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
- `maxTokens`: Maximum tokens for responses
- `reasoningEffort`: For models that support it (low/medium/high)
- `permission`: Agent-specific permission overrides (e.g., for skills)
- `tools`: Enable/disable specific tools for this agent

**Built-in agents:**
- `coder`: Main coding agent (uses all tools)
- `task`: Task planning agent (read-only tools)
- `summarizer`: Session summarization agent
- `title`: Session title generation agent

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

1. **Agent tool disable**: `agents.coder.tools.skill = false` → deny
2. **Agent-specific**: `agents.coder.permission.skill.internal-* = allow`
3. **Global**: `permission.skill.internal-* = deny`
4. **Default**: ask

**Actions:**
- `allow`: Execute immediately
- `deny`: Block access
- `ask`: Prompt user (default)
