# tasklist / taskstop Tools

## Purpose

Defines two control-plane tools for the background-tasks system: the read-only `tasklist` and the synchronous-kill `taskstop`. Both are session-scoped: `tasklist` returns only tasks belonging to the caller's session, and `taskstop` refuses cross-session kills. The descriptions registered with the tool registry explicitly direct the agent NOT to use `tasklist` as a polling loop — completion notifications arrive automatically when a background task finishes. `taskstop` is synchronous: it blocks until the SIGTERM (or context cancel for subagent tasks) has been sent, the process has exited (with a 5s SIGTERM→SIGKILL escalation), AND the synthetic `killed` completion has been written. Permission keys: `tasklist` defaults to `allow` (read-only observability), `taskstop` defaults to `ask`.

## Requirements


### Requirement: `tasklist` tool registration and behavior
The system SHALL register a new top-level tool named `tasklist`. The tool MUST be read-only and MUST list the background tasks belonging to the CURRENT session only. Its input schema accepts an optional `state` filter (`running` | `completed` | `failed` | `killed` | `all`, default `all`) and an optional `limit` (int, default 50, max 200).

#### Scenario: Empty session
- **WHEN** the agent invokes `tasklist` in a session with no background tasks
- **THEN** the tool returns a ToolResult with the literal phrase "No background tasks for this session"

#### Scenario: Active background tasks
- **WHEN** the agent invokes `tasklist` in a session that has 3 background tasks (2 running, 1 completed)
- **THEN** the tool returns a ToolResult containing one line per task with task_id, kind, state, started_at (ISO 8601), and (if not running) finished_at and exit_code (if applicable); sorted newest-first

#### Scenario: State filter
- **WHEN** the agent invokes `tasklist` with `state: "running"` against a session with 5 tasks (3 running, 2 completed)
- **THEN** the tool returns only the 3 running tasks

#### Scenario: Tasks from other sessions are not exposed
- **WHEN** another session has 10 background tasks and the agent invokes `tasklist` in this session
- **THEN** none of the other session's tasks appear; the tool's view is strictly scoped to the caller's session_id

### Requirement: `tasklist` is concurrency-safe and snapshot-consistent
The `tasklist` tool SHALL read the registry under its `RLock`. The returned list MUST represent a consistent snapshot of the registry at one moment; in-flight transitions during the call MAY appear or not appear, but no task may appear in a contradictory state (e.g., `running` with a `finished_at`).

#### Scenario: Concurrent transition during list
- **WHEN** a monitor task completes at the exact moment `tasklist` is called
- **THEN** the returned snapshot shows the task as either `running` (transition not yet visible) or `completed` (transition visible) — never both

### Requirement: `taskstop` tool registration and behavior
The system SHALL register a new top-level tool named `taskstop`. Its input schema requires `task_id` (string). The tool MUST verify that the task's `session_id` matches the caller's session before proceeding; cross-session kill MUST be refused.

#### Scenario: Successful kill of a running task
- **WHEN** the agent invokes `taskstop` with the `task_id` of a running bash background task in its own session
- **THEN** the registry's `Kill(taskID)` is invoked, the subprocess receives SIGTERM, a synthetic `Status: StatusKilled` completion notification is injected, and the tool returns a ToolResult confirming "Task <id> killed"

#### Scenario: Cross-session kill refused
- **WHEN** the agent invokes `taskstop` against a `task_id` belonging to a different session
- **THEN** the tool returns a tool error "Task <id> does not belong to this session"; no kill is performed

#### Scenario: Already-terminal task
- **WHEN** the agent invokes `taskstop` against a `task_id` that has already completed (or was already killed)
- **THEN** the tool returns a ToolResult noting the task is not running (no kill performed, no duplicate notification fired due to the `notified` dedupe gate)

#### Scenario: Unknown task ID
- **WHEN** the agent invokes `taskstop` with a `task_id` that is not in the registry (typo, or task lost to opencode restart)
- **THEN** the tool returns a tool error "No task found with ID: <id>"; no side effects

### Requirement: `taskstop` is synchronous
The `taskstop` tool SHALL block until the kill operation has completed, including:
1. Signal sent (SIGTERM for subprocess, `Cancel()` for subagent).
2. Subprocess exit observed (with a 5-second SIGTERM-to-SIGKILL escalation if needed).
3. Synthetic `Status: StatusKilled` completion notification written via `EnqueueTaskCompletion`.

The tool MUST return only after step 3 completes successfully.

#### Scenario: Subprocess respects SIGTERM
- **WHEN** the bash subprocess installs a SIGTERM handler and exits cleanly within 1 second
- **THEN** `taskstop` returns 1 second later; the synthetic completion is already in the message log

#### Scenario: Subprocess ignores SIGTERM
- **WHEN** the bash subprocess ignores SIGTERM
- **THEN** after 5 seconds the kill path escalates to SIGKILL; `taskstop` returns ~5 seconds later; the synthetic completion is written with status killed

#### Scenario: Subagent kill via context cancel
- **WHEN** `taskstop` targets an `agent_*` task
- **THEN** the registry calls the stored `context.CancelFunc`; the subagent's `agent.Run` returns with cancellation error; cost rollup runs; synthetic completion with `StatusKilled` is written; `taskstop` returns

### Requirement: Permission gate uses tool-specific rules
The `tasklist` tool MUST use a new permission key `tasklist`, default `allow` (it is read-only and observability-only). The `taskstop` tool MUST use a new permission key `taskstop`, default `ask`.

#### Scenario: Default policy
- **WHEN** no explicit rule is set for `tasklist` or `taskstop`
- **THEN** `tasklist` proceeds without prompting; `taskstop` triggers a permission prompt unless `permissionMode: allow` is in effect

#### Scenario: Headless override
- **WHEN** `permissionMode: allow` is set (e.g., `opencode serve` in c2-agent's pod)
- **THEN** both tools proceed without prompting

### Requirement: Tool descriptions reference the background-task primitive
The tool descriptions registered with the tool registry SHALL include guidance directing the agent to use `tasklist`/`taskstop` for inspecting and controlling background work spawned by `bash run_in_background`, `task async`, and `monitor`. The descriptions MUST NOT recommend polling via repeated `tasklist` calls; they MUST direct the agent to wait for the synthetic completion notification instead.

#### Scenario: Anti-polling guidance
- **WHEN** the tool description for `tasklist` is rendered to the agent
- **THEN** it includes language similar to "do NOT use tasklist as a polling loop — completion notifications arrive automatically when a task finishes; tasklist is for one-shot inventory queries"
