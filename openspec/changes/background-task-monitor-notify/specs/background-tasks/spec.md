## ADDED Requirements

### Requirement: Per-process in-memory task registry
The system SHALL maintain a process-global, in-memory registry of background tasks keyed by `task_id`. The registry SHALL be the single source of truth for the lifecycle state of every running, completed, killed, or failed background task. The registry MUST NOT persist its contents to any durable store; opencode restart loses all in-flight tasks.

#### Scenario: Task is registered at spawn
- **WHEN** a tool spawns a background task (bash `run_in_background`, task `async`, monitor, cron-fired completion)
- **THEN** the spawning code path registers a `Task` entry in the registry containing `id`, `session_id`, `kind`, initial `state = running`, `started_at`, `output_path`, `originating_tool_call_id`, and a lifecycle handle (`*os.Process` for shell-spawned, `context.CancelFunc` for subagent-spawned)

#### Scenario: Restart drops in-flight tasks
- **WHEN** opencode restarts while one or more tasks have state `running`
- **THEN** the registry is empty after boot and no completion notification is injected for the lost tasks; the agent's session still contains the original spawn ack message referencing the now-orphaned `task_id`

#### Scenario: Lookup by id
- **WHEN** a caller invokes `Registry.Get(taskID)`
- **THEN** the caller receives the `Task` entry and an existence boolean; concurrent lookups are safe via internal `sync.RWMutex`

#### Scenario: Per-session listing
- **WHEN** a caller invokes `Registry.ListBySession(sessionID)`
- **THEN** the caller receives every `Task` whose `session_id` matches, regardless of state, in registration order

### Requirement: Task ID format
Task IDs SHALL be of the form `<kind>_<base32(16-byte-random)>`, where `kind` is one of `shell`, `agent`, `monitor`, or `cron`, lowercase. The random component MUST be cryptographically random and base32-encoded without padding. IDs MUST be globally unique within a process; the registry MUST refuse to register a duplicate.

#### Scenario: Bash background mode produces a `shell_` ID
- **WHEN** the bash tool is invoked with `run_in_background: true`
- **THEN** the ack ToolResult contains a `task_id` matching `^shell_[A-Z2-7]+$` (16 random bytes → 26 base32 chars)

#### Scenario: Task async mode produces an `agent_` ID
- **WHEN** the task tool is invoked with `async: true`
- **THEN** the ack ToolResult contains a `task_id` matching `^agent_[A-Z2-7]+$`

#### Scenario: Monitor produces a `monitor_` ID
- **WHEN** the monitor tool is invoked
- **THEN** the ack ToolResult contains a `task_id` matching `^monitor_[A-Z2-7]+$`

### Requirement: Output file location and creation
Every background task SHALL be assigned a unique output file path under `<config.Data.Directory>/tasks/<task_id>.out`. The `tasks/` subdirectory MUST be created on demand by the registry (`MkdirAll`, mode 0o700). The output file MUST be created exclusively (O_CREATE|O_EXCL) so two tasks with the same ID cannot share a file.

#### Scenario: First background task creates the tasks directory
- **WHEN** the first background task in a fresh data directory is spawned
- **THEN** the directory `<config.Data.Directory>/tasks/` exists with mode 0o700 and the task's output file `<task_id>.out` is open for writing

#### Scenario: Output file is co-located with the data directory
- **WHEN** `.opencode.json` sets `data.directory` to `/var/lib/myproject/.opencode`
- **THEN** task output files are created under `/var/lib/myproject/.opencode/tasks/<task_id>.out`

### Requirement: Task lifecycle states
A task SHALL transition through a strict state machine: `running → completed | failed | killed`. The non-terminal `monitor-event` notification status MUST NOT be a registry state — it is a per-event injection from a monitor task that remains in state `running`. Terminal-state transitions are one-way; a `completed` task cannot revert to `running`.

#### Scenario: Subprocess exits cleanly
- **WHEN** a `shell_*` or `monitor_*` task's subprocess exits with code 0
- **THEN** the registry transitions the task from `running` to `completed` and records `finished_at` and `exit_code: 0`

#### Scenario: Subprocess exits non-zero
- **WHEN** a `shell_*` or `monitor_*` task's subprocess exits with code != 0
- **THEN** the registry transitions the task to `failed` and records `finished_at` and the exit code

#### Scenario: Subagent panics or errors
- **WHEN** an `agent_*` task's subagent run returns an error (or the agent goroutine panics)
- **THEN** the registry transitions the task to `failed` and records `finished_at`; no exit code is set

#### Scenario: taskstop terminates a running task
- **WHEN** the `taskstop` tool is called against a `running` task
- **THEN** the registry signals (SIGTERM for subprocess, `Cancel()` for subagent), transitions the task to `killed`, records `finished_at`, and the originating tool's monitor goroutine fires a `status: killed` completion notification via the task-notifications primitive

### Requirement: Per-task `notified` dedupe flag
Each task SHALL carry a `notified atomic.Bool` flag, initialized to `false`. The `EnqueueTaskCompletion` primitive MUST compare-and-swap this flag from `false` to `true` before writing a TERMINAL synthetic pair (`status: completed | failed | killed`). If the CAS fails (flag already true), the call returns silently without writing messages or starting `agent.Run`. The `monitor-event` status MUST NOT set or check this flag.

#### Scenario: Fast task whose ack carried output is not re-notified
- **WHEN** a bash background subprocess exits before the spawn goroutine reaches `cmd.Wait`, and the spawn ack ToolResult already carried the final output (atomically set `notified = true`)
- **THEN** the subsequent monitor goroutine's call to `EnqueueTaskCompletion` observes `notified == true`, returns silently, and no duplicate completion pair appears in the message log

#### Scenario: Monitor-event injections are never gated by notified
- **WHEN** a running monitor task fires consecutive `monitor-event` notifications
- **THEN** each call writes its synthetic pair regardless of the flag's value; the flag is neither read nor mutated by monitor-event injections

### Requirement: Orphan output cleanup at boot
At opencode startup, before any session activates, the registry SHALL sweep `<config.Data.Directory>/tasks/` and delete every `*.out` file that does not correspond to a registered (live) task. Since the registry is in-memory only, this cleanup deletes ALL output files on every boot.

#### Scenario: Long-lived data directory accumulates output between runs
- **WHEN** opencode boots and `<data.dir>/tasks/` contains output files from a previous session (file timestamps older than process start time)
- **THEN** the registry deletes all such files; the directory may remain (empty), or may be removed if empty

#### Scenario: K8s ephemeral pod
- **WHEN** opencode boots in a fresh pod with no prior `<data.dir>/tasks/`
- **THEN** the sweep is a no-op; the directory is created on first task spawn

### Requirement: Output writing concurrency
The subprocess writing to the task output file SHALL append in a single goroutine (`cmd.Stdout = f; cmd.Stderr = f`). Concurrent agent `Read` of the same file MUST be safe — the agent reads the file at the file system's consistency level (best-effort recent bytes; no locking required). The file MUST be flushed before the synthetic completion pair is written to the message log.

#### Scenario: Agent reads in-progress output
- **WHEN** the agent calls `Read` on `<data.dir>/tasks/<task_id>.out` while the subprocess is still running
- **THEN** the read returns whatever bytes have been flushed so far; partial-line reads are acceptable

#### Scenario: Output flushed before completion notification
- **WHEN** a background subprocess exits and `EnqueueTaskCompletion` is about to be called
- **THEN** the spawn goroutine SHALL `Sync()` the output file before invoking `EnqueueTaskCompletion`, so a subsequent agent `Read` triggered by the completion notification sees the full output
