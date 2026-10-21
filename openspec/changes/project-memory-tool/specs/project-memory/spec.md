## Purpose

Carry concise project knowledge across sessions using bounded index context and existing filesystem tools, without introducing another model-facing tool.

## ADDED Requirements

### Requirement: No dedicated memory tool
The runtime SHALL use existing file tools for memory and SHALL NOT register a memory tool or add memory-specific provider schemas.

#### Scenario: Tool inventory
- **WHEN** project memory is enabled
- **THEN** the agent's tool inventory and provider tool definitions remain unchanged

### Requirement: Capability-aware reminder
The system reminder SHALL describe only reading and/or writing operations supported by the agent's available tools and permissions. Agents without usable file capabilities SHALL receive no memory reminder. Automatic index loading MUST NOT bypass denied or approval-required reads.

#### Scenario: Reader only
- **WHEN** the agent can read but cannot write memory
- **THEN** it receives index/retrieval guidance and no instruction to create, edit or prune memory

#### Scenario: Writer only
- **WHEN** the agent can write but cannot read memory
- **THEN** it receives concise note-authoring guidance without loading existing memory contents or instructing it to overwrite an unread index

#### Scenario: Bash or read and write
- **WHEN** the agent can use ordinary tools or permitted Bash to read and write memory
- **THEN** it receives the full read/maintain instructions without another tool definition

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

#### Scenario: Harness working on another repository
- **WHEN** work concerns a repository other than the process workspace
- **THEN** the reminder directs the agent to that repository's project memory instead of treating the harness index as global knowledge
