# Design — session-run exclusivity

## Context

`agent.Service` is instantiated many times per process: `InitPrimaryAgents` builds the primary set, `AgentFactory.NewAgent` caches one instance per flow step, and the task tool builds instances per subagent type. Each instance carried its own `activeRequests sync.Map`, which served two distinct purposes that were never separated:

1. **Mutual exclusion** — `RunWith` refused to start when the session had an entry (but only in *its own* map, and via a non-atomic check-then-store).
2. **Instance bookkeeping** — `Cancel` finds the in-flight cancel func; `IsBusy` answers "is this instance doing anything"; the `-summarize` slot rides the same map.

Purpose (1) is a **process-wide** property; purpose (2) is genuinely per-instance. The bug class fixed here is every caller that asked purpose-(1) questions of a single instance's map: `taskDeps.IsSessionBusy`, cron's `AppBusyChecker`, the bridge dispatcher, and `RunWith` itself.

## Decision 1 — split the ledger, keep the instance map

A new package-level `globalSessionSlots sync.Map` (`session_locks.go`) owns mutual exclusion. `RunWith` claims the slot with a single `LoadOrStore` (atomic — also fixes the intra-instance race the old code documented at `injectDeferredDelta`), stores the cancel in the instance map as before, and releases the global slot as the outermost defer so the instance-map entry never outlives it. The instance map stays for purpose (2) — no churn in `Cancel`'s primary path, `IsBusy`, or the summarize slot.

Alternative considered: make `activeRequests` itself global. Rejected — `IsBusy` ("is *this agent* busy") and the `-summarize` key are instance-scoped semantics; merging them into a global map silently changes the TUI spinner and summarize-cancel behavior.

## Decision 2 — instance methods answer with the global truth

`IsSessionBusy`, `TryLockSession`, `UnlockSession` now operate on the global ledger. This transparently fixes every existing call site (taskDeps, cron `AppBusyChecker`, TUI busy indicator) without changing their code, and keeps the `Service` interface stable. A package-level `SessionBusy(sessionID)` is exported for callers that have no instance at hand (`taskDeps` now uses it directly, dropping the ActiveAgent-nil special case).

Cron's sentinel (`cronLock`) moves into the global ledger with unchanged pairing semantics: `UnlockSession` releases only cronLock-typed holders, so it can never strip a live Run.

## Decision 3 — flow-owned tasks never auto-resume

The busy-check fix alone closes the incident's entry point while the step runs, but a completion can also arrive *after* the step's Run ended (long-lived bash/monitor subprocesses survive the step: they are OS processes, not ctx-bound). An auto-resume then would start a turn on a session whose flow step already routed — a zombie turn, and under `ActiveAgent()` rather than the step's agent (sessions do not record an owning agent, so there is nothing correct to resume *with*).

The spawn paths already know whether they are flow-owned: the flow runner installs `tools.StepScopedContextKey` for exactly this kind of decision. Each spawn path stamps `Task.FlowOwned = StepScopedContext(ctx) != nil`; `EnqueueTaskCompletion` consults the registry by TaskID and returns before the resume branch. Everything before the gate — pair write, `MarkFinished`, notified-CAS dedupe — is unchanged, so the in-flight step drain (`WaitForActiveTasks`) and later history reloads see identical data. Cron's empty TaskID resolves to not-flow-owned; cron holds its own session lock anyway.

Alternative considered: resolve the *owning* agent for the resume instead of skipping. Rejected — sessions carry no agent identity, the factory's step-cache is not addressable by session, and even a correctly-typed resume after step end is still a zombie turn the flow engine never asked for.

## Decision 4 — cross-instance Cancel fallback

`Cancel(sessionID)` on an instance without the entry now falls back to the global slot holder's CancelFunc (skipping cron sentinels by type). This makes the documented abort escape hatch (`POST /session/<id>/abort`, surfaced in the bridge's stuck-session error message) actually work for sessions run by flow-step instances. The fallback fires the cancel but does NOT delete the slot — the owning goroutine's deferred cleanup is the single release point.

## Risks

- **Newly-surfaced ErrSessionBusy.** Callers that previously interleaved silently now get `ErrSessionBusy`. Audited consumers: API message handlers (return 409-style errors), bridge dispatcher (logs + surfaces a chat message with the abort hint), flow `forceStructOutputTurn` (existing 6×50ms retry loop), cron (skip-if-busy by design). All handle it; none relied on the interleave.
- **Global map lifetime.** Slots are released by the Run goroutine's outermost defer, which runs even on panic (RecoverPanic is registered later, so it fires first and cannot block — events channel is buffered). Leak risk is unchanged from the instance map.
