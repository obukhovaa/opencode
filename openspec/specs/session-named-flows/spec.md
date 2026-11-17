# session-named-flows Specification

## Purpose

Let ordinary sessions run existing YAML flow definitions faithfully through the same tool used for generated flows.

## Requirements

### Requirement: Named flow invocation
The primary-agent flow tool SHALL list authorized existing flows and accept a discovered flow identifier with validated arguments for execution. Running an existing definition SHALL NOT require generating a definition or installing OpenSpec. Disabled, unknown or unauthorized flows SHALL be refused before work starts.

#### Scenario: Run existing YAML
- **WHEN** an authorized free-form session requests an existing flow with valid args
- **THEN** its authored steps, routing and agent settings execute without editing the source YAML or changing the selected conversation agent

#### Scenario: Invalid arguments or forbidden agent
- **WHEN** an invocation supplies invalid arguments or resolves a step agent outside the caller's allowed delegation
- **THEN** execution is refused before that unauthorized step starts

### Requirement: Immutable named-flow execution
A named invocation SHALL retain a validated definition snapshot and original arguments for recovery. Flow-tool amendments to named runs SHALL be rejected. User steering MAY add task context but SHALL NOT rewrite the definition. An entered externally resolved prompt SHALL retain its selected content/version across retries.

#### Scenario: Source changes after start
- **WHEN** the source YAML or included files change or disappear after the invocation begins
- **THEN** recovery uses the recorded snapshot and does not regenerate or reload the graph

#### Scenario: Attempt to amend an authored flow
- **WHEN** the agent requests an update to a named-flow run
- **THEN** the update is rejected and both the saved snapshot and source definition remain unchanged

### Requirement: No nested flow invocation
Flow planning, creation and named execution SHALL be restricted to ordinary primary-agent sessions. A running flow step or subagent SHALL NOT start another flow, including a first standalone step whose session is also the flow root.

#### Scenario: First-step nesting attempt
- **WHEN** the first standalone step calls plan, create or run through the flow tool
- **THEN** the request is rejected before creating definitions, state or background execution

#### Scenario: Sequential top-level runs
- **WHEN** a flow has completed and the ordinary conversation starts another authorized flow
- **THEN** a distinct run is created without modifying the completed run's history

### Requirement: Per-agent named-flow admission policy
A primary agent MAY configure dynamicFlow.namedFlows as exact, case-sensitive flow IDs. An omitted list SHALL preserve existing named-flow delegation checks. An explicit empty list SHALL deny named runs and hide them from listing without disabling generated flows. Named steps SHALL retain their authored agents' grants; admission SHALL NOT claim that those grants are intersected with the initiating agent's tools.

#### Scenario: Explicit empty named-flow list
- **WHEN** an enabled primary agent has namedFlows set to an empty list
- **THEN** list returns no named definitions and run is refused before a run or step session is created
- **AND** generated plan and create operations remain available

#### Scenario: Named access revoked before recovery
- **WHEN** a stored named run's ID is removed from its initiating agent's current allowlist
- **THEN** retrying admission, resuming or recovering that run is refused without advancing its state
- **AND** selecting a different current agent does not bypass the recorded owner's policy
- **AND** the saved definition remains intact for a later authorized resume

#### Scenario: Standalone compatibility
- **WHEN** a standalone CLI or standalone API flow executes
- **THEN** the session-owned named-flow allowlist does not change its existing behavior
