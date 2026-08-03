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

1. `agentPaths` in `.opencode.json` — custom directories scanned for `*.md` agents (lowest priority; supports `~` and relative paths, scanned non-recursively, mirrors `skills.paths`)
2. `~/.config/opencode/agents/*.md` (global)
3. `~/.agents/types/*.md` (global)
4. `.opencode/agents/*.md` (project)
5. `.agents/types/*.md` (project)
6. `.opencode.json` `agents` config (project — highest priority)

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

### Flow step templates (`include` / `extends`)

A flow file may declare `include:` at the top level listing local files that contribute reusable **step templates**, and a step may declare `extends: [".name", …]`. Templates are `.`-prefixed top-level keys in the included file. Full docs: [`docs/flows.md`](docs/flows.md#shared-step-templates-include--extends). Implementation: `internal/flow/include.go`, called from `parseFlowFile`.

What to keep in mind when working on this code:

- **Resolution runs inside `parseFlowFile` before `$ref` resolution and before `validateFlow`**, so a merged step is validated exactly as an inline one. Moving it after validation would let a template smuggle an invalid step through.
- **The merge is driven by each step's RAW declared keys**, recovered by a second `yaml.Unmarshal` of the same bytes (`stepRawKeys`) — the pattern `validateFlowSessionKeys` already uses. This is not incidental: on a typed struct an explicit zero is indistinguishable from an absent key, and the affected inheritable fields are exactly `maxTurns`, `maxIterations`, `timeout`, `agent`, `prompt` and `session` (a **value** type, so `session: {fork: false}` decodes identically to omitting it). Only `compact` is safe, being a pointer. The copy itself is by yaml key through reflection (`stepFieldIndexByYAMLKey`), deliberately **not** a per-field switch — so adding a field to `Step` needs no change here.
- **The two parses can disagree on element count; that is checked (`stepsRawKeysAligned`), never padded over.** `yaml.v3` DROPS a null / comment-only sequence entry (`- # placeholder`) from a typed `[]Step` decode but KEEPS it as an empty map in the raw one. Pairing by index then hands the real step an empty key set, which reads as "declared nothing", so the template overwrites keys the step *did* set — a guard step silently running an agent its flow does not name, invisible to `validateFlow` because the dropped entry is not in the typed tree. A bounds check at the merge site would have *hidden* this, and so would `stepRawKeys` swallowing a parse error into a nil slice; both fail closed instead. Probed and found NOT to diverge: anchors/aliases, merge keys (`<<:` — merged-in keys appear in both decodes, so they count as declared by the step), multi-document files, `- {}`, and a non-sequence `steps:`.
- **Template keys are checked by a two-part rule, not an allow-list of inheritable fields.** A key must (1) be a known `Step` field — so `promt:` is a load error listing the real fields, instead of being silently dropped as a typed decode drops it — **and** (2) not be one of `id`, `interactive`, `interaction`, `resume_after`, each of which errors quoting *why*. Consequence, and the point of the shape: **a field added to `Step` later is inheritable by default.** An allow-list would put the maintenance friction on the fast-growing side — this engine's `Step` gains fields regularly while the orchestrator's small struct almost never changes — so every new engine field would be silently non-inheritable until someone curated a list in `include.go`.
- **Why those four specifically** — not inferable from this repo: **the flow YAML has a second parser.** The Piano `c2-agent` orchestrator reads the same file with its own structs (task card, reviewer-argument enrichment, postpone-resume timing), is built and released separately, and never resolves templates. A key it reads must stay in the flow file where it can see it, or the author gets a correct-looking flow with silent misbehaviour (`interactive` in a template → a job with nobody bound to answer). `resume_after` is not even modelled by this engine. Known limitation: the rule is top-level only — a *nested* field the orchestrator starts reading later (e.g. a new `session:` sub-key) rides in inside an inheritable key.
- **`stepTemplate` holds a `Step` value** (`tmpl.step`), which is safe only because the two-part check runs before the decode and rejects those four by name, so the decode never sees them. `TestTemplateKeyRule_TwoPart` is driven off `Step`'s own yaml tags and fails when a new field has no sample — that failure asks for a test sample, not for an allow-list.
- **Templates are decoded into a raw map first** so an unknown key is *visible*. A typed decode drops it silently — that is how `flow.session` typos used to pass unnoticed.
- **`include: local:` paths are root-relative** (`config.WorkingDir`), while an `output.schema` `$ref` stays relative to the file declaring it — so a template's `$ref` is resolved at include-load time against the template's own directory, before the merge, because afterwards there is no per-key provenance left. Use `config.Get()` and nil-check it, never `config.WorkingDirectory()`: that panics when no config is loaded and `parseFlowFile` is called from tests without one.
- **Templates are leaves**: no `include` in a template file, no `extends` in a template. The per-file size cap applies to each included file.
- **Errors surface as a missing flow, not a stopped one** — `scanFlowDirectory` WARNs and skips. Keep error messages naming the file, template and key; that log line is all an operator gets.

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
