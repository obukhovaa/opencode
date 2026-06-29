## MODIFIED Requirements

### Requirement: `run_in_background: true` returns an immediate ack regardless of execution mode

The bash tool's ack semantics are unchanged when called with `run_in_background: true` — the tool returns immediately with `task_id` + `output_file`, the subprocess runs detached, and the per-task monitor goroutine writes a synthetic completion when the subprocess exits.

What changes is **what happens after the model's terminal turn** in non-interactive mode (`agent.RunWith(..., NonInteractive: true)`): the agent.Run loop waits for the bash task to complete and re-enters the agentic loop so the model observes the synthetic completion within the SAME `RunWith` invocation. The agent therefore experiences `run_in_background` in non-interactive mode as effectively per-cycle synchronous, but without the 600s timeout cap that applies to truly synchronous bash.

#### Scenario: Background bash in a flow step delivers completion within the same step

- **WHEN** a flow step invokes the agent and the agent calls `bash run_in_background: true` with a 30-second command
- **AND** the model then emits `struct_output`
- **THEN** `agent.RunWith` MUST wait up to `NonInteractiveTaskWaitTimeout` for the bash subprocess to exit
- **AND** the synthetic Assistant(ToolCall name="bash") + Tool(ToolResult) pair MUST be injected into the session
- **AND** the model MUST be invoked for at least one additional cycle so it can reference the bash output in its final struct_output
- **AND** the flow step's resulting struct_output MUST be the post-completion response

#### Scenario: Background bash in interactive mode is unchanged

- **WHEN** the user types a TUI message and the agent calls `bash run_in_background: true`
- **AND** the agent emits `end_turn`
- **THEN** the TUI MUST observe the agent's turn end immediately
- **AND** the eventual synthetic completion MUST trigger a fresh `agent.Run` via auto-resume, surfacing as a new assistant message in the TUI (today's behaviour)

#### Scenario: Background bash exceeding the wait timeout in non-interactive mode

- **GIVEN** the surrounding ctx carries a 5-minute deadline (from a `Step.Timeout: 5m` field, or the `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` env var)
- **AND** the agent spawns `bash run_in_background` for a command that takes 10 minutes
- **WHEN** the deadline elapses while the wait is active
- **THEN** the wait MUST unblock with `ctx.Err()`
- **AND** the bash subprocess MUST continue running (the runtime does NOT auto-kill bash background tasks)
- **AND** the synthetic Assistant timeout note (see `background-tasks` spec) MUST be injected into the session enumerating the still-pending task IDs and output_file paths
- **AND** `agent.RunWith` MUST return the pre-wait `AgentEvent` so the flow runner can surface the result it has

#### Scenario: Background bash with no step timeout and no env default

- **GIVEN** the step has no `timeout` field
- **AND** `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` is unset
- **AND** the surrounding ctx has no deadline
- **WHEN** the agent spawns `bash run_in_background` for a 30-minute command and emits `struct_output`
- **THEN** the wait MUST block until the bash subprocess exits (no synthetic timeout note)
- **AND** the flow step's resulting struct_output MUST be the post-completion response
