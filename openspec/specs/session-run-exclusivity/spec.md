# session-run-exclusivity Specification

## Purpose
Guarantees the one-Run-per-session invariant **process-wide**: at most one `agent.Run`/`RunWith` (or an externally-held lock such as the cron scheduler's) may occupy a session at any moment, regardless of which `agent.Service` instance — primary agent, per-step flow agent, task-subagent instance — attempts the Run. Prevents two concurrent Runs from interleaving one session's message log (orphaned tool_use blocks, empty assistant messages that poison provider replays) and prevents busy-state observers from consulting the wrong instance's ledger (GENAI-239).
## Requirements
### Requirement: Session-run slots are process-global and atomically acquired

The system SHALL maintain a single process-global session-run ledger shared by every `agent.Service` instance. `RunWith` MUST claim the session's slot with one atomic acquire (claim-if-absent) before starting the run goroutine, and MUST return `ErrSessionBusy` without side effects when the slot is already held by ANY holder on ANY instance. The slot MUST be released when the run goroutine exits — on success, error, cancellation, and panic alike — and the release MUST be the last cleanup step, so no per-instance bookkeeping entry outlives the global slot.

#### Scenario: Second instance cannot run a busy session

- **GIVEN** a flow step's agent instance has an in-flight `RunWith` on session S
- **WHEN** a different agent instance (e.g. the active/primary agent, via task auto-resume or the bridge dispatcher) calls `Run` on S
- **THEN** the call returns `ErrSessionBusy` and no second run goroutine starts

#### Scenario: Concurrent claim races produce exactly one winner

- **WHEN** N goroutines concurrently attempt to claim the slot for the same idle session
- **THEN** exactly one acquire succeeds; the rest observe the slot as held

#### Scenario: Slot released after the run ends

- **WHEN** an in-flight run reaches its terminal event and its goroutine exits
- **THEN** the session's slot is free and a subsequent `Run` on any instance may claim it

### Requirement: Busy visibility is instance-independent

`Service.IsSessionBusy(sessionID)` SHALL answer from the process-global ledger, so the answer is identical on every instance. The package SHALL also export an instance-free accessor (`agent.SessionBusy(sessionID)`) for callers that hold no instance (e.g. `taskDeps`).

#### Scenario: Busy check from a non-owning instance

- **GIVEN** session S is running under a per-step flow agent instance
- **WHEN** `taskDeps.IsSessionBusy(S)` (or any other instance's `IsSessionBusy(S)`) is consulted
- **THEN** it returns true — never the false-negative the per-instance ledger produced

### Requirement: External session locks share the same ledger

`TryLockSession` / `UnlockSession` (the cron scheduler's lock) SHALL claim and release slots in the same global ledger, with sentinel-typed holders. `UnlockSession` MUST release only sentinel-typed holders — never a live Run's slot. A held sentinel MUST cause `Run` on any instance to return `ErrSessionBusy`.

#### Scenario: Cron lock excludes runs on every instance

- **WHEN** the cron scheduler holds the lock for session S while committing its synthetic pair
- **THEN** `Run(S)` on any agent instance returns `ErrSessionBusy`

#### Scenario: Unlock cannot strip a live run

- **GIVEN** session S's slot is held by an in-flight Run (a cancel-func holder)
- **WHEN** `UnlockSession(S)` is called
- **THEN** the slot remains held

### Requirement: Cancel reaches runs held by other instances

`Service.Cancel(sessionID)` SHALL, when its own instance holds no entry for the session, fall back to the global slot's holder: if the holder is a live Run's cancel function, it MUST be invoked; sentinel holders MUST be skipped. The fallback MUST NOT release the slot — the owning run goroutine's cleanup is the single release point.

#### Scenario: Abort a flow-step session via the primary agent

- **GIVEN** session S runs under a per-step flow agent instance
- **WHEN** `Cancel(S)` is invoked on the active/primary agent (e.g. by `POST /session/S/abort`)
- **THEN** the in-flight run's context is cancelled, and the slot is released by the owning goroutine's cleanup, not by the canceller

