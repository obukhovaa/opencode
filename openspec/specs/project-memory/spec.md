# project-memory Specification

## Purpose
Carry concise project knowledge across sessions using bounded index context and existing filesystem tools, without introducing another model-facing tool.
## Requirements
### Requirement: No dedicated memory tool
The runtime SHALL use existing file tools for memory and SHALL NOT register a memory tool or add memory-specific provider schemas.

#### Scenario: Tool inventory
- **WHEN** project memory is enabled
- **THEN** the agent's tool inventory and provider tool definitions remain unchanged

### Requirement: Capability-aware reminder
The system reminder SHALL describe only operations supported by the agent's available tools and permissions, at one of two capability levels: read-only, or read+write. Write capability SHALL imply read capability; writing without reading is not supported. File editing tools SHALL count as memory writers only alongside the read tool they depend on. Agents without a usable read capability SHALL receive no memory reminder. Automatic index loading MUST NOT bypass denied or approval-required reads and MUST verify that the opened index matches the canonical path whose permissions were checked.

#### Scenario: Reader only
- **WHEN** the agent can read but cannot write memory
- **THEN** it receives index/retrieval guidance and no instruction to create, edit or prune memory

#### Scenario: Writer
- **WHEN** the agent can write memory with permitted Bash, or with file write tools alongside the read tool
- **THEN** it receives the full read/maintain instructions, including today's UTC date for updatedAt, without another tool definition

#### Scenario: Write tools without the read tool
- **WHEN** the agent has file write tools but not the read tool
- **THEN** those tools do not make it a memory writer

#### Scenario: Store root changes after authorization
- **WHEN** an authorized store root is replaced or its symlink is retargeted before the index is opened
- **THEN** an index from a different canonical path is not preloaded, while an unchanged authorized symlink root remains supported

#### Scenario: Index is created after capability resolution
- **WHEN** the index did not exist when capabilities and permissions were resolved
- **THEN** retrieval guidance remains available, but automatic loading waits for a later resolution that authorizes the new index

### Requirement: Bounded project index
Only the matching project's MEMORY.md index SHALL be automatically loaded, with at most 200 lines and 25 KiB. Detailed topic bodies SHALL remain on disk until explicitly retrieved. The snapshot SHALL be inserted at most once per active session history and refreshed for a new session or after compaction removes it.

#### Scenario: Large index
- **WHEN** the index exceeds either context limit
- **THEN** only the bounded prefix is loaded with an explicit truncation notice and the file is not changed

#### Scenario: Shared filesystem
- **WHEN** one session writes notes and another session starts with the same project identity
- **THEN** the new session receives the current project index

### Requirement: Concise notes and freshness
Authoring instructions SHALL require short factual sentences, source links, a maintained one-line-per-topic index, and minimal description/updatedAt frontmatter. updatedAt SHALL represent the last factual update. Notes SHALL have an 8 KiB authoring budget and index files the same budgets as startup context. Existing file-tool results SHALL flag oversized or malformed notes without silently deleting written content.

#### Scenario: Stale evidence
- **WHEN** a note is old or contradicted by current evidence
- **THEN** the reminder directs the agent to verify, correct or retire the claim according to evidence rather than age alone

#### Scenario: Oversized write
- **WHEN** an ordinary write/edit/patch saves an over-budget memory file
- **THEN** its result reports that the write succeeded and instructs an eligible writer to shorten or split the file

### Requirement: Portable scope
The feature SHALL retain configurable roots, Git identities shared by worktrees, explicit aliases and the mem-sync aggregate format.

#### Scenario: Store root selected per launch
- **WHEN** the process environment sets a non-empty `AGENTS_MEMORY_HOME`
- **THEN** it is the memory root, overriding `memory.directory`; otherwise `memory.directory`, then `~/.agents/memory`, applies, so OpenCode and mem-sync address the same store from the same variable

#### Scenario: Workspace without a network origin
- **WHEN** the workspace has no network Git origin and no explicit project ID
- **THEN** its identity is `local:` plus the absolute path with symlinks resolved, the same path mem-sync derives, so both address one project directory

#### Scenario: Harness working on another repository
- **WHEN** work concerns a repository other than the process workspace
- **THEN** the reminder directs the agent to that repository's project memory, found by its id in `projects/*/project.json`, instead of treating the harness index as global knowledge
- **AND** for a repository with no project directory, Bash writers are given the key derivation and other agents report the facts instead of saving them

#### Scenario: Unresolvable memory root or identity
- **WHEN** the home directory of a `~` root or the project identity cannot be resolved
- **THEN** memory is unavailable, a warning is logged once per process, and nothing is written under the working directory

#### Scenario: Git remotes use distinct nondefault ports
- **WHEN** Git remotes have the same host and repository path but differ by a nondefault port for their URL scheme
- **THEN** their project identities remain distinct; only the default port of each remote's own scheme is omitted

