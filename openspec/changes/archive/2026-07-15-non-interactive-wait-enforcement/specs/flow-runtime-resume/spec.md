## ADDED Requirements

### Requirement: Flow runner exposes a step-scoped context for detached subagents

The flow step runner SHALL make available a **step-scoped context** that lives for the duration of a single flow step and is bounded by the step's deadline (the `Step.Timeout` value, or the `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` default, or the surrounding orchestrator deadline — same precedence chain used for the non-interactive wait). This context MUST be distinct from the per-turn context that `agent.RunWith` cancels at the end of each agentic turn.

Detached background work spawned during the step — specifically `task async` subagents — MUST derive its context from this step-scoped context (see `task-async-mode`), so that:
- a single turn ending does NOT cancel in-flight subagents, but
- the step's deadline (or the step completing) DOES cancel them.

This replaces the previous behavior in which async subagents ran on an unbounded `context.Background()` and could outlive the step (and even the job) indefinitely.

#### Scenario: Step-scoped context outlives a turn but not the step

- **GIVEN** a flow step with `timeout: 15m` whose agent spawns async subagents
- **WHEN** the agent's current turn ends
- **THEN** the step-scoped context MUST remain live and the subagents MUST keep running
- **WHEN** the step's 15-minute deadline subsequently elapses
- **THEN** the step-scoped context MUST be cancelled and all subagents derived from it MUST be cancelled

#### Scenario: Step without an explicit timeout falls back to the env/orchestrator deadline

- **GIVEN** a flow step with no `timeout` field
- **WHEN** `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` is set
- **THEN** the step-scoped context MUST be bounded by that env default
- **AND** when neither is set, the step-scoped context is bounded only by the surrounding orchestrator context (unbounded within the job)
