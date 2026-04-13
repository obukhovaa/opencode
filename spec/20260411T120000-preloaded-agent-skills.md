# Preloaded Agent Skills

**Date**: 2026-04-11
**Status**: Implemented
**Author**: AI-assisted

## Overview

Allow agents to declare a list of skill names in their configuration. At prompt build time, each declared skill that passes permission checks and exists in the skill registry is inlined into the agent's system prompt via `skill.WrapSkillContent`. This gives subagents domain knowledge without requiring them to discover and invoke skills during execution.

## Motivation

### Current State

Skills are loaded on-demand via the `skill` tool. An agent sees available skills listed in the tool description, decides to invoke the tool, waits for permission, and receives the skill content as a tool response:

```go
// internal/llm/tools/skill.go — runtime skill loading
func (s *skillTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
    skillInfo, err := skill.Get(params.Name)
    // ... permission check, substitution, wrapping ...
    return WithResponseMetadata(NewTextResponse(sb.String()), metadata), nil
}
```

This creates problems:

1. **Wasted turns for predictable skills**: When you know a subagent always needs a specific skill (e.g., a code-reviewer agent always needs the `review` skill), forcing it to discover and load the skill wastes a tool-call turn and consumes tokens on the discovery/loading exchange.
2. **Subagents lack inherited context**: Subagents don't inherit the parent conversation's loaded skills. A parent agent that loaded `composer-domain-expertise` cannot pass that knowledge to a spawned workhorse — the workhorse must re-discover and re-load it.
3. **No declarative domain binding**: There is no way to say "this agent is the domain expert on X" at the configuration level. The coupling between agent and skill only exists at runtime through probabilistic tool selection.

### Desired State

Agents declare skills in their configuration. The skills are injected into the system prompt at startup — no tool calls, no permission prompts, no wasted turns:

```yaml
# .opencode/agents/reviewer.md
---
name: Code Reviewer
description: Reviews code for quality and best practices
mode: subagent
skills:
  - review
  - composer-domain-expertise
tools:
  bash: false
  edit: false
---

You are a code review specialist...
```

```json
// .opencode.json
{
  "agents": {
    "reviewer": {
      "skills": ["review", "composer-domain-expertise"]
    }
  }
}
```

The agent's system prompt includes the full skill content wrapped in `<skill_content>` tags, identical to what the skill tool would return — so the agent recognizes the content as already-loaded and does not re-invoke the skill tool.

## Research Findings

### How the Skill Tool Wraps Content

The skill tool in `internal/llm/tools/skill.go` wraps loaded content in `<skill_content>` tags with additional metadata (base directory, bundled files). The `skill.WrapSkillContent` function in `internal/skill/skill.go` provides the minimal wrapping:

```go
func WrapSkillContent(name, content string) string {
    return fmt.Sprintf("<skill_content name=%q>\n%s\n</skill_content>", name, content)
}
```

The tool's richer wrapping also includes base directory and sampled files:

```go
fmt.Fprintf(&sb, "<skill_content name=%q>\n", skillInfo.Name)
fmt.Fprintf(&sb, "Base directory for this skill: %s\n\n", baseDir)
sb.WriteString(processedContent)
// ... bundled files ...
sb.WriteString("</skill_content>")
```

**Key finding**: For preloaded skills, we should use `WrapSkillContent` (minimal wrapping) rather than the full tool output format. Preloaded skills provide static domain knowledge — they don't need base directory info or file sampling, and there are no `$ARGUMENTS` to substitute.

**Implication**: The agent sees `<skill_content name="...">` in its system prompt and the skill tool description already says _"If you see a `<skill_content>` tag in the current conversation turn, the skill has ALREADY been loaded — follow the instructions directly instead of calling this tool again"_. No additional instruction is needed.

### Permission at Prompt Build Time

`GetAgentPrompt` in `internal/llm/prompt/prompt.go` runs at prompt assembly time with no interactive user session. It calls `agentregistry.GetRegistry()` and accesses agent info, but has no access to the `permission.Service` for interactive prompts.

The registry's `EvaluatePermission(agentID, toolName, input)` returns one of three actions:
- `ActionAllow` — proceed
- `ActionDeny` — block
- `ActionAsk` — needs interactive prompt (unavailable at build time)

