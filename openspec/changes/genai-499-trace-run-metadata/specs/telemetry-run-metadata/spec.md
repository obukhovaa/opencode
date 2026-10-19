## ADDED Requirements

### Requirement: A run's trace records its turn budget

When an agent run (one `RunWith` call) creates its Langfuse trace, the system SHALL record the run's effective turn budget as the `max_turns` metadata key, namespaced by `telemetry.metadataNamespace` like every other custom key. The key SHALL be written regardless of `telemetry.generations`.

#### Scenario: Flow step with a budget

- **WHEN** a flow step with `maxTurns: 40` starts its run and `metadataNamespace` is `piano`
- **THEN** its trace carries `piano.max_turns = 40`, set when the trace is created

### Requirement: A run's trace records how it ended

When a run ends, the system SHALL record `turns_used` and `finish_reason` on its trace, namespaced like `max_turns`.

`turns_used` SHALL count the model calls the run's agent made, summed across non-interactive re-entries after background tasks, the forced max-turns wrap-up included. A compaction's summarizer call SHALL NOT be counted.

`finish_reason` SHALL be:
- `canceled` when the run returned a cancellation error;
- `error` when it returned any other error, or panicked;
- otherwise `turns_exhausted` when a turn-budget gate ended the run;
- otherwise `canceled` when the run's context was cancelled, or `error` when its deadline passed, by the time the run returned a result;
- otherwise `permission_denied` or `canceled` when the final message finished that way;
- and `end_turn` for every other finish, an accepted `struct_output` and a `max_tokens` stop included.

#### Scenario: Natural finish

- **WHEN** the model ends its turn on the first call
- **THEN** the trace carries `turns_used = 1` and `finish_reason = end_turn`

#### Scenario: Budget exhausted

- **WHEN** a run with `max_turns = 1` keeps calling tools and gets the forced wrap-up call
- **THEN** the trace carries `turns_used = 2` and `finish_reason = turns_exhausted`

#### Scenario: Rejected permission prompt

- **WHEN** a tool call's permission prompt is rejected and the run ends
- **THEN** the trace carries `finish_reason = permission_denied`

#### Scenario: Provider failure

- **WHEN** the model call fails and the run returns the error
- **THEN** the trace carries `finish_reason = error`

#### Scenario: Interrupted background-task wait

- **WHEN** a non-interactive run ends its turn, waits on a background task, and is cancelled (or hits its step deadline) before the task finishes
- **THEN** the run returns its last result with no error, and the trace carries `finish_reason = canceled` (or `error` for the deadline)

#### Scenario: Panic

- **WHEN** the run panics
- **THEN** the trace carries `finish_reason = error` and the panic still reaches `RunWith`'s recovery

### Requirement: A subagent's run metadata stays on its own observation

A subagent's trace is an observation inside its parent's trace. Its `max_turns`, `turns_used` and `finish_reason` SHALL be written as observation metadata and SHALL NOT be written as trace-level metadata, so the parent run's values are never overwritten.

#### Scenario: Subagent ends before its parent

- **WHEN** a task subagent finishes after 3 model calls while its parent run continues
- **THEN** the subagent's observation carries `turns_used = 3` and the parent trace's `turns_used` is the parent's own count

### Requirement: Runs started through POST /flow carry their flow tag

A run started through `POST /flow` SHALL publish a `flow:<flowID>` tag in its run identity, even when the request carries no other identity field. The tag SHALL shadow every `flow:` tag from `telemetry.tags`, so the run's traces and provider metadata carry exactly one `flow:` tag. A blank flow ID SHALL render no tag. A flow started by `opencode serve --flow` SHALL NOT publish a flow tag, and its configured `flow:` tag SHALL stay in effect. The tag SHALL be cleared with the rest of the run identity when the run ends.

#### Scenario: Pooled run on a pod booted with flow:none

- **WHEN** a pool pod whose `telemetry.tags` include `flow:none` receives `POST /flow` with `flowID: pr-review`
- **THEN** the run's traces carry `flow:pr-review` and not `flow:none`

#### Scenario: Run with no identity fields

- **WHEN** `POST /flow` carries only `flowID: A`
- **THEN** the run identity holds the tag `flow:A` while the run is in flight, and none of the previous run's key, user or tags

#### Scenario: Per-Job auto-start

- **WHEN** a pod starts a flow with `opencode serve --flow A`
- **THEN** no run identity is published and the configured `flow:` tag is used
