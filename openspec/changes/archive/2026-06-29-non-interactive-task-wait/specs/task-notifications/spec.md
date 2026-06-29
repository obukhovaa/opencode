## MODIFIED Requirements

### Requirement: `task.deps.ResumeSession` is naturally suppressed during a non-interactive wait

`task.EnqueueTaskCompletion` calls `deps.ResumeSession(sessionID)` after writing the synthetic Assistant + Tool pair, IF `deps.IsSessionBusy(sessionID)` returns false. The non-interactive end-of-turn wait does NOT introduce any new suppression logic — instead, the existing `IsSessionBusy` check naturally returns true because:

1. The non-interactive `agent.RunWith` invocation that called the model is still in progress. Its goroutine holds the session-busy slot in `agent.activeRequests` from the moment `Run` was called until the goroutine returns.
2. The end-of-turn wait happens INSIDE that same goroutine, between the inner agentic loop's exit and the goroutine's eventual return.
3. While the wait is in progress, any background task that completes invokes `EnqueueTaskCompletion`, which observes `IsSessionBusy=true` and skips `ResumeSession`.
4. The synthetic Assistant + Tool pair is still committed to the message log atomically.
5. The in-flight `agent.RunWith` reloads the message history on its next outer-loop cycle and consumes the synthetic pair as input for the model's next call.

This means there is exactly ONE `agent.Run`-like invocation observing the synthetic completion in non-interactive mode — the original one. No parallel goroutine, no race.

#### Scenario: Background task completing during a non-interactive wait does NOT spawn a parallel Run

- **GIVEN** a non-interactive `agent.RunWith` is waiting for a background bash task at the end of its first inner agentic cycle
- **WHEN** the bash subprocess exits and `bashWaitAndNotify` calls `EnqueueTaskCompletion`
- **THEN** `EnqueueTaskCompletion` MUST call `deps.IsSessionBusy(sessionID)` and observe `true`
- **AND** `deps.ResumeSession` MUST NOT be called
- **AND** the synthetic pair MUST be written to the session
- **AND** the original `agent.RunWith` goroutine's wait MUST unblock and re-enter the agentic loop, picking up the synthetic pair from the reloaded message history

#### Scenario: Background task completing in interactive mode still auto-resumes (unchanged)

- **GIVEN** a TUI agent.Run has returned and the session is idle (no `activeRequests` entry)
- **WHEN** a background task spawned earlier in that session completes
- **THEN** `EnqueueTaskCompletion` MUST observe `IsSessionBusy=false`
- **AND** `deps.ResumeSession` MUST start a fresh `agent.Run` on the session
- **AND** the new assistant message MUST publish to the message broker as today
