## MODIFIED Requirements

### Requirement: Auto-continue on idle session
If the target session is idle (no in-flight `agent.Run` on ANY agent instance — the busy check answers from the process-global session-run ledger, see `session-run-exclusivity`) at the time `EnqueueTaskCompletion` writes its pair, AND the completed task is not flow-owned (see "Flow-owned tasks never auto-resume"), the primitive SHALL start a fresh `agent.Run(ctx, sessionID, "", maxTurnsOverride)` immediately after the write. The empty content argument signals the agent that the new turn input is in the just-written synthetic ToolResult.

#### Scenario: Idle session auto-resumes
- **WHEN** a background bash spawned OUTSIDE a flow step completes and the bound session has no in-flight `agent.Run` on any instance
- **THEN** `EnqueueTaskCompletion` writes the synthetic pair, then invokes `agent.Run` against the same session; the agent observes the new synthetic ToolResult and produces a follow-up Assistant message

#### Scenario: Busy session is NOT re-triggered
- **WHEN** a background monitor fires a `monitor-event` while an `agent.Run` is in-flight on the bound session — on ANY agent instance, including a per-step flow agent the primary instance never sees
- **THEN** `EnqueueTaskCompletion` writes the synthetic pair but DOES NOT start a new `agent.Run`; the in-flight run's next message-list refresh observes the new pair naturally on its next iteration

#### Scenario: Cron preserves its own busy-skip logic
- **WHEN** cron's scheduler invokes `EnqueueTaskCompletion` after the cron's existing session-busy check passed
- **THEN** the primitive proceeds normally; cron is the only caller whose busy semantics are owned externally

### Requirement: `task.deps.ResumeSession` is naturally suppressed during a non-interactive wait

`task.EnqueueTaskCompletion` SHALL continue to call `deps.ResumeSession(sessionID)` after writing the synthetic Assistant + Tool pair, IF AND ONLY IF the completed task is not flow-owned AND `deps.IsSessionBusy(sessionID)` returns false. The non-interactive end-of-turn wait MUST NOT introduce any new suppression logic — the existing `IsSessionBusy` check naturally returns true because:

1. The non-interactive `agent.RunWith` invocation that called the model is still in progress. Its goroutine holds the session's slot in the PROCESS-GLOBAL session-run ledger (`session-run-exclusivity`) from the moment `Run` was called until the goroutine returns — so the answer is true regardless of which agent instance `deps` consults.
2. The end-of-turn wait happens INSIDE that same goroutine, between the inner agentic loop's exit and the goroutine's eventual return.
3. While the wait is in progress, any background task that completes invokes `EnqueueTaskCompletion`, which observes `IsSessionBusy=true` and skips `ResumeSession`.
4. The synthetic Assistant + Tool pair is still committed to the message log atomically.
5. The in-flight `agent.RunWith` reloads the message history on its next outer-loop cycle and consumes the synthetic pair as input for the model's next call.

This means there is exactly ONE `agent.Run`-like invocation observing the synthetic completion in non-interactive mode — the original one. No parallel goroutine, no race — and unlike the per-instance ledger this guarantee holds when the busy observer and the run owner are different agent instances.

#### Scenario: Background task completing during a non-interactive wait does NOT spawn a parallel Run

- **GIVEN** a non-interactive `agent.RunWith` is waiting for a background bash task at the end of its first inner agentic cycle
- **WHEN** the bash subprocess exits and `bashWaitAndNotify` calls `EnqueueTaskCompletion`
- **THEN** `EnqueueTaskCompletion` MUST call `deps.IsSessionBusy(sessionID)` and observe `true` — even when `deps` resolves through an agent instance other than the one running the step
- **AND** `deps.ResumeSession` MUST NOT be called
- **AND** the synthetic pair MUST be written to the session
- **AND** the original `agent.RunWith` goroutine's wait MUST unblock and re-enter the agentic loop, picking up the synthetic pair from the reloaded message history

#### Scenario: Background task completing in interactive mode still auto-resumes (unchanged)

- **GIVEN** a TUI agent.Run has returned and the session is idle (no slot held in the global ledger)
- **WHEN** a background task spawned earlier in that session (outside any flow step) completes
- **THEN** `EnqueueTaskCompletion` MUST observe `IsSessionBusy=false`
- **AND** `deps.ResumeSession` MUST start a fresh `agent.Run` on the session
- **AND** the new assistant message MUST publish to the message broker as today

## ADDED Requirements

### Requirement: Flow-owned tasks never auto-resume

A background task spawned under a flow step's step-scoped context (`tools.StepScopedContextKey` present at spawn time — async task subagents, background bash, monitor) SHALL be marked flow-owned (`task.Task.FlowOwned`). `EnqueueTaskCompletion` SHALL NOT call `deps.ResumeSession` for a flow-owned task's completion — regardless of the session's busy state. Everything else about the completion is unchanged: the synthetic pair is written, terminal statuses transition the registry state and honor the `notified` CAS, and monitor-events remain non-terminal.

Rationale: while the owning step's Run is in flight, the step's own outer loop is the sanctioned reaction mechanism — it blocks in `WaitForActiveTasks` until the pending set is empty, then reloads the message history and re-enters the inner agentic loop for a further model cycle with the synthetic pairs in context. That cycle runs under the step's agent with the step's tools and `output.schema`; `ResumeSession` resolves through the active/primary agent and cannot. After the step has ended, the flow has already routed on the step's result — an auto-resume would start a zombie turn on a completed step's session, and (sessions record no owning agent) it would run under the active/primary agent rather than the step's agent (GENAI-239).

A flow-owned task that outlives its step (a detached `bash`/`monitor` subprocess when the step's Run exits on a timeout or turn-budget exhaustion) therefore has NO consumer for its completion. This is accepted: the pair is still written and remains visible to the transcript, `tasklist`, and any later iteration of the step, and the runtime's wait-timeout note already enumerates the abandoned tasks. The alternative is the zombie turn above.

#### Scenario: Flow-owned bash completion after the step ended

- **GIVEN** a flow step spawned a background bash and the step's Run has since ended (the subprocess outlived the step)
- **WHEN** the subprocess exits and `EnqueueTaskCompletion` fires with a terminal status
- **THEN** the synthetic pair is written and the task transitions terminal state, but `deps.ResumeSession` is NOT called

#### Scenario: Flow-owned monitor event

- **WHEN** a monitor spawned under a step-scoped context fires a `monitor-event` completion
- **THEN** the pair is written and `deps.ResumeSession` is NOT called; an in-flight step run observes the pair via its normal history reload

#### Scenario: Non-flow spawns keep today's behavior

- **WHEN** a background task spawned WITHOUT a step-scoped context (TUI, bridge chat session) completes while its session is idle
- **THEN** `deps.ResumeSession` fires exactly as before

#### Scenario: Unknown or empty task IDs are not flow-owned

- **WHEN** `EnqueueTaskCompletion` is invoked with an empty `TaskID` (cron) or a task ID absent from the registry (e.g. after a registry reset)
- **THEN** the flow-owned gate does not suppress the resume; the busy check alone decides
