# Yield Tool

## Purpose

Defines the explicit idle handoff for background work. `yield` lets the runtime
resume the same session on reports or user input without model polling, a success
claim, or premature structured output. Whether the current run is retained depends
on its execution mode.

## Requirements

### Requirement: Empty input and standalone usage

The system SHALL register a tool named `yield` with no input parameters. `{}` or
omitted input SHALL be accepted; nonempty objects and other JSON values SHALL return
a tool-validation error. Its description SHALL instruct the model to call it alone
when no independent work remains. Yield SHALL NOT spawn a task, cancel background
work, claim success, or finish a flow step. Its input is a tool schema and requires
no new `.opencode.json` configuration fields or config-schema changes.

#### Scenario: Invalid input

- **WHEN** the model calls `yield` with a nonempty object, array, or `null`
- **THEN** the tool SHALL explain that it takes an empty object `{}`

### Requirement: Interactive runs release the session slot

In TUI and bridge runs with `NonInteractive: false`, a successful `yield` with
pending work and no available report/input SHALL persist its result and release
the run like a normal `end_turn`. An already available report/input or no pending
work SHALL instead continue generation immediately. User input SHALL
remain able to start a turn. Later reports SHALL use the existing idle auto-resume
path to continue the same session. A report racing with release MUST NOT be stranded.

#### Scenario: Monitor reports after an interactive yield

- **GIVEN** a TUI run starts a monitor and calls `yield` alone
- **WHEN** the run releases its slot and a monitor event arrives
- **THEN** the idle session SHALL auto-resume with the committed report
- **AND** the monitor SHALL remain running unless its own termination condition is met

### Requirement: Non-interactive runs wait for the first report or inbox message

For `NonInteractive: true`, the runtime SHALL persist the yield result, suspend
generation outside tool execution, and retain the session's run slot until a report
or queued user message is available. All flow steps, including interactive flow
steps, SHALL use this behavior. The wait SHALL subscribe before checking history
and pending tasks, and continue immediately for available reports/input or no pending
work. It SHALL use the run/step context deadline and preserve existing stall handling.

After waking, the model SHALL receive the complete report/input and may continue
independent work or yield again. Waking MUST NOT require all tasks to finish.
Successful final return SHALL still require no pending exact-session background work
and consumption of new reports/input, as defined in `background-tasks`.

#### Scenario: Streaming event resumes the flow run

- **GIVEN** a flow run has yielded while a monitor is active
- **WHEN** a monitor-event commits before subprocess exit
- **THEN** the same run SHALL resume under the step's agent with the report
- **AND** the monitor SHALL remain active so the model can handle more events

#### Scenario: Interactive flow accepts input while waiting

- **GIVEN** an interactive flow step has yielded with background work pending
- **WHEN** reviewer input enters the step session's inbox
- **THEN** the same run SHALL wake without waiting for background completion

#### Scenario: Nothing remains to wait for

- **WHEN** any run yields with no pending tasks or unconsumed reports
- **THEN** generation SHALL continue immediately instead of waiting for user input

### Requirement: Guidance supports legacy tool allowlists

Background guidance SHALL recommend `yield` alone when available. If an agent's
tool allowlist excludes `yield`, ending the turn SHALL remain a supported fallback:
interactive runs release their slot, and non-interactive runs with pending work
wait for the first report or inbox message. Neither route requires model polling.

#### Scenario: Existing allowlist has no yield tool

- **GIVEN** an agent can start background tasks but its allowlist excludes `yield`
- **WHEN** no independent work remains
- **THEN** its prompt SHALL explain ending the turn as the supported waiting action
- **AND** it SHALL NOT instruct the agent to call an unavailable `yield` tool

### Requirement: Completed work does not invite another wait

Yield guidance SHALL direct the agent to consume completion reports already in
context and yield only while registered exact-session background work remains.
When the registry shows no active exact-session tasks, a valid yield SHALL explain
that there is nothing to wait for and direct the agent to process results rather
than yield or poll inventory again. A remote job SHALL require a registered
background waiter; reviewer feedback SHALL use an available question tool.

#### Scenario: Completed task before yield

- **GIVEN** the session's background task has completed before yield executes
- **WHEN** the model calls yield
- **THEN** its acknowledgement SHALL identify that no active background work remains
- **AND** the existing immediate continuation and report delivery SHALL be preserved

### Requirement: Background suspension is observable independently of generation

For flow/headless runs with Langfuse tracing enabled, explicit yields and end
turns with pending work SHALL record a `background_wait` observation. Its numeric
`suspended_ms` SHALL count only event suspension, excluding generation, compaction,
question-tool time and report reads. Metadata SHALL identify the trigger, wake
reason, report count and pending task counts by kind. Reports or inbox input
already ready at the yield boundary SHALL record zero suspension. Ordinary end
turns without pending work SHALL NOT create wait observations. Telemetry SHALL NOT
alter waiting, cancellation, interactive release or first-report resume behavior.

#### Scenario: Subagent completes during suspension

- **GIVEN** a flow agent yields with a pending async subagent
- **WHEN** the completion report arrives
- **THEN** the wait observation SHALL identify a background-report wake
- **AND** its suspension time SHALL exclude model work before and after the wait

#### Scenario: Stale yield

- **WHEN** yield finds neither pending work nor unconsumed reports
- **THEN** the wait observation SHALL record zero suspension and `no_pending_tasks`
