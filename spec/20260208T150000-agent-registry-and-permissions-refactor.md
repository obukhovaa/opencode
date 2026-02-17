# Agent Registry and Permissions Refactor

**Date**: 2026-02-08
**Status**: Implemented
**Author**: AI-assisted

## Overview

Introduce an in-memory agent registry with discovery from config and markdown files, rework the permission system to be generic and granular across all tools, rename/add native agents, refactor the agent tool into a `task` tool with subagent selection, and add TUI features for agent switching and subagent indication.

## Motivation

### Current State

Agents are hardcoded as an enum of four names and wired together via `sync.Once` singletons in `internal/llm/agent/tools.go`:

```go
type AgentName string

const (
    AgentCoder      AgentName = "coder"
    AgentSummarizer AgentName = "summarizer"
    AgentTask       AgentName = "task"
    AgentTitle      AgentName = "title"
)
```

Tool lists are built once per process with `coderToolsOnce` / `taskToolsOnce`. The agent tool (`agent-tool.go`) always spawns a read-only task agent with no subagent selection. Permission logic for skills is inlined in `internal/llm/tools/skill.go` and not reusable.

This creates problems:

1. **No extensibility**: Users cannot define custom agents via config or markdown files. Adding a new native agent requires changes across `config.go`, `agent.go`, `tools.go`, `prompt.go`, and the TUI.
2. **No subagent selection**: The agent tool always spawns the same read-only task agent. There is no way to choose between a read-only explorer vs a full-capability workhorse subagent.
3. **Permission logic is tool-specific**: Skill permission evaluation is duplicated and not reusable for bash command patterns, file path globs, or other granular rules.
4. **No TUI awareness**: Users cannot see or switch which primary agent is active. Subagent invocations show no indication of which subagent is running.

### Desired State

- An `AgentRegistry` interface discovers agents from builtins + config + markdown files with layered merge priority.
- The `task` tool (renamed from `agent`) accepts a `subagent_type` parameter to pick which subagent to spawn.
- Permission evaluation is generic, supporting both simple (`"allow"`) and granular (`{"*": "ask", "git *": "allow"}`) rules for any tool.
- TUI shows active primary agent in status bar, allows `tab` switching, and displays colored subagent badges during task invocations.

## Research Findings

### Reference Implementation (TypeScript opencode)

The upstream TypeScript codebase at `packages/opencode/src/agent/agent.ts` defines agents as a `Record<string, Info>` built at init time with:

| Aspect | Upstream TS | Current Go |
|---|---|---|
| Agent definition | Zod schema with `mode`, `name`, `description`, `native`, `hidden`, `color`, `permission`, `prompt` | Flat struct with `Model`, `MaxTokens`, `ReasoningEffort`, `Permission`, `Tools` |
| Discovery | Config JSON + markdown files in `~/.config/opencode/agents/` and `.opencode/agents/` | Config JSON only |
| Modes | `primary`, `subagent`, `all` | Implicit (hardcoded enum) |
| Permission model | `PermissionNext` ruleset with merge priority, glob patterns per tool, granular bash/edit/read rules | Skill-only pattern matching in `skill.go`, flat `map[string]map[string]string` |
| Task tool | Accepts `subagent_type`, `task_id` for resumption | Always spawns single read-only task agent |

**Key finding**: The upstream design separates agent identity/config from tool wiring, uses a registry pattern, and keeps permission resolution inside the registry.

**Implication**: We should follow this pattern but adapt it to Go idioms — interfaces, struct composition, and explicit initialization rather than lazy singletons.

### Permission Granularity (from docs/permissions)

The reference docs describe a layered permission model:

| Permission Key | Granular Pattern | Example |
|---|---|---|
| `bash` | Command glob | `{"*": "ask", "git *": "allow", "rm *": "deny"}` |
| `edit` | File path glob | `{"*": "deny", "src/**/*.go": "allow"}` |
| `read` | File path glob | `{"*": "allow", "*.env": "deny"}` |
| `skill` | Skill name glob | `{"internal-*": "allow", "*": "ask"}` |
| `task` | Subagent name glob | `{"*": "deny", "explorer": "allow"}` |
| `external_directory` | Directory path glob | `{"~/projects/*": "allow"}` |

