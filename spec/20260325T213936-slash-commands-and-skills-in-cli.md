# Slash Commands & Skills in Non-Interactive Mode + Skill Slash Invocation

**Date**: 2026-03-25
**Status**: Draft
**Author**: AI-assisted

## Overview

Enable slash commands (`/commit`, `/review fix login bug`) and skill invocation (`/skill:git-release`) in non-interactive mode (`opencode -p "/commit"`) and as slash commands in the TUI. Add `argument-hint` metadata to both commands and skills so that argument dialogs show meaningful placeholder text instead of generic "Enter value for $NAME...". Add `user-invocable` metadata field to skills to control which skills appear as slash commands.

## Motivation

### Current State

```bash
# Non-interactive mode passes the raw string to the agent as a literal prompt
opencode -p "/commit"
# → Agent receives "/commit" as plain text, doesn't execute the commit command
```

```go
// cmd/flow.go — runNonInteractive sends prompt directly to agent.Run()
done, err := a.ActiveAgent().Run(ctx, sess.ID, prompt)
```

```go
// Skills are only accessible via the LLM's skill tool, not via slash commands
// The user cannot type /skill:git-release in the editor
```

```go
// Argument placeholders show generic text
ti.Placeholder = fmt.Sprintf("Enter value for %s...", name)
// Shows: "Enter value for COMPONENT_NAME..."
// Could show: "Enter component name (e.g. auth-service)..."
```

Problems:

1. **Slash commands don't work in `-p` mode**: `/commit`, `/review main`, etc. are treated as literal text sent to the agent. Users expect `opencode -p "/commit"` to behave like typing `/commit` in the TUI.
2. **Skills can't be invoked via slash**: Unlike commands, skills have no slash-command entry point. Users must rely on the agent deciding to call the skill tool. Claude Code supports `/skill:name` syntax.
3. **No opt-in for user invocability**: All skills are agent-only — there's no way for a skill author to mark a skill as user-invocable. Claude Code has a `user_invocable` metadata field (defaulting to `true`); we need the same concept but with a safer default of `false` since most skills are agent instructions, not user-facing prompts.
4. **No argument hints**: When a command or skill has `$ARGUMENTS` placeholders, the dialog shows generic placeholders. There's no way for command/skill authors to provide example values or human-readable labels.

### Desired State

```bash
# Slash commands work in non-interactive mode
opencode -p "/commit"          # executes the commit command prompt
opencode -p "/review main"     # executes review with "main" as $ARGUMENTS

# Skills invocable via slash in both modes
opencode -p "/skill:git-release v2.1.0"
# In TUI: type /skill:git-release → loads skill content into prompt
```

```yaml
# argument-hint in command frontmatter
---
title: Review Code
description: Reviews code changes
argument-hint: "[commit-hash-or-branch]"
---
Review the changes in $ARGUMENTS
```

```yaml
# argument-hint and user-invocable in skill frontmatter
---
name: migrate-component
description: Migrate a UI component between frameworks
user-invocable: true
argument-hint: "[component-name] [source-framework] [destination-framework]"
---
```

## Research Findings

### Claude Code Slash Command Behavior

| Feature | Claude Code | OpenCode (current) | OpenCode (proposed) |
|---|---|---|---|
| `/command` in CLI `-p` | Recognized and dispatched | Treated as literal text | Parsed and dispatched |
| `/command args` in CLI | Args passed to command | N/A | Args substituted into `$ARGUMENTS` |
| Skill invocation via slash | `/skill:name` supported | Not supported | `/skill:name` with opt-in |
| `user_invocable` metadata | Default `true` | N/A | `user-invocable`, default `false` |
| Argument hints | Not supported | Not supported | `argument-hint` in frontmatter |
| Built-in commands in CLI | All work via `-p` | None work | Prompt-producing commands work |

**Key finding**: Claude Code parses the prompt string for a leading `/` and resolves it against the command registry before dispatching. Arguments after the command name are passed directly.

**Implication**: We need a prompt-parsing layer that sits before `agent.Run()` in both interactive and non-interactive paths.

### Current Slash Resolution in TUI

