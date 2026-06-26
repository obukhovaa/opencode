# OpenCode Development Guide

## Build/Test Commands
- **Build**: `./scripts/snapshot` (uses goreleaser)
- **Test**: `go test ./...` (all packages) or `go test ./internal/llm/agent` (single package)
- **Final check**: run `make test` when work is done to run final checks, including all tests and fromatters
- **E2E tests**: `make test-e2e` runs every `scripts/test/*.sh` against a freshly-built binary or in-process driver. Each script is self-contained (mktemp sandbox, no external services). Add a new script there when adding a feature whose runtime behavior isn't fully exercised by unit tests (cross-process integration, viper round-trip, etc.).
- **Generate schema**: `go run cmd/schema/main.go > opencode-schema.json`
- **Generate mocks**: `go generate ./...` (generates mocks for interfaces)
- **Database migrations**: Uses sqlc for SQL code generation from `internal/db/sql/`
- **Security check**: `./scripts/check_hidden_chars.sh` (detects hidden Unicode)

### `.opencode.json` schema is part of the public contract

**Whenever you add, rename, or remove a field on `Config` (`internal/config/config.go`) — or on any struct it references that surfaces in `.opencode.json` — you MUST:**

1. Update `cmd/schema/main.go` to declare the new field's JSON-Schema shape (type, description, defaults, enum constraints).
2. Regenerate `opencode-schema.json` via `go run cmd/schema/main.go > opencode-schema.json` and commit the result alongside the code change.
3. Update `docs/` (`docs/hooks.md`, `docs/flows.md`, etc.) and the relevant `openspec/changes/.../specs/<capability>/spec.md` if the field has user-facing behavior.

The schema file is consumed by IDEs / `vscode-jsonschema` / Claude Code's own validators — a stale schema means our users see false-positive errors on a valid config or no validation on an invalid one. Schema drift is a silent breakage; treat it as a build failure.

When adding fields that contain hooks, agents, providers, or any map keyed on user-supplied names, ALSO add a unit test under `internal/config/` exercising `viper.Unmarshal` end-to-end. Viper case-folds map keys; pure `json.Unmarshal` tests pass but the loader silently mangles in production (see `TestConfig_HooksViperRoundTripLowercasesEventKeys`).

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
- `maxTurns`: Maximum number of tool-use turns per request (default 100). Also configurable via `--max-turns` CLI flag (overrides per-agent config) or top-level `maxTurns` in `.opencode.json`.
- `reasoningEffort`: For models that support it (`low`/`medium`/`high`)
- `mode`: `agent` (primary, switchable via tab) or `subagent` (invoked via task tool)
- `name`: Display name for the agent
- `description`: Short description of agent's purpose
- `prompt`: Custom system prompt (overrides builtin prompt)
- `color`: Badge color for subagent indication in TUI (e.g., `primary`, `secondary`, `warning`, `error`, `info`, `success`)
- `hidden`: If true, agent is not shown in TUI switcher or subagent lists
- `native`: Whether this is a built-in agent (set automatically, not user-configurable)
- `skills`: List of skill names to preload into the agent's system prompt at startup (e.g., `["review", "domain-knowledge"]`). Skills are injected as `<skill_content>` blocks — the agent gets the knowledge without needing to invoke the skill tool. Only skills with `allow` or default (no explicit deny) permission are injected. Preloaded skills are independent of the skill tool — `tools: {"skill": false}` disables runtime loading but preloaded skills are still injected. Variable substitution (`$ARGUMENTS`, `${SKILL_DIR}`) and shell markup (`!`command``) are not expanded for preloaded skills.
- `taskBudget`: Advisory token budget for the full agentic loop (min 20,000). Only supported by models with `SupportsTaskBudget` (currently Claude Opus 4.7). Uses the `task-budgets-2026-03-13` beta header. The budget is carried across compaction via the `remaining` field.
- `permission`: Agent-specific permission overrides (supports granular glob patterns per tool)
- `tools`: Enable/disable specific tools (e.g., `{"skill": false, "bash": false}`)