**Key finding**: Every tool permission can be either a simple string (`"allow"`) or an object with glob-pattern keys. The last matching rule wins.

**Implication**: We need a unified `EvaluatePermission(toolName, input, agentName)` function that handles both forms.

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Agent mode enum | `agent`, `subagent` (not `primary`/`all`) | Matches user-facing language in the spec request; simpler than three modes |
| Registry interface | `AgentRegistry` with `Get`, `List`, `ListByMode`, `ResolvedPermissions` | Encapsulates discovery, merge, and permission resolution; testable via interface |
| Discovery merge priority | global markdown (lowest) → global config → project markdown → project config (highest) | Matches spec requirement; project config always wins |
| Markdown agent format | YAML frontmatter + body as prompt (same as skills) | Consistent with existing skill discovery pattern |
| Permission evaluation | Move to `internal/permission/permission.go` as generic functions | Currently skill-only; needs to work for bash, edit, read, task, etc. |
| Rename `task` agent → `explorer` | Yes | Aligns with upstream naming; avoids confusion with `task` tool |
| Rename `title` agent → `descriptor` | Yes | Spec requirement |
| Tool rename `agent` → `task` | Yes | Aligns with upstream; accepts `subagent_type` and `task_id` |
| New native agents | `workhorse` (subagent), `hivemind` (agent) | Spec requirement |
| TUI agent switching | `tab` key cycles primary agents | Matches upstream behavior |
| Singleton tool lists | Remove `sync.Once` pattern | Registry provides per-agent tool lists; singletons prevent per-agent customization |

## Architecture

### Agent Registry

```
┌─────────────────────────────────────────────────────────────┐
│                     AgentRegistry                           │
│                                                             │
│  ┌──────────────┐  ┌──────────────┐  ┌───────────────────┐  │
│  │ Builtin Defs │  │ Config Merge │  │ Markdown Discover │  │
│  │ (native=true)│  │ (.opencode   │  │ (~/.config/       │  │
│  │              │  │  .json)      │  │  opencode/agents/ │  │
│  └──────┬───────┘  └──────┬───────┘  │  .opencode/       │  │
│         │                 │          │  agents/)          │  │
│         │                 │          └──────┬────────────┘  │
│         └────────┬────────┘                 │               │
│                  ▼                          │               │
│         ┌────────────────┐                  │               │
│         │  Merge Layer   │◀─────────────────┘               │
│         │  (priority     │                                  │
│         │   resolution)  │                                  │
│         └───────┬────────┘                                  │
│                 ▼                                           │
│         ┌────────────────┐                                  │
│         │ Agent Map      │                                  │
│         │ id → AgentInfo │                                  │
│         └───────┬────────┘                                  │
│                 │                                           │
│     ┌───────────┼───────────────┐                           │
│     ▼           ▼               ▼                           │
│  Get(id)    List()    ResolvedPermissions(id, tool, input)  │
└─────────────────────────────────────────────────────────────┘
```

### Agent Info Structure

```
┌─────────────────────────────────────────┐
│              AgentInfo                  │
│  ├── ID:          string                │
│  ├── Name:        string                │
│  ├── Description: string                │
│  ├── Mode:        AgentMode (enum)      │
│  ├── Native:      bool                  │
│  ├── Model:       models.ModelID        │
│  ├── MaxTokens:   int64                 │
│  ├── ReasoningEffort: string            │
│  ├── Prompt:      string                │
│  ├── Color:       string                │
│  ├── Hidden:      bool                  │
│  ├── Permission:  PermissionRuleset     │
│  ├── Tools:       map[string]bool       │
│  └── Options:     map[string]any        │
└─────────────────────────────────────────┘
```

### Permission Evaluation Flow

