## Why

A workspace that loads many teams' skills and subagent types puts every one of their descriptions into every agent that has the `skill` or `task` tool. In Piano's unified developer workspace (13 team repos) the skill listing alone was measured at about 22k tokens per request (GENAI-257), and the `task` tool lists every subagent type with its full description. GENAI-257 made the skill listing follow each agent's own `permission.skill` rules, but shared agent types serve every team, so a per-type rule cannot trim them. A flow run, however, knows what it is for: its args name the team (or any other context), so the listings can be scoped per run. This is GENAI-696.

Nothing may become unreachable: an agent that discovers mid-task that it needs another team's skill or subagent must still be able to load or call it by name.

## What Changes

- New `listingScope` block in `.opencode.json` with two optional lists, `skills` and `subagents`. Each entry is a path glob, relative to the working directory, that may contain flow-arg tokens such as `${args.team}` (for example `".agents/skills/**"`, `"${args.team}/skills/**"`).
- In a flow run, each step agent resolves those patterns against the run's live args. When every token in a list resolves, that list scopes the step agent's listing:
  - Skills and subagent types whose file location matches a pattern are listed in full, as today.
  - The others are listed **by name only**, in a compact list after the full entries, with a note that any of them can still be loaded or called by name. This mirrors how deferred tool names are announced.
- A list with an unresolved token (for example, no `team` arg yet) leaves that listing unchanged. Scoping fails open.
- Subagents spawned by a scoped step, synchronously or with `async: true`, inherit the step's resolved scope.
- Items with no file location (agents defined in `.opencode.json`) are always listed in full.
- Scope affects listings only. `skill` and `task` still accept every name the agent is permitted to use, and permission rules keep deciding what is listed at all. A denied item never appears, not even by name.
- Sessions outside a flow run (chat, daemons, cron, bridge sessions) and steps on a pinned primary prefix keep today's listings.
- One log line per scoped agent build records the in-scope and names-only counts, so the savings can be measured.

Non-goals: deferred skill search (`skillsearch`, GENAI-257 proposal 4); step-level overrides of the scope; the dynamic-flow planner's own subagent list; any change to permissions.

## Capabilities

### New Capabilities
- `run-scoped-listings`: config surface, per-run resolution and inheritance, and the two-tier rendering of the `skill` tool's `<available_skills>` listing and the `task` tool's subagent list, with names-only entries that stay loadable and callable.

### Modified Capabilities
<!-- None: no existing requirement changes. Unscoped runs keep byte-identical listings. -->

## Impact

- `internal/config` (new `ListingScope` struct), `cmd/schema/main.go` and the regenerated `opencode-schema.json`.
- `internal/flow/service.go`: resolve the scope per step and carry it on the step context, next to `denyTools`.
- `internal/llm/tools`: context helpers for the resolved scope, and two-tier rendering in `skill.go`.
- `internal/llm/agent`: the factory's step-cache key, toolset construction, the `task` tool's listing in `agent-tool.go`, and scope propagation in `agent-tool-async.go`.
- Docs: `docs/skills.md`, `docs/flows.md`.
- No API, database or wire-format change. The consumer is piano-developer's `.agents.opencode.json`, in a separate workspace merge request under GENAI-696.
