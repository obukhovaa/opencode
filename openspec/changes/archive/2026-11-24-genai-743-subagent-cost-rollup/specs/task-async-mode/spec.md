## MODIFIED Requirements

### Requirement: Cost rollup in async mode
The async subagent's cost SHALL roll up to the parent session in the background goroutine, BEFORE `EnqueueTaskCompletion` writes the completion pair. It SHALL add what THIS run spent as one atomic increment and MUST behave exactly like the synchronous roll-up (see `subagent-cost-rollup`): run baseline, bounded accounting context, warn-and-continue on errors, and refusal of a resume while the previous run or roll-up is in flight.

#### Scenario: Cost rolled up exactly once
- **WHEN** an async subagent completes
- **THEN** the parent session's stored `cost` field has incremented by exactly the cost this run added to the subagent's session; no double-counting; no cost loss if cancelled

#### Scenario: Resumed async task adds only the resumed run
- **WHEN** an async task is resumed by `task_id` on a session that already holds cost B from earlier runs, and the resumed run brings it to B + R
- **THEN** the parent session's stored `cost` grows by R, not by B + R

#### Scenario: Busy resume
- **WHEN** an async resume arrives while the task's previous run or roll-up is in flight
- **THEN** the task tool returns `async task: start subagent: …` wrapping the session-busy error and nothing is rolled up

#### Scenario: Ledger released after a panic
- **WHEN** the background goroutine recovers from a panic after the subagent finished
- **THEN** the task's run-ledger entry is released, so a later resume is not refused as busy