```
STEP 1: Check agent tool disable
─────────────────────────────────
Agent.Tools["bash"] == false → DENY (short circuit)

STEP 2: Evaluate granular rules (last match wins)
──────────────────────────────────────────────────
Agent.Permission["bash"] could be:
  • string "allow" → simple match
  • map {"*": "ask", "git *": "allow"} → glob pattern match
    Iterate rules in order, last matching pattern wins

STEP 3: Fallback to global permission
──────────────────────────────────────
Config.Permission["bash"] evaluated same way

STEP 4: Default
────────────────
Return "ask"
```

### Task Tool Flow

```
STEP 1: Caller invokes task tool
─────────────────────────────────
Parameters: { prompt, subagent_type, task_id? }

STEP 2: Resolve subagent
─────────────────────────
Registry.Get(subagent_type) → AgentInfo
Validate mode == "subagent" (or "agent" for hivemind orchestration)

STEP 3: Resolve permissions & tools
────────────────────────────────────
Registry.ResolvedTools(subagent_type) → []BaseTool
(pre-filtered by agent permission config)

STEP 4: Create or resume session
─────────────────────────────────
task_id provided? → resume existing session
task_id absent?   → create new child session

STEP 5: Run agent loop
──────────────────────
Provider.Send(messages, tools) → stream response

STEP 6: Return result with task metadata
────────────────────────────────────────
Response includes task_id for possible resumption
Metadata includes subagent_type, is_resumed flag
```

### TUI Agent Switching

```
┌─────────────────────────────────────────────────────────┐
│ Status Bar                                              │
│ ┌──────────┐ ┌────────────────┐ ┌───────┐ ┌──────────┐ │
│ │ ctrl+?   │ │ Context: 15K   │ │ LSP   │ │[▶ Coder] │ │
│ │ help     │ │ Cost: $0.12    │ │ 0E 2W │ │ tab→next │ │
│ └──────────┘ └────────────────┘ └───────┘ └──────────┘ │
└─────────────────────────────────────────────────────────┘

Tab pressed → cycles: Coder Agent → Hivemind Agent → (custom) → …
```

### TUI Subagent Badge

```
During task tool invocation, messages show:

┌─────────────────────────────────────────┐
│ ┌──────────────────────────────┐        │
│ │ 🔵 Explorer Agent (new task) │        │
│ │ Searching for config files…  │        │
│ └──────────────────────────────┘        │
│                                         │
│ ┌──────────────────────────────┐        │
│ │ 🟠 Workhorse Agent (resumed) │        │
│ │ Applying changes to module…  │        │
│ └──────────────────────────────┘        │
└─────────────────────────────────────────┘

Each subagent has a unique color badge.
"resumed" vs "new task" indicated based on task_id presence.
```

## Implementation Plan

### Phase 1: Config & Agent Registry Foundation

- [x] **1.1** Extend `config.Agent` struct with new fields: `Mode` (enum: `agent`/`subagent`), `Name` (string), `Native` (bool), `Description` (string), `Prompt` (string), `Color` (string), `Hidden` (bool)
- [x] **1.2** Change `AgentName` from restricted enum to `type AgentName = string`, remove the const block, define builtin IDs as package-level vars: `AgentCoder`, `AgentSummarizer`, `AgentExplorer` (was `AgentTask`), `AgentDescriptor` (was `AgentTitle`), `AgentWorkhorse`, `AgentHivemind`
- [x] **1.3** Create `internal/agent/registry.go` with `Registry` interface:
  ```go
  type Registry interface {
      Get(id string) (AgentInfo, bool)
      List() []AgentInfo
      ListByMode(mode AgentMode) []AgentInfo
      EvaluatePermission(agentID, toolName, input string) permission.Action
      IsToolEnabled(agentID, toolName string) bool
      GlobalPermissions() map[string]any
  }
  ```