**Key finding**: Listing a skill in an agent's `skills` array is explicit user intent to grant that skill. At prompt build time, only an explicit `ActionDeny` should block injection. Both `ActionAllow` and `ActionAsk` (whether from default fallback or an explicit rule) should result in the skill being injected. The reasoning: the user deliberately added the skill to the agent definition — that is a stronger signal than a generic permission pattern.

**Implication**: Only `ActionDeny` blocks a preloaded skill. Users who want to prevent a preloaded skill from loading must explicitly deny it in permissions.

### Merge Semantics Across Configuration Layers

The agent registry applies configuration in priority order: builtins -> global markdown -> project markdown -> JSON config. For the `Skills` field, we need to decide how layers merge.

| Strategy | Behavior | Complexity |
|----------|----------|------------|
| Replace | Higher-priority source replaces entire list | Simple, predictable |
| Union | Lists are merged, deduped | Can accumulate unwanted skills |
| Append | Higher-priority source appends to lower | Ordering becomes confusing |

**Key finding**: The existing `Prompt` field uses replace semantics — a higher-priority source completely overrides the lower one. Most other fields (`Name`, `Description`, `Color`) also use replace.

**Implication**: Replace semantics align with existing patterns and are the simplest to reason about.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Field type | `[]string` on both `AgentInfo` and `config.Agent` | Simple, matches existing patterns for list fields |
| Wrapping function | `skill.WrapSkillContent` (minimal) | No args substitution, no file sampling needed for static domain knowledge; keeps prompt clean |
| Permission at build time | Inject unless `ActionDeny`; skip only on explicit deny | Listing skill in agent definition is explicit user intent; only deny blocks it |
| Tool enablement bypass | Preloaded skills bypass `IsToolEnabled` check | `tools:{"skill": false}` disables runtime loading; preloaded skills are independent of the tool |
| Merge semantics | Replace (non-nil overlay replaces entirely) | Matches `Prompt` field behavior; predictable |
| Deduplication | At registration time, warn on duplicates | Fail-fast catches misconfigurations; log helps debugging |
| Skill not found | Log warning, skip | Don't block agent startup; skill may not exist in all environments |
| Injection order | Sorted alphabetically by skill name | Deterministic prompt output across runs |
| Prompt position | After base prompt + structured output + parallel tool use, before environment info | Skill knowledge is part of agent's core instructions, not environment context |
| Shell markup expansion | Skipped for preloaded skills | Preloaded skills are static context; shell commands should only run at invocation time |
| Size warning | Log warning if total preloaded content exceeds 200KB | Prevents silent context bloat; no hard cap since user may have large context models |

## Architecture

### Data Flow

```
STEP 1: Agent registration (registry.go)
──────────────────────────────────────────
AgentInfo.Skills populated from:
  - Builtin definitions (hardcoded in registerBuiltins)
  - Markdown frontmatter (yaml: skills)
  - JSON config overrides (json: skills)

Deduplication applied at each merge step.
Duplicates logged as warnings.

STEP 2: Prompt assembly (prompt.go — GetAgentPrompt)
─────────────────────────────────────────────────────
For each skill name in AgentInfo.Skills (sorted):
  1. reg.EvaluatePermission(agentID, "skill", skillName)
     → ActionDeny?  Skip, log debug.
     → ActionAllow/ActionAsk?  Continue (user intent from skills list).
  2. skill.Get(skillName)
     → Found?  Continue.
     → Not found?  Log warning, skip.
  3. Append skill.WrapSkillContent(name, content) to prompt.
  After loop: warn if total injected content exceeds 200KB.

STEP 3: Agent receives prompt
──────────────────────────────
System prompt contains <skill_content> blocks.
Skill tool description says "already loaded, don't re-invoke".
Agent uses the knowledge directly.
```

### AgentInfo with Skills

