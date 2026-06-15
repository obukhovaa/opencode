## ADDED Requirements

### Requirement: Cron-fired completions use the shared task-notifications primitive
The cron scheduler's completion-write path SHALL invoke `task.EnqueueTaskCompletion` instead of writing the synthetic Assistant(ToolCall)+Tool(ToolResult) pair via direct `messages.CreatePair` calls. The shape of the resulting message pair MUST be observationally identical to today's `writeSyntheticMessages` output, with one addition: the Assistant message MUST carry `synthetic: true`.

#### Scenario: Cron job fires
- **WHEN** a cron job's schedule triggers and the task tool's run completes
- **THEN** the scheduler calls `task.EnqueueTaskCompletion` with `OriginatingToolName: "task"`, `Kind: KindCron`, `Status: StatusCompleted`, and the task tool's response content; the Assistant ToolCall message in the log has `synthetic: true`

#### Scenario: End-to-end behavior unchanged for non-bridge consumers
- **WHEN** a cron job fires
- **THEN** the parent session sees the same Assistant(ToolCall) + Tool(ToolResult) pair shape as before; subsequent agent.Run on the parent session reads the result and produces a follow-up assistant reply (existing tests in `internal/cron/` pass without modification)

### Requirement: Cron retains its own session-busy semantics
The cron scheduler's existing session-busy probe and skip-on-busy logic SHALL continue to live in the scheduler. The `EnqueueTaskCompletion` primitive MUST NOT acquire the per-session busy mutex on cron's behalf — cron owns that mutex and the synthetic write is committed only after cron's own busy check passes. The auto-resume `agent.Run` triggered by `EnqueueTaskCompletion` on idle sessions MUST be cron's responsibility OR happen via the primitive — implementation may choose either, but the end-state must be: if the session was idle when cron's busy check passed, a fresh agent.Run starts; if the session became busy after, no second run starts.

#### Scenario: Cron's busy-skip preserved
- **WHEN** a cron schedule fires while the parent session has an in-flight agent.Run (e.g., the user just typed a message)
- **THEN** the scheduler observes the busy state, defers the cron job, and does NOT invoke `EnqueueTaskCompletion`; no duplicate message pair is written

#### Scenario: Idle session at cron fire time
- **WHEN** a cron schedule fires while the parent session is idle
- **THEN** the scheduler invokes the task tool, writes the synthetic pair via `EnqueueTaskCompletion`, and a fresh `agent.Run` is started on the session

### Requirement: No behavior change for cron permission gating
The cron scheduler's existing permission-gating logic at job-creation time SHALL be unchanged. The migration to `EnqueueTaskCompletion` MUST NOT introduce a new permission check for cron-fired completions.

#### Scenario: Existing cron permissions honored
- **WHEN** a cron job's creation was permitted under the existing rule and the job fires
- **THEN** the synthetic completion is injected without an additional permission prompt
