## ADDED Requirements

### Requirement: Conversation defaults for named interactive workflows
A named flow started from an authenticated bridge conversation SHALL inherit absent interaction target arguments from that conversation's recorded peer. The effective arguments SHALL satisfy the authored argument schema and be retained for recovery. Explicit targets SHALL NOT be replaced; malformed or unauthorized targets SHALL be refused before the affected step runs. The source flow definition SHALL remain unchanged.

#### Scenario: Chat invocation without reviewer argument
- **WHEN** a Slack, Mattermost, Telegram or external conversation invokes a named flow whose interactive step references a missing target argument
- **THEN** that target resolves to the originating conversation and its question can be answered there without orchestrator argument injection
- **AND** a thread-specific origin retains its thread scope

#### Scenario: Explicit target remains authoritative
- **WHEN** the caller supplies a target argument
- **THEN** the runtime preserves it and enforces the existing authorized-conversation boundary
- **AND** an invalid or foreign target is not silently replaced by the originating peer

#### Scenario: Recovery uses recorded conversation
- **WHEN** an older named run or restored step state lacks the target argument and is explicitly resumed
- **THEN** the runtime resolves it from the run's persisted origin rather than the resuming caller's current conversation

#### Scenario: No authenticated origin
- **WHEN** a named interactive flow has no authenticated bridge origin
- **THEN** the runtime does not invent a target or bypass the existing interaction authorization checks
- **AND** standalone flows retain their existing explicit routing behavior