The TUI has two paths for slash commands:
1. **`Ctrl+K` command palette** — opens a fuzzy-search dialog, fires `CommandSelectedMsg`
2. **`/` in editor** — opens completion dropdown, fires `CompletionSelectedMsg` → `CommandSelectedMsg`

Both paths end at `CommandSelectedMsg` → `cmd.Handler(cmd)` → either `CommandRunCustomMsg` (direct) or `ShowMultiArgumentsDialogMsg` (parameterized).

Neither path handles inline arguments (e.g., `/review main`). The user must select the command first, then fill in arguments via the dialog.

### Skill Discovery vs Command Discovery

| Aspect | Commands | Skills |
|---|---|---|
| Identity | File path → ID with `user:`/`project:` prefix | Directory name = skill name |
| Invocation | Slash `/` or `Ctrl+K` | Agent's `skill` tool only |
| Arguments | `$NAME` placeholders in content | None (content is instruction text) |
| Completions | In command completion provider | Not in any completion provider |
| User-invocable | Always (commands are user-facing by definition) | Not supported (agent-only) |

### Claude Code `user_invocable` Behavior

Claude Code supports a `user_invocable` field in skill metadata. When `true`, the skill appears in the slash command list and can be triggered by the user directly. When `false`, it is only available to the agent via the skill tool.

| Aspect | Claude Code | OpenCode (proposed) |
|---|---|---|
| Field name | `user_invocable` | `user-invocable` (YAML convention) |
| Default value | `true` | `false` |
| Effect when `true` | Skill appears in `/` slash list | Skill appears in `/skill:name` completion and is resolvable via slash |
| Effect when `false` | Agent-only, hidden from slash | Agent-only, hidden from slash (default) |

**Rationale for defaulting to `false`**: Most skills are agent-level instruction sets (coding patterns, review checklists, domain knowledge) that don't make sense as user-invocable prompts. Requiring explicit opt-in prevents the slash command list from being cluttered with irrelevant skills. Claude Code defaults to `true` because their skill ecosystem is more user-prompt oriented; ours is more agent-instruction oriented.

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Slash parsing location | New `slashcmd` package under `internal/` | Reusable by both TUI and CLI paths; keeps parsing logic testable and independent |
| Command resolution | Match against command registry by ID (without prefix) | `/commit` should match `commit` built-in; `/user:deploy` should match exactly |
| Skill slash syntax | `/skill:name` prefix | Clear namespace separation from commands; matches Claude Code pattern |
| Inline arguments | Text after command/skill name becomes `$ARGUMENTS` | Simple, consistent with how `/review main` should work |
| Argument hint metadata | `argument-hint` field in frontmatter | Declarative, optional, backwards-compatible |
| Non-interactive args | Inline only (no modal dialog) | No TTY available; arguments must be provided in the prompt string |
| TUI inline args | If `/command args` typed in editor, skip dialog and substitute directly | Faster workflow when user already knows the arguments |
| Skill as slash command | Load skill content and prepend to prompt sent to agent | Skills are instruction sets, not direct prompts — they augment the agent's context |
| Skill user-invocable gate | `user-invocable: true` in skill frontmatter (default `false`) | Safe default — most skills are agent instructions, not user-facing prompts. Claude Code defaults `true`; we invert because our skill ecosystem is agent-oriented |
| Priority for ambiguous names | Commands take precedence over `skill:` prefix | Commands are the primary slash namespace |
| `argument-hint` parsing | Parse bracket groups from hint string into ordered arg names and examples | `[component] [framework]` → two args with example placeholders |

## Architecture

### Slash Command Parsing Flow

```
Input: "/review main"
         │
         ▼
    slashcmd.Parse(input)
         │
         ├── prefix: "/"
         ├── name: "review"
         ├── args: "main"
         └── isSkill: false
         │
         ▼
    slashcmd.Resolve(parsed, commands, skills)
         │
         ├── Match against command IDs (strip user:/project: prefix)
         │   └── Found "review" → Command
         │       └── Substitute args into $ARGUMENTS
         │
         ├── If name starts with "skill:"
         │   └── Match against skill registry (user-invocable skills only)
         │       ├── Found AND user-invocable=true → Skill
         │       └── Found BUT user-invocable=false → NotFound (skill exists but not user-invocable)
         │
         └── No match → return as literal text
```

### Non-Interactive Dispatch

