# Tool Loop Detection Specification

## Purpose

Detect stalled tool repetition while allowing useful tool calls and interactive conversations to continue.

## Requirements

### Requirement: Repetition SHALL require identical completed calls and results
The agent SHALL count a per-tool streak only when completed calls have identical requested arguments and model-visible results (type, content and error status), ignoring execution metadata. Changed arguments or results SHALL restart that streak. At the configured threshold, it SHALL report a loop and block later identical requests until reset. Forced terminal tools SHALL remain exempt.

#### Scenario: Repeated dismissed question
- **WHEN** three identical question calls return the same dismissal error with the default threshold
- **THEN** the third result reports a loop and subsequent identical attempts do not execute

#### Scenario: Changed result at the threshold
- **WHEN** two identical reads return one value and the third returns a different value
- **THEN** the third result is delivered without a loop warning and the streak restarts

#### Scenario: Independent tool names
- **WHEN** calls to different tools interleave
- **THEN** each tool's own consecutive completed calls determine its repetition streak

#### Scenario: Timing metadata varies
- **WHEN** three identical calls return the same content and error status but different execution timestamps or durations
- **THEN** the third result still reports a loop

### Requirement: Delivered input SHALL reset repetition history
The agent SHALL reset all tool repetition streaks when queued user or customer input is successfully stored in the session at a tool boundary or natural turn end, or when a question returns an actual user answer. Automatic unattended defaults and unanswered notifications SHALL NOT reset history; actual answers SHALL remain deliverable even at a threshold of one. Empty drains and failed persistence SHALL NOT reset history. Delivery SHALL NOT reset the permission-denial breaker or turn budget.

#### Scenario: Empty question with new messages
- **WHEN** more than three identical question calls are separated by successfully delivered messages
- **THEN** no tool-loop warning interrupts the conversation

#### Scenario: Chat exchange
- **WHEN** router_send and question calls repeat across three exchanges with delivered messages between exchanges
- **THEN** neither tool is stopped for repetition

#### Scenario: Natural turn end
- **WHEN** new input is stored after a text-only turn
- **THEN** the next tool cycle starts with cleared repetition history

#### Scenario: Failed delivery
- **WHEN** an inbox drain cannot persist any input
- **THEN** the existing repetition streak remains in force

#### Scenario: Identical successful answers
- **WHEN** the same question is answered successfully with "Approve" three or more times
- **THEN** each answer reaches the model and resets all repetition streaks
- **AND** a dismissed question returning an error still counts toward repetition

#### Scenario: Automatic defaults are not user input
- **WHEN** an unattended question repeatedly selects the same default without asking a human
- **THEN** its identical completed results count toward loop protection

#### Scenario: Unanswered notification with failed persistence
- **WHEN** a question reports that the user sent messages but those messages fail to persist
- **THEN** the notification alone does not reset repetition history

#### Scenario: User answer at threshold one
- **WHEN** a real question is answered while the configured repeat threshold is one
- **THEN** the answer is delivered without being replaced by a loop warning

### Requirement: Sequential admission SHALL use current repetition state
Each queued sequential call SHALL be checked immediately before execution, after earlier calls in the response have completed. Already-running parallel calls MAY finish; forced terminal tools SHALL remain exempt.

#### Scenario: One response contains many identical sequential calls
- **WHEN** six identical sequential calls are requested at the default threshold and their results are identical
- **THEN** only the first three execute and the remaining calls receive loop warnings

#### Scenario: An earlier answer clears a blocked call
- **WHEN** an actual question answer precedes a previously blocked sequential call in the same response
- **THEN** the later call executes using the cleared repetition state
