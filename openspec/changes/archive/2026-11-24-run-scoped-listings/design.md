## Context

See proposal.md (Why) and specs/run-scoped-listings/spec.md for the behaviour. The relevant current state:

- **Skill listing.** The `skill` tool is built per agent in `NewToolSet` and already carries the agent ID (GENAI-257). Its description is rebuilt in `Info()` on every request: `skill.All()`, then the per-agent `permission.skill` filter, then `renderAvailableSkills` with the description cap and the optional `maxListingChars` budget. Each `skill.Info` has a `Location`, the path of its `SKILL.md`.
- **Subagent listing.** The `task` tool (`agentTool`) is also built per agent. `Info()` lists `registry.ListByMode(subagent)` with every description. A markdown-defined agent has a `Location`; an agent defined in `.opencode.json` has none.
- **Per-step shaping precedent.** A flow step's `denyTools` is put on the step context (`tools.WithDeniedTools`, `internal/flow/service.go`) right before `NewAgent`. The factory folds it into `stepCacheKey` and builds the toolset without the denied tools. Subagents inherit it because `task` calls `NewAgent` with the tool-call context, which derives from the step's. The async task path copies it onto its detached context explicitly (`agent-tool-async.go`).
- **Live args.** The flow service holds the run's live args when it builds a step agent, and already expands `${args.x}` in step fields with `substituteScoped`. An unresolved token stays in the output as literal `${…}`.
- **Config loading.** Environment-reference expansion leaves dotted expressions like `${args.team}` untouched. `doublestar/v4` is already a dependency.
- **Pinned prefixes.** Steps of a session-generated flow that run on the initiating primary reuse that primary's tool objects (`primaryFlowPrefix.toolsFor`), for prompt-cache identity.

## Goals / Non-Goals

**Goals:**
- One workspace-level declaration that scopes both listings for every flow run, with no per-flow or per-step edits.
- Byte-identical listings wherever the scope does not apply, so prompt caches and existing tests are untouched.
- No loss of capability: every permitted skill and subagent stays named and usable.

**Non-Goals:**
- Changing what an agent may load or call. Permissions stay the only gate.
- Scoping the dynamic-flow planner's subagent list or TUI listings.
- Searchable or deferred skill descriptions (GENAI-257 proposal 4).

## Decisions

