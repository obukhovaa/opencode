## ADDED Requirements

### Requirement: Flow steps invoke `agent.RunWith` with `NonInteractive: true`

Every flow step that delegates to the agent SHALL invoke the new `agent.Service.RunWith` entry point with `RunOptions{NonInteractive: true}`. This ensures the agent's end-of-turn wait engages for background tasks the step's agent spawns, so the step's `AgentEvent` (and any `struct_output` it produces) reflects the post-completion state rather than the immediate pre-completion ack.

Headless `cmd/flow.go`, `cmd/acp.go` one-shot invocations, and any other entry point whose lifetime ends with a single `agent.Run` return MUST also set `NonInteractive: true`. Interactive entry points (TUI loop, `internal/bridge/service/dispatch.go`) MUST leave the flag false (the default) so their existing auto-resume semantics are preserved.

#### Scenario: Per-step agent invocation in flow.Service.Run

- **GIVEN** a flow definition with a step whose agent spawns a background task
- **WHEN** the flow runner reaches that step
- **AND** invokes `agentSvc.RunWith(ctx, sessionID, prompt, step.MaxTurns, RunOptions{NonInteractive: true})`
- **THEN** the resulting `AgentEvent` delivered to the flow runner MUST reflect the agent's response AFTER the background task completed
- **AND** the flow runner MUST advance to the next step using that post-completion response

#### Scenario: Headless CLI flow invocation

- **WHEN** the user runs `opencode flow <name>` from the shell (non-TUI mode)
- **AND** the flow's steps use background tasks
- **THEN** the CLI entry point MUST set `NonInteractive: true` on every per-step agent invocation
- **AND** the CLI MUST exit only after every step's background work has completed (subject to the step / env-var timeout chain — see next requirement)

#### Scenario: ACP one-shot agent invocation

- **WHEN** an ACP client triggers a one-shot agent invocation via the ACP server
- **AND** the agent spawns background tasks
- **THEN** the ACP server MUST set `NonInteractive: true` on the `agent.RunWith` call
- **AND** the SSE stream MUST emit the synthetic completion `message.created` events AND the post-completion final assistant message
- **AND** the response delivered to the ACP client MUST be the post-completion state

### Requirement: Per-step `timeout` field bounds the non-interactive wait

The `Step` struct in `internal/flow/flow.go` SHALL gain a `Timeout` field, parseable from step YAML as a Go duration string (e.g. `"15m"`):

```yaml
steps:
  - id: integration-test
    agent: coder
    prompt: "Run the integration suite and produce a struct_output with results"
    timeout: 30m
```

When `Step.Timeout > 0`, the flow runner SHALL wrap the agent invocation's ctx with `context.WithTimeout(parentCtx, step.Timeout)` before calling `agent.RunWith`. The deadline applies to the entire `RunWith` invocation — including the inner agentic loop AND the non-interactive end-of-turn wait — and cascades through to `WaitForActiveTasks`.

#### Scenario: Step.Timeout caps the wait

- **GIVEN** a flow step with `timeout: 30s` whose agent spawns a 10-minute background bash task
- **WHEN** the model emits `struct_output` and the non-interactive wait begins
- **THEN** the wait MUST unblock at the 30-second mark via `ctx.Err()`
- **AND** the synthetic Assistant timeout note MUST be injected into the session
- **AND** the outer agentic loop MUST break and `agent.RunWith` MUST return

#### Scenario: Step without timeout inherits ENV default if set

- **GIVEN** `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT=5m` is set in the process environment
- **AND** a flow step has no explicit `timeout` field
- **WHEN** the flow runner builds the ctx for this step
- **THEN** the runner MUST apply `context.WithTimeout(parent, 5m)` to bound the step
- **AND** the wait inside `agent.RunWith` MUST respect that 5-minute deadline

#### Scenario: Step timeout always wins over ENV default

- **GIVEN** `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT=5m` is set
- **AND** a step has `timeout: 1h`
- **WHEN** the flow runner builds the ctx
- **THEN** the runner MUST apply `context.WithTimeout(parent, 1h)` (the step value)
- **AND** the ENV default MUST be ignored for that step

#### Scenario: Neither step timeout nor ENV default = unbounded wait

- **GIVEN** no `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` is set
- **AND** the step has no `timeout` field
- **WHEN** the flow runner builds the ctx
- **THEN** the runner MUST pass the parent ctx unwrapped (only the orchestrator's surrounding deadline applies)
- **AND** if no orchestrator deadline exists, the wait blocks until the work completes or the process exits

### Requirement: Interactive entry points leave `NonInteractive: false`

The TUI loop, the chat bridge dispatch loop, and the auto-resume callback in `task.deps.ResumeSession` MUST NOT set `NonInteractive: true`. These callers rely on the existing auto-resume semantics where background completions trigger a fresh `agent.Run` that publishes a new assistant message to the broker. Forcing the wait inside these callers would unnecessarily serialize work that the message broker already handles asynchronously.

#### Scenario: TUI agent.Run uses the original 4-arg signature

- **WHEN** the TUI submits user input to the agent
- **AND** invokes `agentSvc.Run(ctx, sessionID, content, maxTurns)` (or `RunWith` with zero-value options)
- **AND** the agent spawns a background task
- **THEN** `Run` MUST return as today (immediately after the inner agentic loop exits)
- **AND** the eventual synthetic completion MUST auto-resume via `task.deps.ResumeSession`

#### Scenario: Bridge dispatch uses the original 4-arg signature

- **WHEN** the chat bridge receives an inbound message
- **AND** invokes `agentSvc.Run(ctx, sessionID, content, maxTurns)` via its dispatch loop
- **AND** the agent spawns a background task
- **THEN** `Run` MUST return as today
- **AND** the eventual synthetic completion MUST auto-resume, with the new assistant message fanned out to the chat platform via the existing parts-broker subscriber
