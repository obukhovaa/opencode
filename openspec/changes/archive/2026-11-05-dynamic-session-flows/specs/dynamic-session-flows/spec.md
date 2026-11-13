## Purpose

Provide durable, bounded agent-built workflows inside ordinary chat sessions with progress visibility and user control.

## ADDED Requirements

### Requirement: Opt-in bounded primary-agent flow planning
The system SHALL expose flow planning only to primary agents with dynamicFlow.enabled, default false. Optional flowComplexity SHALL accept XS, S, M, L, XL, XXL; explicit configuration SHALL override builder selection. OpenSpec SHALL be required. Plans SHALL preserve the initiating primary agent, permit only configured subagent alternatives, and bound steps, turns, iterations and parallel work.

#### Scenario: Disabled configuration
- **WHEN** an agent has no dynamicFlow setting or is a subagent
- **THEN** it does not receive the flow tool

#### Scenario: Configured complexity
- **WHEN** the builder proposes a plan exceeding the configured size
- **THEN** the plan is rejected before execution

### Requirement: Durable session workflows
The system SHALL persist validated definitions before execution and recover existing step sessions without regenerating plans. Existing standalone execution SHALL retain its behavior.

#### Scenario: Restart recovery
- **WHEN** a user resumes an interrupted flow root
- **THEN** the saved definition resumes from persisted progress

### Requirement: TUI flow visibility and controls
The TUI SHALL recognize /flow, display progress and step agent/status/output, allow cancellation and resume, and show flow roots instead of internal step sessions. Chat SHALL remain responsive and deliver queued steering at step boundaries. Downstream amendments SHALL preserve executed steps.

#### Scenario: User steers a running flow
- **WHEN** the user sends a message during a step
- **THEN** the next step receives the message before starting

#### Scenario: Session picker
- **WHEN** a flow has multiple step sessions
- **THEN** the picker shows its marked root and hides internal steps

#### Scenario: Attended questions preserve permission policy
- **WHEN** a TUI-owned step or its task agent asks a question
- **THEN** the actual question is presented and the root's tool permission policy remains in effect

#### Scenario: Live owner recovery is refused
- **WHEN** another local process owns execution of the same persisted run
- **THEN** recovery is refused without changing its stored state

#### Scenario: Safe downstream amendment
- **WHEN** the pinned primary submits an amended plan
- **THEN** only unentered steps may change and the next boundary uses the saved amendment

#### Scenario: Structured or conditional step skip
- **WHEN** a user requests skip on a step requiring output or conditional routing
- **THEN** skip is refused and the user can resume or terminate instead

#### Scenario: Parallel task limit
- **WHEN** synchronous or asynchronous task starts would exceed the complexity's parallel limit
- **THEN** the additional task is refused without starting another agent
