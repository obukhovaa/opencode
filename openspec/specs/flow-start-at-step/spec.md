# Flow Start At Step

## Purpose

Defines how a flow run starts at a named step with the outputs of the steps before it supplied by the caller (`startAt`, `doneSteps`), and how an interactive step asks a person to accept a supplied output with no model call (`presentSteps`): how supplied outputs are checked and routed before anything is written, how done steps are recorded so a later resume walks past them, and what each answer to a presented question does.
## Requirements
### Requirement: A run can start at a named step with earlier outputs supplied

`flow.RunOptions` SHALL accept `StartAt`, a step id, and `DoneSteps`, a map from step id to output; `POST /flow` SHALL forward `startAt` and `doneSteps` to them. With `StartAt` set, the run SHALL record each done step as a `completed` flow state with the args it would have run with and the supplied output, publish it as completed, and run no agent for it. The run SHALL then enter `StartAt` with the done outputs on its path merged into args.

#### Scenario: Earlier steps are recorded as done and the run enters the start step

- **WHEN** a flow `gather → draft → review → confirm` is started with `StartAt: review` and outputs for `gather` and `draft`
- **THEN** `gather` and `draft` are published and persisted as `completed` with the supplied outputs, no agent runs for them, and `review` runs with both outputs in its args

### Requirement: A supplied output matches its step

A supplied output for a step that declares `output.schema` SHALL be a JSON object. It SHALL be completed as the step's `struct_output` would complete it, with the schema's declared defaults filling absent properties, SHALL then carry every field the schema marks `required`, and SHALL be stored, completed, as the step's struct output. A step without an output schema SHALL take a string, stored as its text output; an object SHALL be refused for it, because its agent cannot produce one.

#### Scenario: A schema step's output must carry its required fields

- **WHEN** `DoneSteps` gives a step that requires `draft` an object without `draft`
- **THEN** the run is refused and nothing is written

#### Scenario: Schema defaults complete a supplied output

- **WHEN** a step's schema declares `blockers` with `default: []` and `mode` with `default: "fast"`, and `DoneSteps` gives it `{"summary": "s"}`
- **THEN** the step is recorded with `{"blockers":[],"mode":"fast","summary":"s"}`, and its rules route on `mode` as they would for its own struct output

#### Scenario: A schema step does not take text

- **WHEN** `DoneSteps` gives a schema-bearing step a string
- **THEN** the run is refused

#### Scenario: A schemaless step does not take an object

- **WHEN** `DoneSteps` gives a step without `output.schema` a JSON object
- **THEN** the run is refused

### Requirement: The supplied outputs route to the start step as a resume would walk them

Before writing anything, the engine SHALL walk from the flow's first step over the supplied outputs, evaluating each step's rules on its output at iteration 1, as the resume planner walks completed rows. Every step the walk reaches SHALL be a done step or `StartAt`; the walk SHALL reach `StartAt`; and every done step SHALL be reached. The first arrival at `StartAt` SHALL decide its args and predecessor. A done step the walk did not reach `StartAt` through, that `StartAt` can route to through any chain of rules or fallbacks, SHALL be refused: recorded as completed, it would never run after `StartAt`.

#### Scenario: A gap before the start step is refused

- **WHEN** a flow `gather → draft → review` is started at `review` with an output for `gather` only
- **THEN** the run is refused, naming `draft`, and no flow state is written

#### Scenario: A stray done step is refused

- **WHEN** `DoneSteps` holds an output for a step the walk to `StartAt` never reaches
- **THEN** the run is refused

#### Scenario: A join on a parallel branch is refused

- **WHEN** a flow `a → [s, c]`, `s → join`, `c → join` is started at `s` with outputs for `a`, `c` and `join`
- **THEN** the run is refused, naming `join`, because `s` routes to it and it would never run

#### Scenario: A parallel branch the start step cannot reach is recorded

- **WHEN** a flow `a → [s, c]` with no join is started at `s` with outputs for `a` and `c`
- **THEN** `a` and `c` are recorded as done and the run enters `s`

#### Scenario: Outputs that route nowhere are refused

- **WHEN** a done step's output makes none of its rules route on
- **THEN** the run is refused because `StartAt` is never reached

#### Scenario: A done step that routes back to itself is refused

- **WHEN** a done step's output routes the step to itself or to a postpone
- **THEN** the run is refused

### Requirement: Start options are refused before anything is written

Every refusal of the start options SHALL wrap `ErrInvalidStartOptions` and SHALL happen before any flow state or session is written.

#### Scenario: Unknown or contradictory options are refused

- **WHEN** `StartAt` or a `DoneSteps` or `PresentSteps` key names no step, `DoneSteps` is given without `StartAt`, `StartAt` is itself in `DoneSteps`, or `StartAt` is combined with `RecoverRunning`
- **THEN** the run is refused with `ErrInvalidStartOptions` and no flow state is written

### Requirement: A start-at run never lands on resumable work

A run with `StartAt` SHALL be refused when the flow's existing state has work a re-trigger would resume, unless `Fresh` is set. A start-at run that later parks SHALL resume like any other run, walking the recorded done steps as completed.

