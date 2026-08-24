# Process-Global Session-Run Exclusivity + Flow-Owned Auto-Resume Suppression

## Why

A production flow run (GENAI-239, `TPWEBAPP-63013` on the `ai-agents` prod deployment, opencode v0.14.9) exposed a hole in the session-busy model: the one-Run-per-session guarantee only ever held **within a single `agent.Service` instance**, because the busy ledger (`agent.activeRequests`) is a per-instance map. The runtime routinely operates many instances — the primary agents, one cached instance per flow step (`AgentFactory` keyed by stepID), and task-subagent instances — and several observers consult the *wrong* instance:

- `taskDeps.IsSessionBusy` / `ResumeSession` (`internal/app/app.go`) resolve via `app.ActiveAgent()` — the primary agent.
- The cron scheduler's `AppBusyChecker` locks and checks sessions via `app.ActiveAgent()` too.
- The bridge dispatcher runs inbound messages on `app.ActiveAgent()`.

Observed failure chain: a flow step (`piano-architect`) was mid-`RunWith` on its session with two async task subagents in flight; an explorer subagent completed; `EnqueueTaskCompletion`'s busy check consulted the primary agent's empty map, observed "idle", and auto-resumed the session — starting a **second concurrent Run under the wrong agent** (`piano-developer`, the primary). That rogue run interleaved the session's message log with the step's in-flight turn (82 "orphaned tool_use" repairs, one empty assistant message that poisoned every later Bedrock replay as a 400-masked-as-RST_STREAM), duplicated ~33 minutes of research on opus-5, and then `taskstop`-killed the step's two legitimate 35-minute task subagents ("context canceled" in Langfuse). The `task-notifications` spec's "No parallel goroutine, no race" reasoning was violated exactly because its premise — one shared busy ledger — was false.

## What Changes

- **Process-global session-run ledger** (`internal/llm/agent/session_locks.go`): a package-level map every `agent.Service` instance claims a session's slot in before starting a Run. `RunWith` acquires it atomically (`LoadOrStore`) — closing both the cross-instance blindness and the pre-existing check-then-store race within one instance. `IsSessionBusy`, `TryLockSession`, `UnlockSession` now read/write this ledger, so busy answers are instance-independent; a new package-level `agent.SessionBusy(sessionID)` exposes the same truth without an instance. `Cancel` gains a cross-instance fallback: when the caller's own instance has no entry, it fires the global slot holder's CancelFunc (cron sentinels are skipped by type), making `/session/<id>/abort` work for flow-step sessions.
- **Flow-owned tasks never auto-resume** (`internal/task`): `task.Task` gains a `FlowOwned` flag, set by every spawn path (async task subagents, background bash, monitor) when the spawn happened under a flow step's step-scoped context (`tools.StepScopedContextKey`). `EnqueueTaskCompletion` skips `deps.ResumeSession` for flow-owned tasks: while the step's Run is in flight, its end-of-turn drain is the sanctioned reaction mechanism (and the — now correct — busy check suppresses resume anyway); after the step has ended, a resume would start a zombie turn on a session whose step already routed, under the primary agent rather than the step's agent.
- `taskDeps.IsSessionBusy` switches from `ActiveAgent().IsSessionBusy` to `agent.SessionBusy` (instance-free). `ResumeSession` keeps its `ActiveAgent()` behavior for the interactive/TUI path — it now only ever fires for non-flow-owned tasks on genuinely idle sessions.

## Capabilities

### New Capabilities

- `session-run-exclusivity`: the process-wide one-Run-per-session invariant — atomic acquisition, instance-independent busy visibility, cron-lock unification, and cross-instance cancel.

### Modified Capabilities

- `task-notifications`: the "Auto-continue on idle session" and "`task.deps.ResumeSession` is naturally suppressed during a non-interactive wait" requirements are re-grounded on the global ledger, and gain the flow-owned suppression rule.

## Impact

- Modified: `internal/llm/agent/agent.go` (RunWith gate, IsSessionBusy, TryLockSession/UnlockSession, Cancel), new `internal/llm/agent/session_locks.go`, `internal/llm/agent/agent-tool-async.go`, `internal/task/task.go` (+`FlowOwned`), `internal/task/background.go` (resume gate), `internal/llm/tools/bash_background.go`, `internal/llm/tools/monitor.go`, `internal/app/app.go` (taskDeps).
- Behavior change: (1) a Run attempt on a session already running under ANY agent instance now returns `ErrSessionBusy` instead of silently interleaving — all existing `ErrSessionBusy` consumers (API handlers, bridge dispatcher surface-message, flow force-turn retry loop) already handle it; (2) background-task completions spawned by flow steps no longer kick an auto-resume Run under the primary agent.
- No config surface changes; no schema changes; no new dependencies.
