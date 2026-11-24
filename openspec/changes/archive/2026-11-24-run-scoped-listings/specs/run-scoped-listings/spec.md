## Purpose

Scopes the `skill` tool's skill listing and the `task` tool's subagent listing to what a flow run is about, so agents in a workspace with many teams' skills and subagents carry full descriptions only for the relevant ones, while every other permitted skill and subagent stays listed by name and loadable or callable on demand.

## ADDED Requirements

### Requirement: Listing scope is configured per workspace and is off by default

`.opencode.json` SHALL accept an optional top-level `listingScope` object with two optional fields, `skills` and `subagents`, each a list of path patterns. A pattern is matched against an item's file location relative to the working directory, using forward slashes. `*` matches within one path segment and `**` matches across segments. A pattern MAY contain `${args.<path>}` tokens, where `<path>` is a dot-separated path into the flow run's args. These tokens SHALL survive configuration loading unexpanded. The published JSON schema SHALL declare the block. When `listingScope` is absent, or a list is absent or empty, the corresponding listing SHALL be byte-identical to the listing produced without this feature.

#### Scenario: No configuration changes nothing

- **WHEN** `.opencode.json` has no `listingScope`
- **THEN** every agent's `skill` and `task` tool descriptions are byte-identical to those produced before this feature, in and outside flow runs

#### Scenario: Run tokens survive configuration loading

- **WHEN** `.opencode.json` declares `"listingScope": {"skills": ["${args.team}/skills/**"]}` and the configuration is loaded, including environment-reference expansion
- **THEN** the loaded pattern is still `${args.team}/skills/**`

#### Scenario: Schema declares the block

- **WHEN** the JSON schema is regenerated
- **THEN** it declares `listingScope` with `skills` and `subagents` as arrays of strings

### Requirement: The scope resolves per flow step from the run's live args and fails open

For each flow step, the system SHALL resolve each configured list against the run's args as they stand when the step's agent is built. A list SHALL apply only when every token in every one of its patterns resolves to a non-empty string, number or boolean. Otherwise that list SHALL NOT apply, and its listing stays unscoped for that step. A resolved value SHALL be matched literally, never as pattern syntax. Only `${args.<path>}` tokens are supported; any other `${...}` token leaves its list unresolved. Agents outside a flow run (chat sessions, daemons, cron, bridge sessions) SHALL NOT be scoped. A step that runs on a pinned primary prefix SHALL keep that primary's listings unchanged. The two lists resolve independently.

#### Scenario: Resolved team scopes the step

- **WHEN** `listingScope.skills` is `[".agents/skills/**", "${args.team}/skills/**"]` and a flow step runs with `args.team = "composer"`
- **THEN** the step agent's skill listing is scoped with the patterns `.agents/skills/**` and `composer/skills/**`

#### Scenario: Missing team leaves the listing unscoped

- **WHEN** the same configuration applies and the run has no `team` arg, or an empty one
- **THEN** the step agent's skill listing is identical to the unscoped listing

#### Scenario: Team resolved by an earlier step

- **WHEN** a run starts without `team`, an earlier step sets `args.team = "platform"`, and a later step's agent is built afterwards
- **THEN** the earlier step's listing is unscoped and the later step's listing is scoped to `platform/skills/**`

#### Scenario: Re-entered step follows the current scope

- **WHEN** the same step runs twice in a run and the resolved scope differs between the two arrivals
- **THEN** each arrival's listing reflects the scope resolved for that arrival

#### Scenario: Value with pattern characters is literal

- **WHEN** `args.team` resolves to `*`
- **THEN** the pattern `${args.team}/skills/**` matches only a directory literally named `*`, not every directory

#### Scenario: Chat sessions are not scoped

- **WHEN** an agent runs in a chat, daemon, cron or bridge session that is not a flow step
- **THEN** its listings are unscoped, regardless of `listingScope`

### Requirement: Out-of-scope skills are listed by name only

When the skill list applies, the `skill` tool's `<available_skills>` block SHALL list every permitted skill whose location matches a scope pattern in full, exactly as an unscoped listing would. Every other permitted skill SHALL be listed by name only, sorted by name, in one compact element after the full entries. A note SHALL state that those skills are outside this run's focus and that any of them can be loaded by name when the task needs it. A skill denied by the agent's permission rules SHALL appear in neither part. The names-only element SHALL always be complete. Any configured listing budget applies to the full entries, after reserving room for the names-only element and its note. When no permitted skill is out of scope, the block SHALL be identical to the unscoped one. For a given set of skills, permissions and resolved scope, the description SHALL be byte-identical across requests.

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

### Requirement: Out-of-scope subagent types are listed by name only

When the subagent list applies, the `task` tool's list of available subagent types SHALL show each subagent type whose definition file matches a scope pattern, and each subagent type defined without a file, with its description as today. Every other subagent type SHALL be listed by name only, sorted by name, on one line after the full entries. A note SHALL state that any of them can still be used as `subagent_type` when the task needs it. When no subagent type is out of scope, the description SHALL be identical to the unscoped one.

#### Scenario: Team subagents scoped

- **WHEN** the scope is `[".agents/types/**", "platform/types/**"]` and subagent types are defined in `.agents/types`, in `platform/types`, in `composer/types`, and in `.opencode.json`
- **THEN** the types from `.agents/types`, `platform/types` and `.opencode.json` are listed with descriptions
- **AND** the `composer/types` types appear by name only, with the note

### Requirement: Loading and calling by name are unaffected by the scope

The scope SHALL change listings only. The `skill` tool SHALL load any skill the agent's permission rules allow, whether it is listed in full or by name. The `task` tool SHALL accept any subagent type registered in subagent mode, whether it is listed in full or by name. Error messages that list available skill or subagent names SHALL include the names-only entries.

#### Scenario: Load a names-only skill

- **WHEN** a scoped agent calls `skill` with the name of a skill listed by name only
- **THEN** the skill loads exactly as it would for an unscoped agent

#### Scenario: Call a names-only subagent

- **WHEN** a scoped agent calls `task` with a `subagent_type` listed by name only
- **THEN** the subagent runs exactly as it would for an unscoped agent

### Requirement: Subagents inherit the step's scope

A subagent spawned through the `task` tool, synchronously or with `async: true`, SHALL apply the same resolved scope to its own `skill` and `task` listings as the step agent that spawned it, including subagents spawned by subagents within that step. A subagent spawned by an unscoped agent SHALL be unscoped.

#### Scenario: Async subagent keeps the scope

- **WHEN** a step agent scoped to `composer` spawns a subagent with `async: true`
- **THEN** the subagent's skill listing is scoped to `composer` exactly as the step's is

### Requirement: Scoped listings are observable

Whenever a scope applies to an agent's listing, the system SHALL log one line per agent build and listing. The line names the agent and the listing (`skills` or `subagents`) and gives the counts of full and names-only entries. It SHALL NOT include arg values or resolved patterns.

#### Scenario: Log line on a scoped build

- **WHEN** a step agent is built with an applying skill scope
- **THEN** one log line records the agent, `skills`, and the full and names-only counts
