# bridge-flow-progress Specification

## Purpose

Present ongoing conversation workflows clearly across chat platforms without flooding conversations or exposing verbose structured output by default.

## Requirements

### Requirement: One recoverable progress message
Native Slack, Mattermost and Telegram bridges SHALL maintain one editable progress message per flow run and recipient. Progress SHALL show ordered step states and elapsed duration when available. Replayed events MUST NOT regress the displayed state or create duplicate cards after a normal restart. Delivery failures MUST remain retryable and MUST NOT be acknowledged as successful.

#### Scenario: Several steps finish
- **WHEN** a flow emits multiple step transitions
- **THEN** its progress message is edited instead of posting one message per transition
- **AND** completed, active, waiting and failed states remain distinguishable

#### Scenario: Restart and replay
- **WHEN** a process resumes delivery for an existing run
- **THEN** it reuses the stored message identity for that recipient
- **AND** stale events do not overwrite newer displayed progress

#### Scenario: Send or edit fails
- **WHEN** a platform rejects delivery transiently
- **THEN** delivery remains pending and retries preserve the existing card identity

### Requirement: Ended runs settle their plan presentation
For completed, failed and terminated executions, unvisited steps SHALL display as
skipped and unfinished attempts SHALL display as interrupted. This projection
SHALL NOT mutate execution state or count skipped steps as completed. Slack task
statuses SHALL contain no pending or in-progress tasks for an ended execution;
failed or terminated runs SHALL retain an unsuccessful native indicator even
when no individual step carries a failure.

#### Scenario: Branched run completes early
- **WHEN** five of eight steps ran and the completed route bypassed three branches
- **THEN** the plan shows five executed steps and three skipped steps
- **AND** its native progress indicator no longer spins

#### Scenario: Failure can resume
- **WHEN** a failed execution's plan displays unvisited steps as skipped
- **THEN** its stored pending steps remain available to the resumed execution

### Requirement: Outcomes follow conversation interaction
Slack's native editable flow card SHALL place its concise outcome below the plan.
Completion, failure, termination and suspension SHALL additionally post a bounded
result as a new message to the run's original peer or thread after its question
and step messages. Result posting SHALL be separately receipted so a result-post
failure remains retryable after a successful card edit and ordinary replay does
not duplicate a posted result. A stale outcome from an older resumed attempt
SHALL NOT be appended after a newer attempt. The existing crash window between
remote send and local receipt persistence SHALL remain documented.

#### Scenario: Long interactive flow
- **WHEN** a flow finishes after several questions and step messages
- **THEN** its earlier plan settles in place
- **AND** its result is posted as a new message at the end of that conversation

#### Scenario: Result send fails after card update
- **WHEN** the final plan edit succeeds but the result post fails
- **THEN** the event remains pending and retry posts the result without duplicating the card

#### Scenario: Resume overtakes outcome replay
- **WHEN** an undelivered failure or suspension is replayed after the same run resumes
- **THEN** it does not append the obsolete attempt's outcome beneath newer interaction

### Requirement: Verbosity controls flow detail
Conversation flow presentation SHALL honor the existing live verbosity setting. Compact mode SHALL omit routine step output and show concise status and outcome. Full mode SHALL include bounded step details behind platform-native folding. Disabled progress SHALL suppress routine intermediate updates while preserving terminal outcomes, failures and waiting states. Question and steering acknowledgements SHALL keep their existing behavior.

#### Scenario: Verbosity changes during a run
- **WHEN** the user switches from full to compact before a subsequent progress update
- **THEN** that update removes routine detailed output from the card

#### Scenario: Progress disabled
- **WHEN** step progress is disabled and the flow fails
- **THEN** the failure remains visible without emitting routine successful step messages

### Requirement: Platform-native bounded formatting
Slack SHALL render plan/task cards where supported and degrade to an editable readable fallback on unsupported blocks. Mattermost SHALL supply collapsible blocks and a useful text fallback. Telegram SHALL use escaped HTML with expandable quotations for long details. Valid structured output SHALL be pretty-printed; ordinary prose SHALL use supported platform text formatting. Rendering SHALL respect total platform limits, preserve valid Unicode and markup, explicitly indicate truncation and keep failure state visible. Outputs MUST NOT create unintended mentions or executable UI actions.

#### Scenario: Structured step output
- **WHEN** a step returns long JSON in full mode
- **THEN** the output is formatted and folded instead of appearing as an unbroken JSON paragraph

#### Scenario: Unsupported rich blocks
- **WHEN** a platform rejects or disables its rich block capability
- **THEN** a bounded text progress summary remains readable

#### Scenario: Large or hostile output
- **WHEN** output contains Unicode, markup delimiters, mention syntax or exceeds platform limits
- **THEN** rendering remains valid, bounded and non-notifying

### Requirement: Existing delivery contracts remain compatible
External relay flow-event identity, ordering metadata and receipt behavior SHALL remain compatible. Standalone flow execution, flow tool definitions, LLM prompts and native question callbacks SHALL remain unchanged by presentation.

#### Scenario: External relay
- **WHEN** a flow event targets an external relay
- **THEN** the existing event envelope remains available and a failed send is not acknowledged
