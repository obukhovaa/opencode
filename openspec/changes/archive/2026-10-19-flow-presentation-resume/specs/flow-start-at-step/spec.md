## MODIFIED Requirements

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