**D1. A top-level `listingScope` block with path globs.**
`{"listingScope": {"skills": [...], "subagents": [...]}}`, matched against the item's location relative to the working directory.
- *Alternative: name patterns such as `${args.team}-*`.* Rejected. Team repos ship skills without the team prefix (for example bdata's `dcs-events` and `scala-streaming-patterns`), and shared skills have no common prefix. Where an item was discovered is the only reliable signal of who owns it.
- *Alternative: nest the lists under the existing `skills` and `agents` keys.* Rejected. `agents` is a map of agent definitions, and keeping both lists together makes the feature read as one thing.
- *Alternative: a field on flows or steps.* Rejected for now. Every shared flow would repeat it. A step-level override can be added later without changing this surface.

**D2. Allowlist semantics: matching items are listed in full, the rest by name.**
An unexpected item is never lost, only shortened. Agents defined in `.opencode.json` have no location and are always listed in full, because they are shared by construction and few. A skill or agent located outside the working directory never matches, so it is listed by name.

**D3. Resolve per list, at step-agent build, and fail open.**
The flow service expands every pattern of a list with the same substitution `model:` uses, against the live args. Before substitution, a scalar value has its glob metacharacters escaped (`doublestar` honours `\` escapes), so a value is always literal.
- If any pattern still contains `${` after substitution, or a token resolves to an empty or non-scalar value, the whole list is inactive for that step.
- A token other than `${args.…}` also leaves the list inactive. It is logged once per pattern as a misconfiguration.
- *Alternative: drop only the unresolved patterns.* Rejected. With `[".agents/skills/**", "${args.team}/skills/**"]`, a run without a team would list every team skill by name only, which is the opposite of failing open.

**D4. Carry the resolved scope on the step context, exactly like `denyTools`.**
`tools.WithListingScope(ctx, ListingScope{Skills, Subagents []string})` is set next to `WithDeniedTools`. A nil list means the list does not apply.
- The factory reads it, adds a canonical string form to `stepCacheKey` (so a re-entered step with a different scope builds a new agent), and passes it through `newAgent` to `NewToolSet`, which hands each list to `NewSkillTool` and to the `task` tool.
- A synchronous subagent inherits the scope through the tool-call context, as `denyTools` does. The async path copies it the same way `agent-tool-async.go` copies the denied tools.
- *Alternative: have the `task` tool read the scope off its parent agent.* Rejected. The context already reaches nested and async subagents, and one mechanism is easier to reason about than two.

**D5. Matching helper.**
`relativeSlashPath(location)` returns the path relative to `config.WorkingDirectory()`, cleaned and with forward slashes. A path that starts with `..` never matches. `doublestar.Match(pattern, rel)` decides the match. Both listings share this one helper, in `internal/llm/tools`.

**D6. Rendering.**
- *Skills.* Full entries render exactly as today. When any permitted skill is out of scope, the block also carries `  <other_skills count="N">a, b, c</other_skills>` and a `<note>` saying they are outside this run's focus and loadable by name.
  - The budget logic reserves the length of those two elements before fitting full entries, then applies the existing drop, disclose and keep-one rules. The names element is never truncated.
  - The parameter description's examples are drawn from in-scope skills.
- *Subagents.* After the full `- name: description` lines comes one line: `Other subagent types (by name only; any of them can be used as subagent_type when the task needs it): a, b, c`.
- Names are sorted, and the scope is fixed per agent instance, so each description is byte-stable across requests.

**D7. A pinned primary prefix wins.**
`primaryFlowPrefix.toolsFor` keeps returning the primary's tool objects unchanged. Scoping a session-generated flow would break the cache identity that path exists to provide, and those flows run in chat sessions, which are unscoped anyway.

**D8. Observability.**
On the first `Info()` call of a scoped tool (`sync.Once`), log at INFO the agent ID, the listing, and the full and names-only counts. A full count of zero flags a pattern that matches nothing.

**D9. Interplay with existing config: discovery, then permissions, then presentation.**
`listingScope` adds no discovery root and removes no item. It only decides how an item that is already discovered and permitted is shown.
- **`skills.paths`, `agentPaths`, and the default roots.** These decide what exists (project `.agents/skills`, `.claude/skills`, `.agents/types`, `.opencode/agents`, global dirs, custom paths). The location discovery records is the only thing a scope pattern matches, so patterns mirror the path entries: one templated `${args.team}/skills/**` covers every `<team>/skills` entry, and onboarding a team changes nothing. When names are duplicated, the winning copy's location decides. Global directories sit outside the working directory, so their items are names-only under an applying scope.
- **`permission.skill`.** Applied first. A denied skill is in neither tier.
- **`skills.maxDescriptionChars` and `maxListingChars`.** Apply to full entries only (D6).
- **An agent's own `skills:` list.** Those are preloaded into its system prompt, so the scope never affects them. Their content is in context whichever tier they are listed in.
- **`flowPaths`, `cliTools.paths`, `contextPaths`.** No interplay. Flows are never listed to the model. CLI tools are tools, gated by `allowTools` and deferrable with `deferredTools`. Context files have their own scoping (GENAI-285).
- *Alternative: attach a scope to each `skills.paths` or `agentPaths` entry.* Rejected. It would turn plain string arrays into a union type (a schema change for every consumer), and the scope would have to be repeated on every team entry instead of written once as a template.

## Risks / Trade-offs

- **[Risk] The model misses a names-only skill it needed, because it can't see the description.** → Names in this workspace are descriptive, and the repo-owning team's skills stay named. The log counts plus Langfuse input tokens (GENAI-696, measure first) show whether the trimming costs quality. The workspace can widen the patterns without a code change.
- **[Risk] A typo in a pattern makes every item names-only.** → That still fails safe, since everything stays reachable, and the INFO line shows a full count of zero.
- **[Trade-off] More step agents per run when the scope changes between arrivals of the same step.** → Rare in practice: `team` is set once, early. The cost is one agent build.
- **[Risk] Run args come from webhook payloads.** → Values are escaped and only shape a listing, so nothing new becomes accessible.

## Migration Plan

Opt-in, with no migration. Roll out by releasing opencode and bumping the c2-agent image, then add `listingScope` to piano-developer's `.agents.opencode.json` in a workspace MR (GENAI-696). Roll back by removing the block.

## Open Questions

- Should the repo-owning team of a cross-team ticket also be listed in full, through a second arg such as `${args.repo_team}` in a separate pattern? It needs no change here, but D3's "whole list" rule means such an arg must always be set when used. Decide in the workspace once measurements exist.