```
opencode -p "/commit"
         │
         ▼
    cmd/root.go: prompt = "/commit"
         │
         ▼
    runNonInteractive(ctx, app, prompt, ...)
         │
         ▼
    slashcmd.Parse(prompt)
         │
         ├── IsSlashCommand = true
         │   ├── command found?
         │   │   └── YES → execute command content as prompt
         │   │       (with $ARGUMENTS substituted from inline args)
         │   │
         │   ├── skill found? (skill: prefix, user-invocable=true)
         │   │   └── YES → load skill content, prepend to remaining args,
         │   │       send combined text as prompt
         │   │
         │   └── NO match → send original text as literal prompt
         │
         └── IsSlashCommand = false
             └── send as literal prompt (current behavior)
```

### TUI Inline Argument Flow

```
User types: /review main↵
         │
         ▼
    Editor sends full text (current: removed by completion)
         │
    NEW: Before sending, check if text starts with "/"
         │
         ├── Parse slash command
         ├── Resolve against commands/skills
         ├── If command found AND has inline args:
         │   └── Substitute $ARGUMENTS with inline args
         │       └── Skip argument dialog
         │       └── Send as CommandRunCustomMsg
         │
         ├── If command found AND no inline args AND has $placeholders:
         │   └── Show argument dialog (existing behavior)
         │
         ├── If skill found (user-invocable=true):
         │   └── Load skill content
         │   └── Prepend to any inline args
         │   └── Send as prompt to agent
         │
         └── No match → send as literal text to agent
```

### Argument Hint Integration

```
Command frontmatter:
---
title: Review Code
argument-hint: "[commit-or-branch] [focus-area]"
---

Skill frontmatter:
---
name: migrate-component
description: Migrate a UI component
argument-hint: "[component-name] [source-framework] [destination-framework]"
---

         │
         ▼
    Parse argument-hint: split on bracket groups
         │
         ├── "[commit-or-branch]" → arg name: COMMIT_OR_BRANCH, hint: "e.g. commit-or-branch"
         ├── "[focus-area]"       → arg name: FOCUS_AREA, hint: "e.g. focus-area"
         │
         ▼
    MultiArgumentsDialogCmp uses hints as placeholder text
    ti.Placeholder = "commit-or-branch"  (instead of "Enter value for ARGUMENTS...")
```

### File Structure

```
internal/
    slashcmd/
        ├── parse.go          ← Parse("/review main") → ParsedCommand
        ├── parse_test.go
        ├── resolve.go        ← Resolve(parsed, commands, skills) → ResolvedAction
        └── resolve_test.go
    tui/
        components/
            dialog/
                ├── custom_commands.go   ← add argument-hint parsing from frontmatter
                └── arguments.go         ← use hints for placeholder text
            chat/
                └── editor.go            ← intercept /command before send()
        page/
            └── chat.go                  ← handle resolved slash commands
    skill/
        └── skill.go                     ← add argument-hint to Info struct
    completions/
        └── commands.go                  ← include skills in completion provider
cmd/
    └── flow.go                          ← parse slash commands before agent.Run()
```

## Implementation Plan

### Phase 1: Slash Command Parser

- [ ] **1.1** Create `internal/slashcmd/parse.go`:
  - `ParsedCommand` struct: `Name string`, `Args string`, `IsSkill bool`, `Raw string`
  - `Parse(input string) *ParsedCommand` — returns nil if not a slash command
  - Handle `/name`, `/name args`, `/skill:name`, `/skill:name args`
- [ ] **1.2** Create `internal/slashcmd/resolve.go`:
  - `ResolvedAction` struct with variants: `CommandAction`, `SkillAction`, `NotFound`
  - `Resolve(parsed, commands, skills)` — match against registries
  - Command matching: try exact ID, then strip `user:`/`project:` prefix
- [ ] **1.3** Write comprehensive tests for parser and resolver

### Phase 2: Non-Interactive Slash Dispatch

- [ ] **2.1** Add slash command resolution in `runNonInteractive` before `agent.Run()`:
  - Load commands via `dialog.LoadCustomCommands()` + built-in commands
  - Load skills via `skill.All()`
  - Parse prompt, resolve, substitute `$ARGUMENTS` with inline args
  - For skills: load skill content, combine with args, send as prompt