#### Scenario: Refused over a parked step

- **WHEN** a flow has a `postponed` step and is started with `StartAt` and no `Fresh`
- **THEN** the run is refused and the existing state is unchanged

#### Scenario: Fresh discards the parked step

- **WHEN** the same run is started with `StartAt` and `Fresh`
- **THEN** the existing state is discarded and the run enters `StartAt`

#### Scenario: A later resume walks past the done steps

- **WHEN** a run started at `review` with outputs for `gather` and `draft` parks on `review`, and the flow is triggered again with no start options
- **THEN** the re-trigger resumes `review` with the done outputs in its args and no agent runs for `gather` or `draft`

### Requirement: An interactive step can be given an output to present

`flow.RunOptions` SHALL accept `PresentSteps`, a map from step id to a supplied output, a question and an accept label; `POST /flow` SHALL forward `presentSteps` to it. The output SHALL follow the supplied-output rules. The run SHALL be refused when a presented step is not interactive, is also a done step, has no question text, or has an accept label that is not one of the question's options, or when no question service is enabled.

#### Scenario: Presenting a non-interactive step is refused

- **WHEN** `PresentSteps` names a step without `interactive: true`
- **THEN** the run is refused

#### Scenario: Presenting needs the question service

- **WHEN** `PresentSteps` is given and no question service is enabled
- **THEN** the run is refused before anything is written

### Requirement: A presented output is asked about with no model call

On a presented step's first entry in the run, after the interactive bind and the `waiting_for_input` transition, the engine SHALL ask the supplied question on the step's own session through the question service and wait for the answer, with no model call. The engine SHALL post only the question: what the person reviews is whatever the caller puts in it. An answer that is exactly the accept label SHALL complete the step with the supplied output through the normal completion path, and no agent attempt SHALL run.

#### Scenario: Accepting completes the step with no model call

- **WHEN** a run presents `confirm` with the question "Does this look right?" and options Continue and Change something, accept Continue, and the person answers Continue
- **THEN** the question is asked on `confirm`'s own session, `confirm` completes with the supplied output, its rules route the run on, and no agent runs for it

### Requirement: Any other answer hands the presented step to its agent

Any answer other than exactly the accept label, including a multi-select that adds another option, and a dismissed question SHALL run the step's agent as usual. Its first prompt SHALL carry the supplied output, the question asked about it and the answer, ahead of the step's own prompt. A run cancelled while waiting SHALL fail the step, and the step's flow state SHALL be persisted as `failed` even though the run's context is cancelled.

#### Scenario: Another answer hands the step to its agent

- **WHEN** the person answers "Rename the second reason"
- **THEN** the step's agent runs once, its prompt carries the supplied output, the question and that answer ahead of the step's prompt, and the step completes with the agent's output

#### Scenario: A dismissal hands the step to its agent

- **WHEN** the presented question is dismissed
- **THEN** the step's agent runs, told the question was dismissed

#### Scenario: An abort while the question is open fails the step

- **WHEN** the run is aborted while the presented question is unanswered
- **THEN** no agent runs and the step's flow state is persisted as `failed`, so the next trigger does not replay it as a running step

### Requirement: A presentation is used once per run

A presentation SHALL be used on the step's first entry in the run only. A later entry of the same step, by self-loop or cycle, SHALL run the agent as usual. A first entry that continues the step's own unfinished work from an earlier run SHALL NOT ask the question and SHALL run the step's agent as usual, on the step's session. Such an entry is the one the resume planner makes for a step whose row, as it stood before the run wrote anything, is `running`, `postponed`, `waiting_for_input` or `failed`, or for the pending next iteration of a step whose `completed` row routes back to itself. That entry SHALL still use up the presentation, so a later entry of the step in the same run also runs the agent. A step with no row, or with a `completed` row that a re-trigger re-runs from the flow's first step, SHALL present.

#### Scenario: A self-looping step presents once

- **WHEN** a presented step is accepted and its rules route it back to itself
- **THEN** its second iteration runs the agent and the question is not asked again

#### Scenario: A continuation over a declined presentation runs the agent

- **WHEN** a presented step's output was declined, its agent was revising it when the pod was killed, leaving the row `running`, and the flow is run again with `recoverRunning` and the same `presentSteps`
- **THEN** the step's agent runs on the step's session, the question is not asked, and the step completes with the agent's output

#### Scenario: A continuation over a parked presented step runs the agent

- **WHEN** a presented step's row is `postponed` and the flow is run again with the same `presentSteps`
- **THEN** the step's agent runs and the question is not asked

#### Scenario: An interrupted presentation is not asked again

- **WHEN** the pod was killed while the presented question was unanswered, leaving the row `running`, and the flow is run again with `recoverRunning` and the same `presentSteps`
- **THEN** the step's agent runs with the step's prompt, and the presented question is not asked again

#### Scenario: A re-run over a finished run presents

- **WHEN** every row of the flow is `completed` from a run that finished, and the flow is triggered again with `presentSteps`
- **THEN** the run restarts from the first step and the presented step asks its question