```
┌─────────────────────────────────────────┐
│              AgentInfo                   │
│  ├── ID:              string            │
│  ├── Name:            string            │
│  ├── ...                                │
│  ├── Skills:          []string  ← NEW   │
│  ├── Permission:      map[string]any    │
│  ├── Tools:           map[string]bool   │
│  └── ...                                │
└─────────────────────────────────────────┘
         │
         │ Skills: ["review", "composer-domain-expertise"]
         │
         ▼  GetAgentPrompt
┌─────────────────────────────────────────┐
│           System Prompt                  │
│                                          │
│  [base prompt]                           │
│  [structured output instructions]        │
│  [parallel tool use]                     │
│  [preloaded skills]          ← NEW       │
│    <skill_content name="...">            │
│      ...skill markdown...                │
│    </skill_content>                      │
│  [environment info]                      │
│  [LSP info]                              │
│  [project context]                       │
└──────────────────────────────────────────┘
```

## Implementation Plan

### Phase 1: Add Skills field to data structures

- [x] **1.1** Add `Skills []string` to `config.Agent` in `internal/config/config.go` with JSON tag `json:"skills,omitempty"`
- [x] **1.2** Add `Skills []string` to `AgentInfo` in `internal/agent/registry.go` with YAML tag `yaml:"skills,omitempty"`
- [x] **1.3** Add `deduplicateSkills(skills []string) []string` helper in `registry.go` — returns unique names preserving order, logs warning on duplicates found

### Phase 2: Wire skills through agent discovery and merge

- [x] **2.1** Update `mergeMarkdownIntoExisting` in `registry.go` — if markdown agent has non-nil `Skills`, replace existing skills entirely, then deduplicate
- [x] **2.2** Update `applyConfigOverrides` in `registry.go` — if config agent has non-nil `Skills`, replace existing skills entirely, then deduplicate
- [x] **2.3** Log discovered skills per agent in `newRegistry` alongside existing tools/permissions logging

### Phase 3: Inject preloaded skills in prompt assembly

- [x] **3.1** Import `internal/skill` and `internal/permission` in `internal/llm/prompt/prompt.go`
- [x] **3.2** Add `appendPreloadedSkills(agentName string, reg Registry) string` function that:
  - Gets agent info from registry
  - Iterates `Skills` in sorted order
  - For each: checks `EvaluatePermission(agentID, "skill", skillName)` — skips only on `ActionDeny`
  - For each: calls `skill.Get(name)` — skips with warning log if not found
  - Appends `skill.WrapSkillContent(name, info.Content)` to prompt
  - After loop, logs warning if total injected content exceeds 200KB
- [x] **3.3** Call `appendPreloadedSkills` in `GetAgentPrompt` after parallel tool use block, before environment info block

### Phase 4: Tests

- [x] **4.1** Unit test `deduplicateSkills` — empty, no dupes, with dupes, preserves order
- [x] **4.2** Unit test merge behavior — markdown replaces skills, config replaces skills, nil preserves existing
- [x] **4.3** Unit test prompt injection — skill allowed and found, skill denied (skipped), skill with "ask" permission (injected), skill not found (skipped with warning), empty skills list, default permission allows
- [x] **4.4** Integration: verify `<skill_content>` tag appears in assembled prompt for an agent with preloaded skills

### Phase 5: Schema and documentation

- [x] **5.1** Regenerate `opencode-schema.json` via `go run cmd/schema/main.go` (includes `skills` field in agent definition)
- [x] **5.2** Update CLAUDE.md agent configuration section to document the `skills` field
- [x] **5.3** Update `docs/skills.md` with a "Preloaded Skills" section covering usage, permissions, and limitations

## Edge Cases

### Skill listed but not found in registry

1. Agent config declares `skills: ["nonexistent-skill"]`
2. `skill.Get("nonexistent-skill")` returns `ErrSkillNotFound`
3. Log warning: `"Preloaded skill not found, skipping"` with agent ID and skill name
4. Agent starts normally without that skill

### Skill listed but permission is "ask"

1. Agent config declares `skills: ["internal-docs"]`, global permission has `"internal-*": "ask"`
2. `EvaluatePermission` returns `ActionAsk`
3. Skill is still injected — listing it in `skills` is explicit user intent, overriding the generic "ask" pattern
4. If user truly wants to block preloading, they must set explicit `deny`

### Skill listed but permission is "deny"