- [ ] **2.2** Handle edge cases: no match falls through to literal prompt, empty args with required placeholders

### Phase 3: Skill Slash Commands in TUI

- [ ] **3.1** Add `UserInvocable bool` field to `skill.Info` struct (`user-invocable` YAML key, default `false`)
- [ ] **3.2** Filter skills by `UserInvocable == true` when building the slash completion list
- [ ] **3.3** Add skills to the completion provider — create `NewSkillCompletionProvider` or extend command completion to include `skill:name` entries (only user-invocable skills)
- [ ] **3.4** Handle `/skill:name` selection in `chatPage` — load skill content via `skill.Get()`, verify `UserInvocable`, send as prompt (with args if provided)
- [ ] **3.5** Support inline arguments for skills in the editor — parse `/skill:name arg1 arg2` and pass args along with skill content
- [ ] **3.6** In resolver: if a skill exists but `UserInvocable` is `false`, return a clear error ("skill 'name' is not user-invocable, set `user-invocable: true` in its SKILL.md frontmatter")

### Phase 4: Argument Hints

- [ ] **4.1** Add `ArgumentHint string` field to `commandFrontmatter` struct (`argument-hint` YAML key)
- [ ] **4.2** Add `ArgumentHint string` field to `skill.Info` struct
- [ ] **4.3** Parse `argument-hint` bracket groups into hint strings for each `$PLACEHOLDER`
- [ ] **4.4** Pass hints through `ShowMultiArgumentsDialogMsg` to `MultiArgumentsDialogCmp`
- [ ] **4.5** Use hints as `textinput.Placeholder` values instead of generic text
- [ ] **4.6** For skills invoked via slash with `argument-hint`: if skill content contains `$PLACEHOLDERS`, show argument dialog with hints

### Phase 5: TUI Inline Argument Shortcut

- [ ] **5.1** In the editor or chat page, before `send()`: detect `/command args` pattern
- [ ] **5.2** If the command has `$ARGUMENTS` placeholder and inline args are present, substitute directly without showing the dialog
- [ ] **5.3** If the command has multiple named `$PLACEHOLDERS` (not just `$ARGUMENTS`), still show the dialog (inline shortcut only works for single-arg `$ARGUMENTS` pattern)

## Edge Cases

### No Matching Command or Skill

1. User types `opencode -p "/nonexistent"`
2. No command or skill matches "nonexistent"
3. Send "/nonexistent" as literal text to the agent (backwards-compatible)

### Command Name Collision with Skill

1. A command `deploy` exists AND a skill `deploy` exists (with `user-invocable: true`)
2. `/deploy` matches the command (commands take priority)
3. `/skill:deploy` explicitly targets the skill
4. Both appear in the completion dropdown with clear labels

### Skill Exists but Not User-Invocable

1. User types `/skill:internal-codestyle` but the skill has `user-invocable: false` (or omitted, since default is `false`)
2. Resolver returns a specific error: "skill 'internal-codestyle' is not user-invocable"
3. In non-interactive mode: print error and exit
4. In TUI: skill does not appear in completion dropdown at all
5. The skill remains fully accessible to the agent via the `skill` tool

### Slash Command with No Arguments but Placeholders

1. `opencode -p "/review"` — the review command has `$ARGUMENTS`
2. No inline args provided, non-interactive mode (no TTY)
3. Substitute `$ARGUMENTS` with empty string (the agent will figure out context from git state)

### Skill with argument-hint but No `$PLACEHOLDER` in Content

1. Skill has `argument-hint: "[version]"` but no `$VERSION` in its markdown
2. The hint text is informational only — inline args are appended after the skill content
3. No dialog shown since there are no placeholders to fill

### Multiple Arguments in Non-Interactive Mode

1. `opencode -p "/review main --focus security"`
2. Everything after the command name is `$ARGUMENTS`: `"main --focus security"`
3. Single substitution into `$ARGUMENTS`, no splitting

### Built-in Commands in Non-Interactive Mode

1. `/compact` triggers session compaction — this is a TUI action, not a prompt
2. `/agents` navigates to a TUI page — not meaningful in CLI
3. Only commands that produce a prompt (like `/commit`, `/review`, `/init`) should work in non-interactive mode
4. TUI-only commands should return an error message in CLI mode: "Command 'compact' is only available in interactive mode"

