## Purpose

Give OpenCode agents a compatible filesystem memory tool for durable project knowledge across sessions and cooperating processes.

## ADDED Requirements

### Requirement: Compatible memory command interface
The tool SHALL be named `memory` and accept Anthropic memory command names and parameter names for view, create, str_replace, insert, delete and rename. It SHALL support directory views, numbered/ranged text views, PNG/JPEG views, unique exact replacement, insertion after a line, and confined directory rename/deletion. Create SHALL refuse existing files.

#### Scenario: Reuse in another session
- **WHEN** one session creates a project memory entry and another views the same virtual path
- **THEN** the second receives the persisted content

#### Scenario: Edit ambiguity
- **WHEN** old_str occurs more than once or not at all
- **THEN** replacement returns a tool error without changing the file

#### Scenario: Range and insertion
- **WHEN** view_range uses an end of -1 or insert_line is zero
- **THEN** the view extends to EOF or the insertion precedes the first line respectively

### Requirement: Configurable aggregate and project routing
Memory SHALL be opt-in with a configurable filesystem root, Claude-style project indexes and routing guidance. The root SHALL serve as the deployment's memory isolation boundary.

#### Scenario: Multi-repository harness
- **WHEN** a job works across repositories from one harness working directory
- **THEN** the agent can address distinct project directories through the path parameter without changing the tool schema

### Requirement: Permission and filesystem confinement
Only agents explicitly granted memory SHALL receive the tool. All operations SHALL enforce memory permissions and remain under the configured root. Traversal, symlinks, special files, hidden internal metadata, root deletion and root rename SHALL be rejected.

#### Scenario: Denied write
- **WHEN** an agent's memory permission denies create
- **THEN** the request changes no memory content

#### Scenario: Escape attempt
- **WHEN** a path attempts traversal or passes through a symlink outside the root
- **THEN** the request fails without reading or modifying the outside target

### Requirement: Cooperative concurrency
The tool SHALL coordinate with other processes and mem-sync using the aggregate lock, publish updates atomically, and bound reads and output.

#### Scenario: Concurrent writers
- **WHEN** separate processes edit one memory concurrently
- **THEN** each operation is serialized or reports contention; partial writes are never exposed

### Requirement: Provider compatibility
Compatible Claude requests SHALL use the native memory tool definition and other providers SHALL receive the equivalent function schema. Token counting, caching and deferred tool references SHALL retain the memory declaration.

#### Scenario: Native Claude request
- **WHEN** memory is enabled for a Claude model
- **THEN** the request declares type memory_20250818 with name memory and no custom input schema

#### Scenario: Other provider
- **WHEN** memory is enabled for a non-Claude model
- **THEN** the model receives the same commands as a normal function tool
