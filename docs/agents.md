# Agents

An agent is a model plus a system prompt, a tool set and permissions. OpenCode ships a few built-in agents, and you can override them or define your own in `.opencode.json` or as markdown files.

## Built-in agents

| Agent | Mode | Purpose |
|-------|------|---------|
| `coder` | agent | Main coding agent (all tools) |
| `hivemind` | agent | Supervisory agent for coordinating subagents |
| `explorer` | subagent | Fast codebase exploration (read-only tools) |
| `workhorse` | subagent | Autonomous coding subagent (all tools) |
| `summarizer` | subagent | Session summarization |
| `descriptor` | subagent | Session title generation (attended sessions only: non-interactive flow steps, headless `-p` runs, cron jobs that fire while no TUI or chat binding watches their session, and the subagents of all three keep their creation title) |

Primary agents (`mode: agent`) are switched with `Tab` in the TUI or picked with `opencode -a <id>`; subagents are invoked through the `task` tool.

## Agent fields

| Field | Description |
|-------|-------------|
| `model` | Model ID to use ([providers](providers.md)) |
| `maxTokens` | Maximum response tokens |
| `maxTurns` | Maximum tool-use turns per request (default 100; `--max-turns` or top-level `maxTurns` also set it) |
| `toolCallTimeoutSeconds` | Bound on one tool call in this agent's loop; inherits the top-level value (default 1200; 0 = off). `task`, `question` and `bash` are exempt |
| `reasoningEffort` | `low`, `medium`, `high` (default; `medium` on Claude Haiku 5.5), `xhigh`, `max` |
| `mode` | `agent` (primary, switchable via tab) or `subagent` (invoked via task tool) |
| `name` | Display name for the agent |
| `description` | Short description of agent's purpose |
| `hidden` | Hide the agent from the TUI switcher and subagent lists |
| `disabled` | Exclude the agent from the registry entirely |
| `permission` | Agent-specific permission overrides (supports granular glob patterns, see [permissions](configuration.md#permissions)) |
| `tools` | Tool deny-list — unmentioned tools stay enabled (e.g., `{"skill": false}`) |
| `allowTools` | Tool allow-list — the agent gets exactly these, nothing else (e.g., `["read", "gitlab_*"]`, [guide](tool-permissions.md)). Mutually exclusive with `tools` |
| `deferredTools` | On-demand tool loading — matching tools stay out of context until discovered via `toolsearch` (e.g., `{"jira_*": true}`, [guide](deferred-tools.md)) |
| `parallelToolUse` | Enable/disable parallel tool invocation if tool allows it |
| `skills` | Skills preloaded into the system prompt at startup ([guide](skills.md)) |
| `context` | Scoped context files for this agent ([guide](context.md)) |
| `color` | Badge color for subagent indication in TUI |
| `prompt` | Custom system prompt (mutually exclusive with `langfusePromptPath`) |
| `langfusePromptPath` | Path of a prompt in Langfuse Prompt Management to use as the system prompt ([below](#langfuse-managed-system-prompts)) |
| `langfusePromptLabel` | Langfuse label to resolve for `langfusePromptPath` (default `production`) |
| `structOutputSchemaDelivery` | Where the `struct_output` schema goes in the request: `message` (default) or `tool` ([guide](structured-output.md#where-the-schema-is-placed)) |
| `taskBudget` | Advisory token budget for the whole agentic loop (min 20,000), on models that support task budgets |
| `compactionThreshold` | Fraction of the context window at which this agent's sessions auto-compact ([guide](compaction.md#per-agent-tuning)) |
| `summarizerMaxInputTokens` | Cap on what this agent's compactions send the summarizer ([guide](compaction.md#per-agent-tuning)) |
| `selfCompact` | Opt-in `compact_session` tool so the agent can compact its own session ([guide](compaction.md#self-compaction)) |

## Custom agents via markdown

Define custom agents as markdown files with YAML frontmatter. Discovery locations (merge priority, lowest to highest):

1. `agentPaths` in `.opencode.json` — custom directories scanned for `*.md` agents (lowest priority)
2. `~/.config/opencode/agents/*.md` — Global agents
3. `~/.agents/types/*.md` — Global agents
4. `.opencode/agents/*.md` — Project agents
5. `.agents/types/*.md` — Project agents
6. `.opencode.json` `agents` config — Highest priority

`agentPaths` accepts absolute paths, `~` (home directory), and relative paths (resolved against the working directory). Each directory is scanned non-recursively for `*.md` files, mirroring the `skills.paths` option:

```json
{
  "agentPaths": ["~/.my-agents", ".team/agents"]
}
```

Example `.opencode/agents/reviewer.md`:

```markdown
---
name: Code Reviewer
description: Reviews code for quality and best practices
mode: subagent
model: vertexai.claude-opus-4-6
color: info
tools:
  bash: false
  write: false
---

You are a code review specialist...
```

The file basename (without `.md`) becomes the agent ID. Custom agents default to `subagent` mode.

Fields set in a higher-priority source override lower-priority ones. Note that a `.opencode.json` `agents.<id>` entry without `model` is given the default model and `maxTokens`, which replace the frontmatter values — so a JSON entry layered over a markdown agent must repeat them.

## Deny-list (`tools`) vs allow-list (`allowTools`)

`tools` is a **deny-list**: keys enable or disable named tools (wildcards
allowed), and anything unmentioned stays *enabled*. Convenient for a
general-purpose agent, but it means every tool added to the harness later is
granted to every existing agent retroactively.

`allowTools` is an **allow-list**: the agent gets exactly the tools it names
and nothing else.

```markdown
---
name: Scenario Runner
mode: subagent
allowTools:
  - struct_output
  - question
  - scenario-run_*
---
```

The two keys are mutually exclusive within one definition source. Full semantics, the migration checklist and the version footgun (older builds ignore the key silently) are in [tool-permissions.md](tool-permissions.md).

## Langfuse-managed system prompts

An agent's system prompt can live in [Langfuse Prompt Management](telemetry.md#langfuse-prompt-management)
instead of in the definition, so prompt changes ship from the Langfuse UI
with no deploy. In a markdown agent it is a frontmatter key, and the body —
which *is* the inline prompt — must be empty:

```markdown
---
name: Code Reviewer
description: Reviews code for quality and best practices
mode: subagent
langfusePromptPath: agents/reviewer/system
langfusePromptLabel: production   # optional; this is the default
---
```

The same two keys work in the `.opencode.json` `agents` block alongside
`prompt` (note the JSON schema requires `model` on an agent entry, so
restate it there even when only the prompt is changing):

```json
{
  "agents": {
    "reviewer": {
      "model": "vertexai.claude-sonnet-4-5-m",
      "langfusePromptPath": "agents/reviewer/system",
      "langfusePromptLabel": "staging"
    }
  }
}
```

Declaring both an inline prompt and a `langfusePromptPath` — a non-empty
markdown body counts — is a load-time error rather than a precedence rule.
Each definition layer is a partial override, so a higher-priority layer may
declare `langfusePromptLabel` alone to re-label a path a lower one supplied;
a label that ends up with no path anywhere is dropped with a warning rather
than failing the boot.

Resolution happens when the agent is constructed, not when the registry is
loaded. For subagents (built per `task` spawn) and flow-step agents that
means an edit in the Langfuse UI reaches the next run bounded by `cacheTTL`,
with no restart. **Primary agents (`mode: agent`) are built once at startup
and held for the process lifetime**, so their prompt is pinned until
restart — see the freshness table in
[telemetry.md](telemetry.md#freshness-what-a-ui-edit-reaches-and-when).

A reference that cannot be resolved and has nothing cached fails agent
construction naming the path; the agent never runs on an empty system prompt
and never silently falls back to the built-in prompt for its name.
