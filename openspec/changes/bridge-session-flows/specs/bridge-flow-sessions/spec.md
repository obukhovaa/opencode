## Purpose

Keep flow execution part of its originating bridge conversation across turns, user interaction and worker lifecycle changes.

## ADDED Requirements

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
Child-step questions SHALL reach only authorized peers of the owning conversation while preserving the requesting session and question identity. Explicit question replies SHALL answer that question; other messages SHALL queue as ordered steering for the next step or iteration boundary. External message IDs SHALL be acknowledged as delivered only after their delivery is committed.

#### Scenario: Real-time question from a child step
- **WHEN** an unbound child step asks a question
- **THEN** its root conversation receives an answerable widget and the answer reaches that exact pending request

#### Scenario: Steering during execution
- **WHEN** a user sends ordinary text while a flow step is running
- **THEN** the text is queued and delivered at the next boundary without starting a competing root turn

#### Scenario: Duplicate external delivery
- **WHEN** the orchestrator retries an accepted message batch or question reply
- **THEN** identifiers prevent duplicate delivery and a reply cannot settle a different replacement question

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
