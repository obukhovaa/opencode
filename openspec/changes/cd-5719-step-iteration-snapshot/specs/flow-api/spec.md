## ADDED Requirements

### Requirement: Status records preserve the persisted step iteration
The flow status response SHALL carry the FlowState iteration as `iteration` on each current and completed step record. The value SHALL be copied from the engine state, never derived from a timestamp or allocated by a read. Zero MAY be omitted for legacy or unknown state. Reading status SHALL NOT change execution, pending questions or routing.

#### Scenario: A status read recovers an execution without its start event
- **WHEN** a running step with persisted iteration 3 is observed and its status is read repeatedly
- **THEN** every current-step record carries iteration 3, including a serialized response used by a reconnecting consumer

#### Scenario: Completed steps retain their iteration
- **WHEN** a step at iteration 3 completes
- **THEN** its completed status record carries iteration 3

#### Scenario: Retrying does not create an execution
- **WHEN** a retrying notification occurs during a step execution
- **THEN** the current step record and its iteration remain unchanged

#### Scenario: A new execution uses the engine iteration
- **WHEN** the same step session begins iteration 4
- **THEN** status carries iteration 4 even when its timestamps equal those of iteration 3
