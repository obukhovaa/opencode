## MODIFIED Requirements

### Requirement: Out-of-scope skills are listed by name only

When the skill list applies, the `skill` tool's `<available_skills>` block SHALL list in full, exactly as an unscoped listing would, every permitted skill whose location matches a scope pattern and every permitted skill whose file lies outside the working directory (an installation-level skill such as one in the home directory). Every other permitted skill SHALL be listed by name only, sorted by name, in one compact element after the full entries. A note SHALL state that those skills are outside this run's focus and that any of them can be loaded by name when the task needs it. A skill denied by the agent's permission rules SHALL appear in neither part. The names-only element SHALL always be complete. Any configured listing budget applies to the full entries, after reserving room for the names-only element and its note. When no permitted skill is out of scope, the block SHALL be identical to the unscoped one. For a given set of skills, permissions and resolved scope, the description SHALL be byte-identical across requests.

#### Scenario: Two tiers

- **WHEN** the scope is `[".agents/skills/**", "composer/skills/**"]` and permitted skills exist under `.agents/skills`, `composer/skills` and `bdata/skills`
- **THEN** the skills under `.agents/skills` and `composer/skills` are listed with their descriptions
- **AND** the skills under `bdata/skills` appear by name only, after them, with the note

#### Scenario: Denied skills stay hidden

- **WHEN** the agent's `permission.skill` denies `bdata-*` and the scope excludes `bdata/skills/**`
- **THEN** no `bdata-*` skill appears, not even by name

#### Scenario: Budget keeps every name

- **WHEN** a listing budget is configured that cannot hold all full entries plus the names-only element
- **THEN** every out-of-scope name is still listed, full entries are dropped by the existing budget rules, and the omission is disclosed in-band

#### Scenario: Nothing out of scope

- **WHEN** every permitted skill matches the scope
- **THEN** the block has no names-only element and equals the unscoped block

#### Scenario: Skill outside the working directory stays in full

- **WHEN** a scope applies and a permitted skill is discovered from the home directory (for example `~/.agents/skills/agent-browser/SKILL.md`)
- **THEN** that skill is listed with its description, not by name only

### Requirement: Out-of-scope subagent types are listed by name only

When the subagent list applies, the `task` tool's list of available subagent types SHALL show each subagent type whose definition file matches a scope pattern, each subagent type defined without a file, and each subagent type whose definition file lies outside the working directory, with its description as today. Every other subagent type SHALL be listed by name only, sorted by name, on one line after the full entries. A note SHALL state that any of them can still be used as `subagent_type` when the task needs it. When no subagent type is out of scope, the description SHALL be identical to the unscoped one.

#### Scenario: Team subagents scoped

- **WHEN** the scope is `[".agents/types/**", "platform/types/**"]` and subagent types are defined in `.agents/types`, in `platform/types`, in `composer/types`, and in `.opencode.json`
- **THEN** the types from `.agents/types`, `platform/types` and `.opencode.json` are listed with descriptions
- **AND** the `composer/types` types appear by name only, with the note

#### Scenario: Global subagent type stays in full

- **WHEN** a scope applies and a subagent type is defined in a global agents directory outside the working directory
- **THEN** it is listed with its description, not by name only
