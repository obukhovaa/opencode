## ADDED Requirements

### Requirement: A task run adds only its own spend to the parent

When a `task` run ends, the system SHALL add to the parent session's stored cost the child session's stored cost after the run minus the run's baseline. This SHALL hold for synchronous and asynchronous runs, for new and resumed tasks, and for runs that succeed, fail, are cancelled or are killed with `taskstop`. A run that adds no cost to the child SHALL leave the parent's cost unchanged. A resume refused because the child session is busy SHALL add nothing.

#### Scenario: New task

- **WHEN** a parent with stored cost P starts a new task whose run spends C
- **THEN** the parent's stored cost is P + C

#### Scenario: Resumed task is not counted twice

- **WHEN** a task's first run spent 0.32487257, and the parent resumes it by `task_id` (synchronously or with `async: true`) and the resumed run spends 0.09976659
- **THEN** the parent's stored cost has grown by 0.42463916 in total over both runs, not by 0.74951173

#### Scenario: Failed or cancelled run still hands over its spend

- **WHEN** a task run spends C and then fails, is cancelled or is killed
- **THEN** the parent's stored cost grows by C before the task's result or completion notification is delivered

#### Scenario: Nested subagents

- **WHEN** a task run starts its own subagent, whose run spends G, and the task's own calls spend C
- **THEN** the task's session grows by C + G and its parent grows by C + G once

### Requirement: The roll-up runs on a bounded context that survives cancellation

The subagent cost roll-up SHALL run on a context that the caller's cancellation does not end and that is bounded in time, so a cancelled caller cannot drop the run's spend and a stuck database cannot hold the task open forever.

#### Scenario: Caller cancelled

- **WHEN** a synchronous task run spends C and the caller's context is cancelled before the roll-up
- **THEN** the parent's stored cost still grows by C

### Requirement: One run and roll-up per task session

A task session's run and its roll-up SHALL form one critical section in the process. While one is in flight, a resume of the same task SHALL be refused with the session-busy error and SHALL add nothing. A resumed run SHALL read the child's current stored cost after acquiring its slot. The final snapshot SHALL include post-turn compaction recorded before the run channel closes.

#### Scenario: Resume while the previous run or roll-up is in flight

- **WHEN** a task's run, or the roll-up that follows it, has not finished and the parent resumes the same task by `task_id`
- **THEN** the resume is refused with the session-busy error and the parent's stored cost is not charged for it

#### Scenario: Workers alternate between runs

- **WHEN** a second worker completes and credits a run between two invocations on the first worker
- **THEN** the first worker credits only the cost incurred during its latest invocation

#### Scenario: Compaction follows the terminal event

- **WHEN** a task records compaction usage after sending its terminal event and before closing its result channel
- **THEN** its parent receives that usage when the task completes

### Requirement: Stored session cost changes only by atomic increments

The system SHALL change a session's stored cost after creation only by adding a non-negative delta in a single database statement, on every supported session provider (SQLite and MySQL). The agent's per-call usage, compaction calls and subagent roll-ups SHALL all use it. A usage or compaction increment SHALL be the same statement as the token counts and summary message id it belongs to. Saving any other session fields SHALL NOT write the stored cost.

#### Scenario: Parallel task completions

- **WHEN** several tasks of one parent complete at the same moment, each having spent its own amount, while the parent records the cost of its own model calls
- **THEN** the parent's stored cost equals its own spend plus the sum of every task's spend; no increment is lost

#### Scenario: Stale save keeps the cost

- **WHEN** a component reads a session, the session's cost is then incremented, and the component saves its older copy with changed tokens or summary
- **THEN** the saved fields are written and the stored cost keeps the increment

#### Scenario: MySQL provider

- **WHEN** the session provider is MySQL and concurrent increments target one session
- **THEN** the stored cost equals the sum of all increments

#### Scenario: Usage and compaction land with their cost

- **WHEN** a model call or a compaction is recorded
- **THEN** its token counts, its summary message id (compaction) and its cost increment are written by one statement

#### Scenario: Negative delta

- **WHEN** a caller asks to add a negative cost
- **THEN** the request is rejected and the stored cost is unchanged

### Requirement: Cost writes retry only the write statement

A retry after a transient database conflict SHALL re-issue only the write statement. Reading the updated row back SHALL NOT be retried together with the write. When the read-back fails after the write committed, the system SHALL return an error wrapping `session.ErrCostCommitted`, and the caller SHALL NOT re-issue the increment.

#### Scenario: Transient write conflict

- **WHEN** the increment statement fails with a retryable conflict and the retry succeeds
- **THEN** the increment is applied once

#### Scenario: Read-back fails after the write

- **WHEN** the increment committed and reading the row back fails
- **THEN** the write is not issued again and the error wraps `session.ErrCostCommitted`

### Requirement: A step's reported cost includes its subagents

The `cost` of a flow step (flow events, the CLI `--flow` result's `steps[].cost`) SHALL be the stored cost of the step's session, which includes the spend of every subagent session below it. The flow total (`metrics.cost`) SHALL be the sum over step sessions only. Documentation of these fields SHALL say so and SHALL warn that adding child session costs counts them twice.

#### Scenario: Step with subagents

- **WHEN** a step's own model calls spend S and its subagents spend C in total, including resumed runs
- **THEN** the step's reported `cost` is S + C, and the flow's `metrics.cost` adds S + C once