- [x] **1.4** Implement `newRegistry()` that:
  - Registers builtin agents with hardcoded defaults (model, prompt, tools, permissions)
  - Merges config overrides from `cfg.Agents`
  - Discovers markdown agent files from `~/.config/opencode/agents/`, `~/.agents/types/`, `.opencode/agents/`, `.agents/types/`
  - Applies merge priority: global markdown → global config → project markdown → project config
- [x] **1.5** Implement markdown agent parser (YAML frontmatter + body as prompt), reuse patterns from `internal/skill/skill.go`
- [x] **1.6** Write tests for registry: merge priority, markdown discovery, duplicate ID resolution, permission evaluation

### Phase 2: Permission System Refactor

- [x] **2.1** Add generic permission types to `internal/permission/evaluate.go`:
  ```go
  type Action string // "allow", "deny", "ask"
  func EvaluateToolPermission(toolName, input string, agentPerms, globalPerms map[string]any) Action
  func IsToolEnabled(toolName string, toolsConfig map[string]bool) bool
  func MatchWildcard(pattern, str string) bool
  ```
- [x] **2.2** Implement `EvaluateToolPermission` in `internal/permission/evaluate.go` — supports both simple string and granular object (glob patterns, last match wins)
- [x] **2.3** Update `PermissionConfig` in `config.go` to support generic tool permissions with `Rules map[string]any`
- [x] **2.4** Refactor `internal/llm/tools/skill.go` to call generic `permission.EvaluateToolPermission` instead of inline logic
- [x] **2.5** Update `internal/llm/tools/bash.go` to use generic permission evaluation with command-level granularity
- [x] **2.6** Update file tools (`edit.go`, `write.go`, `patch.go`, `multiedit.go`, `view.go`) to use generic permission evaluation with path-level granularity
- [x] **2.7** Wire `Registry.EvaluatePermission` to combine agent-specific + global rules
- [x] **2.8** Write tests for permission evaluation: simple rules, granular globs, merge precedence, agent override, IsToolEnabled

### Phase 3: Native Agent Definitions

- [x] **3.1** Define `coder` agent: mode=`agent`, name="Coder Agent", all tools enabled, existing coder prompt
- [x] **3.2** Define `summarizer` agent: mode=`subagent`, name="Summarizer Agent", no tools (prompt-only), existing summarizer prompt
- [x] **3.3** Rename `task` → `explorer`: mode=`subagent`, name="Explorer Agent", read-only tools (glob, grep, ls, sourcegraph, skill, view, view_image, fetch), task prompt adapted to exploration focus, restricted permissions (deny write/edit/bash by default)
- [x] **3.4** Rename `title` → `descriptor`: mode=`subagent`, name="Descriptor Agent", no tools, existing title prompt
- [x] **3.5** Define `workhorse` agent: mode=`subagent`, name="Workhorse Agent", full tool access like coder (bash, edit, write, patch, glob, grep, etc.), coder-like prompt adapted for autonomous task completion, receives work from parent agent and works until done
- [x] **3.6** Define `hivemind` agent: mode=`agent`, name="Hivemind Agent", has task tool + read tools, prompt focused on supervising/coordinating subagents, planning and delegating work, optionally following a provided flow (deterministic step sequence)
- [x] **3.7** Create prompt files for new agents: `internal/llm/prompt/workhorse.go`, `internal/llm/prompt/hivemind.go`
- [x] **3.8** Update `internal/llm/prompt/prompt.go` `GetAgentPrompt` to handle dynamic agents from registry (custom prompts from markdown files)

### Phase 4: Task Tool Refactor

- [x] **4.1** Rename tool from `agent` to `task` (kept `AgentToolName` as alias for backward compat)
- [x] **4.2** Add `subagent_type` parameter: selects which subagent to spawn from registry
- [x] **4.3** Add `task_id` parameter (optional): resumes an existing subagent session instead of creating new
- [x] **4.4** Update tool description dynamically listing available subagents and their descriptions from registry
- [x] **4.5** Update `Run` method to:
  - Look up subagent from registry by `subagent_type` (validates existence)
  - Resolve tools based on subagent type and tools config
  - Create or resume session based on `task_id`
  - Return metadata including `task_id`, `subagent_type`, `subagent_name`, `is_resumed` for TUI consumption
