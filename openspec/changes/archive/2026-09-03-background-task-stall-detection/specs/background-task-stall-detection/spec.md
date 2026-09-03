# background-task-stall-detection

Progress-based termination of subagent background tasks. A `task`-kind background task
whose LLM loop has demonstrably stopped turning is killed through the existing task-kill
path, so the non-interactive drain returns for its ordinary reason and the parent step
completes with the structured output it already produced — instead of parking until the
orchestrator's flow deadline. Process-backed tasks (`bash`, `monitor`) are out of scope
by design: their liveness is answered by the operating system, not inferred.

## ADDED Requirements

### Requirement: Stall detection applies only to subagent-backed tasks

The system SHALL evaluate stalling for tasks of kind `task` (async subagents) only. It
SHALL NOT evaluate, and MUST NOT terminate on progress grounds, tasks of kind `bash`,
`monitor`, or `cron`.

The distinction is the task's lifecycle handle. `bash` and `monitor` tasks carry a real
`*os.Process`, so their liveness is definitional — the process is running or it has
exited, and the registry is already notified on exit. A `task` carries only a
`context.CancelFunc` around an LLM loop, which nothing observes; for that kind alone
liveness must be inferred.

Silence in particular MUST NOT be treated as evidence of a stall for an exempt kind. A
`monitor` exists to poll an external source cheaply, without LLM calls, and by design
emits nothing until its pattern matches. Its bounds are `max_events` (chosen by the
model), `taskstop`, natural subprocess exit, and the surrounding flow deadline; this
capability SHALL NOT add a time bound to monitors.

#### Scenario: Silent monitor is never killed on progress grounds

- **GIVEN** a `monitor` task polling an external source whose pattern has not matched
- **AND** it has produced no events and written no output for longer than the stall
  threshold
- **WHEN** stall evaluation runs
- **THEN** the monitor is not terminated
- **AND** it continues until `max_events`, `taskstop`, subprocess exit, or the
  surrounding ctx ends it

#### Scenario: Long-running silent bash task is never killed on progress grounds

- **GIVEN** a `bash run_in_background` task running a build that has written no output
  for longer than the stall threshold
- **WHEN** stall evaluation runs
- **THEN** the task is not terminated and continues to completion

#### Scenario: Subagent task is in scope

- **GIVEN** an async subagent task
- **WHEN** stall evaluation runs and the task's progress signal is older than the
  threshold
- **THEN** the task is eligible for stall termination

### Requirement: A subagent task's progress signal is its own session's message log

The system SHALL record, on each registered `task`-kind entry, the session id of the
subagent it spawned, distinct from the task's `SessionID` (which identifies the
*parent* session and is what per-session pending lookups key on). This field joins
the set a task carries at registration (see the `background-tasks` registration
requirement).

A subagent task's progress timestamp SHALL be the creation/update time of the most
recent message persisted on that subagent session, falling back to the task's
`StartedAt` when the subagent has persisted no message yet. Any new message — a
generation, a tool call, or a tool result — SHALL count as progress.

A `task`-kind entry without a recorded subagent session id MUST NOT be considered
stalled, so a missing signal can never cause a termination, and the skip SHALL be
logged — that state means the spawn-site wiring regressed and detection is silently
inactive for the task.

An unavailable progress answer — a failed read of the subagent session — SHALL be
treated as unknown and MUST NOT be substituted with a timestamp. Substituting either
`StartedAt` or the zero time reads as a stall for any task already older than the
threshold, which would terminate healthy long-running work on a single transient
read failure.

#### Scenario: New message resets the progress clock

- **GIVEN** a subagent task approaching the stall threshold
- **WHEN** the subagent persists a new message on its own session
- **THEN** its progress timestamp advances and it is no longer close to the threshold

#### Scenario: Subagent that has not yet persisted a message uses its start time

- **GIVEN** a subagent task registered moments ago with no messages on its session
- **WHEN** stall evaluation runs
- **THEN** the task's age is measured from `StartedAt` and it is not stalled

#### Scenario: Missing subagent session id is never stalled

- **GIVEN** a `task`-kind entry with no recorded subagent session id
- **WHEN** stall evaluation runs
- **THEN** the task is not considered stalled and is not terminated
- **AND** the skip is logged, so the spawn-site regression that caused it is visible

#### Scenario: A failed progress read never terminates a task

- **GIVEN** a subagent task whose total runtime already exceeds the threshold
- **AND** a read of its session that returns an error
- **WHEN** stall evaluation runs
- **THEN** the progress answer is unknown and the task is not terminated

### Requirement: The stall threshold is configurable and defaults above the largest tool-call budget

The system SHALL expose a configuration field setting the stall threshold as a duration,
defaulting to 30 minutes. A zero or negative value SHALL disable stall detection
entirely, restoring the prior behaviour in which only the surrounding ctx ends a wait.
The field SHALL appear in the generated configuration JSON schema, and its description
SHALL state that the value must exceed the largest single tool-call budget reachable in
the deployment.

The default MUST exceed the maximum time a healthy subagent can be legitimately silent
under the shipped defaults — bounded by the `bash` foreground hard cap and the default
MCP per-call budget — so that no default deployment kills working tasks. Because the
per-server MCP call timeout has no upper bound, a deployment that raises it above the
stall threshold MUST raise the threshold correspondingly.

