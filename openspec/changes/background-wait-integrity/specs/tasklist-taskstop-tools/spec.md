## MODIFIED Requirements

### Requirement: `tasklist` tool registration and behavior
The system SHALL register a new top-level tool named `tasklist`. The tool MUST be read-only and MUST list the background tasks belonging to the current session AND to sessions whose parent is the current session (one level of descent) — the same scope the foreground-wait redirect uses. Child-owned rows MUST be marked as such so the model can tell them apart. Tasks belonging to any other session, including parallel sibling steps of the same flow, MUST NOT be listed. Its input schema accepts an optional `state` filter (`running` | `completed` | `failed` | `killed` | `all`, default `all`) and an optional `limit` (int, default 50, max 200).

#### Scenario: Empty session
- **WHEN** the agent invokes `tasklist` in a session with no background tasks
- **THEN** the tool returns a ToolResult with the literal phrase "No background tasks for this session"

#### Scenario: Active background tasks
- **WHEN** the agent invokes `tasklist` in a session that has 3 background tasks (2 running, 1 completed)
- **THEN** the tool returns a ToolResult containing one line per task with task_id, kind, state, started_at (ISO 8601), and (if not running) finished_at and exit_code (if applicable); a `kind=monitor` row additionally carries a scanned-line count; a child-owned row carries an owning-session marker; sorted newest-first

#### Scenario: State filter
- **WHEN** the agent invokes `tasklist` with `state: "running"` against a session with 5 tasks (3 running, 2 completed)
- **THEN** the tool returns only the 3 running tasks

#### Scenario: Tasks from other sessions are not exposed
- **WHEN** an UNRELATED session (not a child of the caller) has 10 background tasks and the agent invokes `tasklist` in this session
- **THEN** none of that session's tasks appear

#### Scenario: Child-owned tasks are listed and marked
- **WHEN** a subagent whose parent is the caller has a running background task
- **THEN** the task appears in the caller's `tasklist` output, marked as owned by the child session

#### Scenario: Sibling-step tasks are not exposed
- **GIVEN** a parallel sibling step of the same flow has background tasks
- **WHEN** the agent invokes `tasklist`
- **THEN** none of the sibling's tasks appear

### Requirement: `taskstop` tool registration and behavior
The system SHALL register a new top-level tool named `taskstop`. Its input schema requires `task_id` (string). The tool MUST verify that the task is owned by the caller's session or by a session whose parent is the caller (one level of descent) before proceeding; a kill targeting any other session MUST be refused. This mirrors the foreground-wait redirect's scope: the redirect can block the caller on a child-owned task, so the caller MUST be able to observe and cancel that task — otherwise it is blocked on work it has no way to escape.

#### Scenario: Successful kill of a running task
- **WHEN** the agent invokes `taskstop` with the `task_id` of a running bash background task in its own session
- **THEN** the registry's `Kill(taskID)` is invoked, the subprocess receives SIGTERM, a synthetic `Status: StatusKilled` completion notification is injected, and the tool returns a ToolResult confirming "Task <id> killed"

#### Scenario: Cross-session kill refused
- **WHEN** the agent invokes `taskstop` against a `task_id` belonging to a session that is neither the caller nor a direct child (including a parallel sibling step of the same flow)
- **THEN** the tool returns a tool error "Task <id> does not belong to this session"; no kill is performed

#### Scenario: Child-owned task can be killed
- **WHEN** the agent invokes `taskstop` against a `task_id` owned by a session whose parent is the caller
- **THEN** the kill proceeds exactly as for a task in the caller's own session

#### Scenario: Already-terminal task
- **WHEN** the agent invokes `taskstop` against a `task_id` that has already completed (or was already killed)
- **THEN** the tool returns a ToolResult noting the task is not running (no kill performed, no duplicate notification fired due to the `notified` dedupe gate)

#### Scenario: Unknown task ID
- **WHEN** the agent invokes `taskstop` with a `task_id` that is not in the registry (typo, or task lost to opencode restart)
- **THEN** the tool returns a tool error "No task found with ID: <id>"; no side effects