- [x] **4.6** Restrict task tool availability: only `coder` and `hivemind` agents have this tool via `CoderAgentTools` and `HivemindAgentTools`
- [x] **4.7** Remove old `sync.Once` singleton patterns for tool lists
- [x] **4.8** Update `internal/llm/agent/tools.go` with `WorkhorseAgentTools` and `HivemindAgentTools`

### Phase 5: Agent Service Refactor

- [x] **5.1** Update `createAgentProvider` to fallback to registry for custom agents (markdown-defined) that aren't in `cfg.Agents`; inherits coder's model if no model specified
- [x] **5.2** Update `app.App` to hold `Registry` and create agents from it
- [x] **5.3** Support multiple primary agents in `App` (coder + hivemind + any custom primary agents) via `PrimaryAgents` map and `ActiveAgentIdx`
- [x] **5.4** Update `agent.Service` interface to expose the agent's `AgentInfo` for TUI consumption
- [x] **5.5** Handle agent switching: `App.SwitchAgent()` cycles through primary agents, updates `CoderAgent` pointer

### Phase 6: TUI Agent Switching

- [x] **6.1** Add `tab` key binding in `internal/tui/tui.go` to cycle through primary agents (mode=`agent`, hidden=false)
- [x] **6.2** Update `internal/tui/components/core/status.go` to show active agent name in status bar using registry
- [x] **6.3** `SwitchAgent` handler shows info message with agent name; `CoderAgent` pointer updates automatically
- [x] **6.4** Update `internal/tui/page/chat.go` `sendMessage` to use currently active agent instead of hardcoded `CoderAgent`
- [x] **6.5** `tab` key binding auto-included in help dialog via `keyMap` struct

### Phase 7: TUI Subagent Indication

- [x] **7.1** Task tool response metadata includes `subagent_type`, `subagent_name`, `is_resumed`, `task_id`
- [x] **7.2** Update `internal/tui/components/chat/message.go` to detect task tool calls and render colored subagent badge
- [x] **7.3** Each subagent's badge color comes from registry `AgentInfo.Color` (supports theme colors: primary, secondary, warning, error, info, success); builtin fallbacks: explorer=blue, workhorse=orange
- [x] **7.4** Badge text format: `"● Explorer Agent (new task):"` or `"● Workhorse Agent (resumed):"`

### Phase 8: Documentation & Migration

- [x] **8.1** Update README.md: document new agent config fields, agent table with modes
- [x] **8.2** Update `docs/skills.md` if any skill discovery paths changed
- [x] **8.3** Generate updated `opencode-schema.json` with new config fields
- [x] **8.4** Add backward compatibility: old `task`/`title` names in config map to `explorer`/`descriptor` with deprecation warning logged via `migrateOldAgentNames()`
- [x] **8.5** Update CLAUDE.md with new agent names and configuration patterns

## Edge Cases

### Agent ID Collision Across Sources

1. User defines agent `explorer` in both `~/.config/opencode/agents/explorer.md` and `.opencode.json`
2. Project config wins (highest priority)
3. Fields from lower priority sources are used as defaults for unset fields

### Unknown Subagent Type in Task Tool

1. Task tool called with `subagent_type: "nonexistent"`
2. Return tool error response listing available subagents
3. Do not crash or panic

### Circular Agent Invocation

1. Hivemind spawns a task with `subagent_type: "hivemind"` (or another primary agent)
2. Task tool should only allow `subagent` mode agents (or agents explicitly configured for sub-invocation)
3. Validate mode before spawning; return error if agent is primary-only and not self

### Backward Compatibility

1. Existing config uses `"task"` or `"title"` as agent names
2. Registry recognizes old names, maps them to `explorer`/`descriptor`
3. Log deprecation warning once

### Agent Without Model Configured