1. Agent config declares `skills: ["secret-ops"]`, permission has `"secret-*": "deny"`
2. `EvaluatePermission` returns `ActionDeny`
3. Skill is skipped, debug log emitted
4. Agent can still attempt to load it at runtime via the skill tool (which will also be denied)

### Duplicate skill names in configuration

1. Agent markdown declares `skills: ["review", "review", "commit"]`
2. `deduplicateSkills` removes second `"review"`, logs warning
3. Final list: `["review", "commit"]`

### Skill preloaded AND invoked via tool

1. Agent has `skills: ["review"]` — injected into system prompt
2. Agent sees `<skill_content name="review">` in prompt
3. Skill tool description says "do not invoke a skill that is already loaded"
4. If agent still calls the skill tool with `name: "review"`, it gets the same content again (wasted turn but not harmful)

### Skill tool disabled but preloaded skills configured

1. Agent has `skills: ["review"]` and `tools: {"skill": false}`
2. The skill tool is disabled — agent cannot discover or load skills at runtime
3. Preloaded skills bypass `IsToolEnabled`; they check only permission patterns
4. `"review"` is still injected into the system prompt
5. This is the intended way to lock an agent to a fixed set of skills

### Large skills consuming context

1. Agent preloads 5 skills, each 50KB
2. 250KB of skill content in system prompt
3. A warning is logged when total exceeds 200KB threshold
4. No hard cap — user may have a model with a large context window

### Skills field merge across layers

1. Builtin agent has `Skills: ["a", "b"]`
2. Markdown override has `Skills: ["c"]`
3. Result: `["c"]` (replace semantics, not union)
4. To keep "a" and "b", markdown must list them explicitly: `skills: ["a", "b", "c"]`

## Open Questions

1. ~~**Should we add a total size warning for preloaded skills?**~~ **Resolved**: Yes. Log a warning if total preloaded content exceeds 200KB. No hard cap.

2. **Should preloaded skills expand shell markup (`!`command``)?**
   - The skill tool expands `!`command`` syntax at runtime via `shell.ExpandMarkup`.
   - At prompt build time, expanding shell commands would execute them during startup.
   - **Recommendation**: No. Preloaded skills provide static domain knowledge. Shell markup should only run when a user explicitly invokes the skill via the tool. Document this limitation.

3. **Should preloaded skills substitute `${SKILL_DIR}` and `${SESSION_ID}` variables?**
   - At prompt build time there is no session ID, and skill directory may not be meaningful as static context.
   - **Recommendation**: No substitution. Raw content only, wrapped via `WrapSkillContent`. If a skill relies heavily on variable substitution, it should be loaded via the tool, not preloaded.

## Success Criteria

- [ ] `AgentInfo` has a `Skills []string` field that is populated from builtins, markdown frontmatter, and JSON config
- [ ] Duplicate skill names within a single agent are deduplicated with a warning
- [ ] `GetAgentPrompt` injects `<skill_content>` blocks for each allowed, found skill
- [ ] Only skills with `ActionDeny` permission are skipped; `ActionAsk` is treated as allow for preloaded skills
- [ ] Skills not found in the registry are skipped with a warning log
- [ ] Injection order is deterministic (sorted by name)
- [ ] Existing skill tool behavior is unchanged — preloaded skills don't break runtime loading
- [ ] Warning logged when total preloaded skill content exceeds 200KB
- [ ] `make test` passes
- [ ] Schema is regenerated

## References

- `internal/agent/registry.go` — `AgentInfo` struct, agent discovery, merge logic, permission evaluation
- `internal/config/config.go:75` — `Agent` struct (JSON config counterpart)
- `internal/llm/prompt/prompt.go:71` — `GetAgentPrompt` function (prompt assembly)
- `internal/skill/skill.go:87` — `Get()`, `WrapSkillContent()` functions
- `internal/llm/tools/skill.go` — Skill tool implementation (runtime loading, permission checks, wrapping format)
- `internal/permission/evaluate.go` — `EvaluateToolPermission`, `Action` type
- `spec/20260202T105813-skills-feature.md` — Original skills feature spec
- `spec/20260208T150000-agent-registry-and-permissions-refactor.md` — Agent registry and permissions spec
