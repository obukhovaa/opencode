# dynamic-flow-ownership Specification

## Purpose

Make conversation flow definitions and recovery available to authorized replacement workers without duplicate execution or reliance on one profile filesystem.

## Requirements

### Requirement: Portable durable flow records
The configured session backend SHALL retain the authoritative definition, kind, version, original arguments, conversation association, state, pending question/answer state and delivery identity of a run. Recovery by an authorized replacement worker SHALL NOT require the original host's profile-local definition file.

#### Scenario: Worker migration with shared MySQL
- **WHEN** the original worker exits and an authorized replacement claims its run using the same session backend
- **THEN** the replacement obtains the saved definition and step progress without generating a new definition or requiring the old filesystem

#### Scenario: Legacy file import
- **WHEN** a local legacy manifest is imported
- **THEN** its project/session association is verified and repeated import does not create duplicate runs or overwrite a newer definition

### Requirement: Fenced execution ownership
Only the current run owner SHALL advance execution, acknowledge steering or commit terminal delivery. Ownership changes SHALL invalidate stale workers. Loss of ownership SHALL stop new work; uncertain external effects SHALL be surfaced rather than represented as exactly-once execution.

#### Scenario: Competing workers
- **WHEN** two workers attempt to claim the same recoverable run
- **THEN** exactly one receives current ownership and the other starts no steps

#### Scenario: Expired owner returns
- **WHEN** a previous owner attempts to write completion or acknowledge input after takeover
- **THEN** the write is refused and the current owner's state is preserved

### Requirement: Recovery preserves admission and delivery scope
Recovery SHALL preserve the run's authorized conversation, requester and execution deadline through trusted admission. Duplicate outbound retries SHALL carry stable event identity. Persisted run definitions SHALL NOT contain access tokens or callback secrets.

#### Scenario: Foreign conversation recovery request
- **WHEN** an external caller requests a run belonging to another conversation
- **THEN** admission is refused without disclosing or changing that run

#### Scenario: Terminal delivery retry
- **WHEN** an external relay retries an unacknowledged terminal event after reconnect
- **THEN** its stable identity allows the recipient to deduplicate it without rerunning the flow

#### Scenario: Worker replacement during a question
- **WHEN** a worker exits while a step is waiting for a question and a reply arrives after replacement
- **THEN** the authorized replacement restores the question's logical identity and routes that reply to the same step, without replaying a committed answer or settling a different question