#### Scenario: Default threshold exceeds the shipped tool-call ceilings

- **WHEN** no stall threshold is configured
- **THEN** the effective threshold is 30 minutes
- **AND** it is greater than the `bash` foreground hard cap and the default MCP
  per-call budget, so a subagent blocked in one legitimate tool call is never stalled

#### Scenario: Detection can be disabled

- **WHEN** the stall threshold is configured as zero or a negative duration
- **THEN** no task is ever terminated on progress grounds
- **AND** the drain behaves exactly as it did before this capability existed

#### Scenario: Threshold is present in the schema

- **WHEN** the configuration JSON schema is generated
- **THEN** the stall-threshold field is present with a description naming its
  relationship to the largest tool-call budget

### Requirement: A tool-call budget at or above the threshold is reported

Because a subagent is silent for the whole of a blocking tool call, an MCP server whose
resolved per-call budget meets or exceeds the stall threshold means healthy work will be
killed mid-call. The system SHALL report every such server once per process at warning
level, naming the server, its call budget and the threshold. No report SHALL be emitted
when detection is disabled.

This makes the operator-facing constraint checkable rather than documentation-only,
since the per-server call budget has no upper bound.

#### Scenario: An over-budget server is named

- **GIVEN** stall detection enabled with a 30-minute threshold
- **AND** an MCP server configured with a 45-minute per-call budget
- **WHEN** the policy is first constructed
- **THEN** a warning names that server, its budget and the threshold

#### Scenario: Default-budget servers are not reported

- **GIVEN** stall detection enabled with the default threshold
- **AND** MCP servers on the default per-call budget
- **THEN** no such warning is emitted

### Requirement: Coverage is limited to the end-of-turn drain

Stall detection SHALL apply to the non-interactive end-of-turn drain. It is NOT required
to apply to the anti-spin foreground-wait redirect, which waits on the registry directly:
a wedged subagent reached by a model self-poll remains bounded only by the surrounding
ctx. This boundary SHALL be documented so it is not mistaken for coverage.

#### Scenario: The anti-spin wait is not covered

- **GIVEN** a wedged subagent task pending on a session
- **WHEN** the model issues a foreground self-wait that is redirected to the registry
  wait rather than entering the end-of-turn drain
- **THEN** stall detection does not apply to that wait
- **AND** the documented behaviour says so

### Requirement: A stalled subagent task is killed through the existing task-kill path

On detecting a stalled task the system SHALL terminate it using the same mechanism
`taskstop` uses: mark the state terminal, stamp the finish time, signal the task's
completion channel, and invoke its cancel function. The termination SHALL be logged at
warning level naming the task id, the subagent session, and the age of the last
observed progress.

Downstream behaviour MUST be indistinguishable from any other task kill: the pending
wait observes a terminal task and returns for its ordinary reason, the synthetic
completion pair is written into the parent session, and the parent re-enters its
agentic loop.

The completion pair is NOT ordered ahead of the drain's return. Task termination
signals the task's completion channel before the pair is written, so the parent's
first reload after the drain may not yet see it and may cost one additional cycle.
This is the pre-existing ordering of the kill path, which stall detection makes
routine rather than introduces; the step still completes, because the structured
output is retained independently of the reload.

#### Scenario: Stalled task unblocks the drain and the step completes

- **GIVEN** a non-interactive step whose agent has already emitted an accepted
  `struct_output`
- **AND** one async subagent task that has made no progress for longer than the
  threshold
- **WHEN** the task is terminated as stalled
- **THEN** the drain returns because every task is terminal, not because ctx was
  cancelled
- **AND** the parent re-enters the agentic loop and the step completes and routes

#### Scenario: Structured output survives or is superseded, per the existing contract

- **GIVEN** a step that had an accepted `struct_output` before its subagent stalled
- **WHEN** the stalled task is killed and the parent re-cycles
- **THEN** a newly emitted `struct_output` replaces the earlier one
- **AND** if the model emits none, the earlier accepted output is what the step returns

#### Scenario: Termination is observable

- **WHEN** a task is terminated as stalled
- **THEN** a warning-level log entry names the task id, the subagent session id, and
  the age of the last observed progress

### Requirement: The drain imposes no timeout of its own

This capability MUST NOT introduce a deadline into the non-interactive wait. The
`background-tasks` requirement that the wait impose no timeout of its own, with the
surrounding `ctx` as the sole deadline source, remains in force unchanged: the wait
still blocks until every pending task is terminal. Stall detection acts on task
lifecycle, not on the wait, and a stalled task simply reaches a terminal state sooner.

No task that is making progress SHALL be terminated by this capability, regardless of
how long it has been running in total.

#### Scenario: A task making progress is never capped by total runtime

- **GIVEN** a subagent task that has been running far longer than the stall threshold
- **AND** which persists a new message on its session more often than the threshold
- **WHEN** stall evaluation runs repeatedly over its lifetime
- **THEN** the task is never terminated
- **AND** the wait continues until the task finishes on its own or ctx is cancelled

#### Scenario: The wait itself gains no deadline

- **GIVEN** stall detection is enabled
- **AND** a pending task is exempt from stall detection by kind
- **WHEN** the surrounding ctx has no deadline
- **THEN** the wait does not return until that task reaches a terminal state
