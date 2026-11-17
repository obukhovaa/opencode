# bridge-flow-sessions Specification

## Purpose

Keep flow execution part of its originating bridge conversation across turns, user interaction and worker lifecycle changes.

## Requirements

### Requirement: Flow-owned conversation lifetime
An authorized primary agent in a free-form direct or external bridge session SHALL be able to start a top-level flow using the flow tool. The conversation SHALL remain associated with that run until terminal or explicit suspended state; completion of the submitting turn MUST NOT imply completion of the flow. At most one nonterminal run SHALL own a conversation.

#### Scenario: Direct bridge flow survives submitting turn
- **WHEN** a Slack or Telegram primary submits a flow and finishes its turn
- **THEN** flow progress and its terminal result continue to reach the original conversation

#### Scenario: Generated planning through the bridge
- **WHEN** a bridge conversation requests a generated workflow
- **THEN** its primary planner receives the same bundled authoring guidance, authorized agent inventory and effective complexity limits as TUI planning before the definition is submitted

#### Scenario: Conflicting flow start
- **WHEN** a new invocation tries to start a second flow while the conversation already has an active or suspended run
- **THEN** it is rejected without starting work, while a retry of the original invocation returns its existing run identity

### Requirement: Shared generated planning admission
The primary-agent flow tool SHALL accept a goal through its plan operation and provide the shared builder with bundled authoring guidance, authorized agents and effective complexity policy after the submitting turn yields. Configured complexity SHALL take precedence over requested or automatically selected complexity. Existing create/update submissions SHALL remain compatible. Plan and create SHALL produce generated-source runs; named run SHALL bypass the builder. All start operations SHALL enforce the same top-level-only admission.

#### Scenario: Plan a user goal
- **WHEN** an authorized ordinary primary invokes plan with a goal
- **THEN** the generated planning invocation is durably identified before the builder starts, and its accepted definition is stored before any execution step runs

#### Scenario: Configured complexity wins
- **WHEN** plan requests a different complexity from the agent's configured value
- **THEN** the configured value governs planning and runtime validation

### Requirement: Bound questions and steering
Child-step questions SHALL reach only authorized peers of the owning conversation while preserving the requesting session and question identity. Explicit question replies SHALL answer that question; other messages SHALL queue as ordered steering for the next step or iteration boundary. When a question is pending, queued steering SHALL instead close it as unanswered and be delivered to that same step at the interaction boundary, without inventing an answer. External message IDs SHALL be acknowledged as delivered only after their delivery is committed.

#### Scenario: Real-time question from a child step
- **WHEN** an unbound child step asks a question
- **THEN** its root conversation receives an answerable widget and the answer reaches that exact pending request

#### Scenario: Steering during execution
- **WHEN** a user sends ordinary text while a flow step is running
- **THEN** the text is queued and delivered at the next boundary without starting a competing root turn

#### Scenario: Duplicate external delivery
- **WHEN** the orchestrator retries an accepted message batch or question reply
- **THEN** identifiers prevent duplicate delivery and a reply cannot settle a different replacement question

#### Scenario: Steering interrupts a pending question
- **WHEN** ordinary prose or an id-bearing batch arrives while a flow question is waiting
- **THEN** the question is closed as unanswered, the same step receives the committed input, and delivery is acknowledged only after persistence
- **AND** explicit buttons, selected option labels/numbers, request-addressed replies and explicit custom-answer submissions remain answers rather than steering

### Requirement: Surface controls and mediated ownership
Stop, resume and terminate controls SHALL operate on the owning run and its work. An external worker SHALL remain assigned until the run is terminal or explicitly suspended. Lifecycle events SHALL identify the root conversation, run and step, and preserve requester attribution without exposing credentials.

#### Scenario: Stop after initial turn ended
- **WHEN** a bridge user stops a flow after the submitting agent turn has finished
- **THEN** the controller cancels active flow work and reports its actual state to the conversation

#### Scenario: External worker ownership
- **WHEN** a mediated worker starts a flow from an ordinary session
- **THEN** the orchestrator receives a capability-compatible run lifetime and does not release the worker on the submitting turn's completion

#### Scenario: Unauthorized conversation target
- **WHEN** a flow or its arguments request delivery to another external conversation
- **THEN** delivery is refused and the existing conversation boundary remains intact

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