1. Custom markdown agent has no `model` field
2. Subagents inherit the model of the invoking primary agent
3. Primary agents fall back to the coder's model

### Empty Registry (No Agents)

1. All builtin agents disabled via config
2. App should refuse to start with a clear error message
3. At least one primary agent must be active

## Open Questions

1. **Should `hivemind` have access to write/edit tools directly, or only through subagents?**
   - Options: (a) No direct tools, orchestration only via task, (b) Full tools like coder, (c) Configurable
   - **Recommendation**: (a) Orchestration only — keeps hivemind focused on coordination. But leave configurable via agent config.

2. **Should the `task` tool support invoking primary-mode agents as subagents?**
   - Options: (a) Strict: only subagent-mode, (b) Allow via explicit `task` permission
   - **Recommendation**: (a) Strict by default. A primary agent invoking another primary agent is unusual and could confuse the UX.

3. **Should markdown agent files support `tools` field to specify custom tool lists?**
   - Options: (a) Yes, same as JSON config, (b) No, only permissions control access
   - **Recommendation**: (a) Yes, for consistency with JSON config. The YAML frontmatter can include `tools: {bash: false}`.

4. **How should the Flow concept (mentioned for hivemind) be modeled?**
   - This is explicitly deferred as a future feature per the spec request. The hivemind prompt should reference flows conceptually but the data model can wait.
   - **Recommendation**: Defer. Hivemind prompt mentions flows but no schema or runtime support yet.

5. **Should agent switching mid-session be allowed, or only for new sessions?**
   - Options: (a) Only new sessions use the switched agent, (b) Allow switching mid-session
   - **Recommendation**: (a) Only new sessions. A session is associated with the agent that created it. Switching changes which agent handles the next `sendMessage` for a *new* session.

6. **How to handle `sync.Once` tool initialization when tools need to vary per agent?**
   - Current `coderToolsOnce`/`taskToolsOnce` prevent per-agent tool customization.
   - **Recommendation**: Remove singletons. The registry builds tool lists per agent at init time and caches them. MCP tool discovery can still be cached globally since MCP tools don't vary per agent.

## Success Criteria

- [x] `AgentRegistry` interface is implemented with full test coverage for discovery and merge
- [x] Permission evaluation works for all tools with both simple and granular rules
- [x] All six native agents (coder, summarizer, explorer, descriptor, workhorse, hivemind) are registered and functional
- [x] Task tool accepts `subagent_type` and `task_id`, correctly spawns the right subagent
- [x] TUI `tab` key cycles through primary agents; status bar shows active agent name
- [x] TUI shows colored subagent badges during task invocations with new/resumed indication
- [x] Old config using `task`/`title` agent names still works with deprecation warnings
- [x] `make test` passes
- [x] README and schema are updated

## References

- `internal/config/config.go` — Agent struct, AgentName type, PermissionConfig
- `internal/llm/agent/agent.go` — Agent service, NewAgent
- `internal/llm/agent/agent-tool.go` — Current agent tool (to become task tool)
- `internal/llm/agent/tools.go` — Tool list construction (to be replaced by registry)
- `internal/permission/permission.go` — Permission service (to be extended)
- `internal/llm/tools/skill.go` — Skill permission logic (to be generalized)
- `internal/llm/prompt/prompt.go` — Agent prompt dispatch
- `internal/tui/tui.go` — TUI keybindings, app model
- `internal/tui/components/core/status.go` — Status bar rendering
- `internal/tui/components/chat/message.go` — Message rendering (for subagent badges)
- `internal/tui/page/chat.go` — Chat page, session creation
- `internal/session/session.go` — Session service
- `internal/app/app.go` — App initialization, agent creation
- `internal/skill/skill.go` — Skill discovery pattern (reusable for agent markdown discovery)
- Reference: `packages/opencode/src/agent/agent.ts` (upstream TS implementation)
- Reference: `https://opencode.ai/docs/agents` (agent docs)
- Reference: `https://opencode.ai/docs/permissions/` (permission docs)