Here's the list of **built-in agents** available by default:
- `coder`: Main coding agent, can spawn subagents (all tools)
- `hivemind`: Supervisory agent, can spawn subagents (coordinates subagents to solve complex problems, read-only tools)
- `explorer`: Codebase exploration subagent (read-only tools)
- `workhorse`: Autonomous coding subagent (all tools)
- `summarizer`: Summarization subagent (no tools)
- `descriptor`: Short description generation subagent (no tools)

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
skills:
  - review
  - composer-domain-expertise
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

### Chat Bridge

Telegram / Slack / Mattermost adapters live in-process under `internal/bridge/` and mount HTTP routes under `/router/*` on the existing API mux. The bridge boots when `.opencode.json` has a non-empty `router` section with at least one enabled channel identity. Full docs: [`docs/bridge.md`](docs/bridge.md).

Things to know when working in the codebase:

- **Package layout**: `internal/bridge/` holds types only (no internal deps); orchestrator code lives in `internal/bridge/service/`; per-platform adapters under `internal/bridge/{slack,telegram,mattermost}/`. The split is required to break the import cycle `bridge.service → llm/agent → llm/tools → bridge`.
- **Per-session dispatch goroutine**: each bound session owns one inbound-handling goroutine (cap 16 inbound, never drop) + one parts-handling goroutine (cap 64 parts, drop-oldest). Tool-update streaming runs in parallel with `agent.Run` — both run concurrently so tool icons interleave with the agent's progress instead of arriving after the final reply.
- **Sub-package config**: `cfg.Router.PermissionMode` (`allow` / `deny` / `ask` / empty) determines whether the bridge auto-resolves agent permission requests on bridge-owned sessions (direct bindings or subagent sessions whose `root_session_id` matches a bound row). Unrecognised values fail-safe to deny with a one-shot WARN log.
- **`router_send` agent tool**: lives at `internal/llm/tools/router_send.go` (not under `internal/llm/agent/tools/`). The tools package declares a `BridgeSender` interface; the bridge service satisfies it without creating an import cycle. The tool's description is rebuilt dynamically per registration from the live `cfg.Router` snapshot.
- **Single-writer election**: the bridge takes a per-identity lock to prevent two opencode processes from owning the same chat identity — SQLite uses `flock` on `<dataDir>/bridge.lock`; MySQL uses `GET_LOCK` on a dedicated `*sql.Conn`. Adapter launch fails cleanly if another process already owns the identity.
- **Cost attribution**: subagent costs (`task` tool) roll into the parent's `sess.Cost` via `agent-tool.go::Run` even on canceled/error paths. The bridge's `/sessions` and `/session` commands use this aggregated value.
- **`tools.GetContextValues(ctx)`** returns `(sessionID, messageID)`; the bridge dispatcher injects both before calling `agent.Run`. Subagent sessions inherit `sessionID` from `taskSession.ID` and `root_session_id` from the parent — this is the link the bridge's `PermissionRouter` and parts demux use to scope to "bridge-owned" sessions.

## TUI Pitfalls

### Background color gaps in dialogs/pages

`lipgloss.JoinVertical` and `lipgloss.JoinHorizontal` produce lines of different widths. Shorter lines (e.g. a button row) leave cells with no background, which render as black. **Always** wrap the final rendered string with `styles.ForceReplaceBackgroundWithLipgloss(rendered, bg)` before returning from `View()`. This forces every ANSI cell to the theme background. See `internal/tui/styles/background.go` for the implementation and `internal/tui/page/crons.go` or `internal/tui/components/dialog/missed_crons.go` for usage examples.

### Value-receiver Update and shared state

`appModel.Update` uses a **value receiver** (`func (a appModel) Update`). Bubbletea dereferences the pointer returned by `New()`, copies the struct, mutates the copy, and stores the returned copy. The original `*appModel` is never updated again. **Never** capture the model pointer in a closure that outlives `New()` — the closure will read stale zero-values. Instead, store shared mutable state on `*app.App` (which is a pointer field that survives copying) using `atomic.Value` or a mutex. See `App.SetActiveSessionID` / `App.ActiveSessionID` for the pattern.