### Argument Dialog with Hints from argument-hint

1. Command has `argument-hint: "[branch] [reviewer]"` and content has `$BRANCH` and `$REVIEWER`
2. Dialog shows two inputs with placeholders "branch" and "reviewer"
3. Mapping: bracket groups are matched to `$PLACEHOLDER` names by position in the hint string

## Open Questions

1. **Should `/skill:name` load the skill content as a system-level instruction or as user message content?**
   - Option A: Prepend skill content to the user's message as a user message
   - Option B: Inject as a system message/context (like the skill tool does)
   - **Recommendation**: Option A — send as user message with skill content prepended. This is simpler and doesn't require system prompt modification at runtime. The agent will see the skill instructions and follow them.

2. **How should argument-hint map to `$PLACEHOLDER` names?**
   - Option A: Positional — first bracket group maps to first `$PLACEHOLDER` found in content
   - Option B: Name-based — `[branch]` maps to `$BRANCH` (case-insensitive, hyphen→underscore)
   - **Recommendation**: Option B — name-based mapping is more explicit and doesn't break when placeholder order changes. `[commit-hash]` → matches `$COMMIT_HASH`.

3. **Should skills support `$PLACEHOLDER` arguments in their content?**
   - Currently skills are pure instruction text with no parameterization
   - Adding `$PLACEHOLDER` support would unify the argument system across commands and skills
   - **Recommendation**: Yes, support it. Skills with `$PLACEHOLDERS` show the argument dialog when invoked via slash, same as commands. This makes skills and commands consistent.

4. **Should `user-invocable` be a top-level YAML field or nested under `metadata`?**
   - Option A: Top-level field in frontmatter (`user-invocable: true`) — simple, discoverable
   - Option B: Under metadata (`metadata: { user-invocable: true }`) — keeps frontmatter minimal, metadata is already a map
   - **Recommendation**: Option A — top-level field. It's a first-class behavioral flag, not arbitrary metadata. Matches how `name`, `description`, `license` are top-level. The `metadata` map is for custom/unstructured data.

5. **Should non-interactive mode show an error for TUI-only commands or silently ignore them?**
   - **Recommendation**: Return an error: `"command 'compact' is only available in interactive mode"`. Silent failure is confusing.

## Success Criteria

- [ ] `opencode -p "/commit"` executes the commit command prompt
- [ ] `opencode -p "/review main"` executes review with "main" substituted for `$ARGUMENTS`
- [ ] `opencode -p "/skill:git-release v2.1.0"` loads the skill and sends with args (skill must have `user-invocable: true`)
- [ ] `opencode -p "/skill:agent-only-skill"` returns error when skill is not user-invocable
- [ ] Typing `/skill:` in TUI editor shows only user-invocable skills in completion dropdown
- [ ] Skills without `user-invocable: true` do not appear in slash completions but remain accessible via the agent's skill tool
- [ ] `argument-hint` in command frontmatter populates dialog placeholders
- [ ] `argument-hint` in skill frontmatter populates dialog placeholders
- [ ] Inline args in TUI (`/review main↵`) skip the argument dialog
- [ ] Unrecognized `/something` in `-p` mode falls through to literal prompt
- [ ] TUI-only commands return clear error in non-interactive mode
- [ ] `go build ./...` and `go vet ./...` pass
- [ ] `make test` passes with no regressions

## References

- `cmd/flow.go` — `runNonInteractive()` where slash parsing must be added
- `internal/tui/components/dialog/custom_commands.go` — Command loading and `ParameterizedCommandHandler`
- `internal/tui/components/dialog/arguments.go` — `MultiArgumentsDialogCmp` for hint integration
- `internal/tui/page/chat.go` — Slash completion handling and `sendMessage()`
- `internal/tui/tui.go` — `buildCommands()`, `CommandSelectedMsg` handling
- `internal/skill/skill.go` — Skill discovery and `Info` struct
- `internal/llm/tools/skill.go` — Skill tool for reference on how skills are loaded
- `internal/completions/commands.go` — Command completion provider
- `docs/custom-commands.md` — Current custom commands documentation
- `docs/skills.md` — Current skills documentation
