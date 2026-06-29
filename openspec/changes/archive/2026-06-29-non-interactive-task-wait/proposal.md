## Why

The background-task primitives (`bash run_in_background`, `task async`, `monitor`) were designed for **interactive** sessions: the agent dispatches work, returns control to the TUI/bridge, and a synthetic completion later auto-resumes a fresh turn that the user observes. This model breaks for **non-interactive** callers — flow steps that demand `struct_output`, the headless `prompt` CLI, and ACP one-shot invocations — because `agent.Run` returns when the model emits its terminal turn, BEFORE the background work completes. The flow caller receives a premature result, and the auto-resumed turn fires onto a session whose orchestrator has already moved on.

Prompt-level guardrails ("don't use background mode in flow steps") are insufficient — silent failure of a flow step is a mission-critical bug, not a soft warning. We need a deterministic runtime behaviour that holds the turn open until pending background work completes whenever the caller has signalled it cannot react to a later notification.

## What Changes

- `agent.Service` gains a new `RunWith(ctx, sessionID, content, maxTurnsOverride, RunOptions) (<-chan AgentEvent, error)` entry point. `RunOptions{NonInteractive bool}` is set explicitly by callers whose lifetime ends with the `Run` return: `flow.Service.Run` (per flow step), the headless `prompt`/`acp` one-shot entry points. Interactive callers (TUI loop, `bridge/service/dispatch.go`) leave it false. The original 4-arg `Run` remains as a backward-compat shim that calls `RunWith` with the zero-value options.
- The task registry gains `WaitForActiveTasks(ctx, sessionID, WaitOptions) error` returning when ALL in-flight tasks for the session reach a terminal state (or ctx is cancelled). Monitor tasks ARE included in the wait by default — the agent bounds their lifetime via `max_events`, `taskstop`, or by spawning a finite-running `cmd`, mirroring how `bash run_in_background` and `task async` are bounded by the underlying work's natural completion.
- `processGeneration` (the agentic loop in `internal/llm/agent/agent.go`) adds a post-loop step: after the model emits its terminal turn, if `NonInteractive` AND there are pending tasks for the session, the loop calls `WaitForActiveTasks`, reloads message history (synthetic completions land there during the wait), and re-enters the agentic loop ONE additional cycle so the model can react to the completion. Repeats up to `effectiveMaxTurns`.
- `EnqueueTaskCompletion`'s auto-resume path is unchanged. The wait happens inside `agent.Run`, so `IsSessionBusy` returns true while the wait is in progress and `ResumeSession` is naturally suppressed. No race.
- Bridge integration is unchanged. Interactive auto-resume already publishes the new assistant message; the existing parts-broker subscriber fans it out to chat. Non-interactive callers never go through the bridge.
- **Wait timeout is sourced from the caller's ctx, not from any agent config.** Flow steps gain a new `timeout` field on `Step` (Go duration string, e.g. `"15m"`); the flow runner wraps `agent.RunWith`'s ctx with `context.WithTimeout(parentCtx, step.Timeout)`. If no step timeout is set, the `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` env var provides a process-wide default. If neither is set, the wait is unbounded (only the surrounding orchestrator's ctx caps it). Step-level timeout always wins over the env default.
- When the wait returns `ctx.Err()` (timeout or cancellation), the runtime injects a SYNTHETIC Assistant text message into the session log enumerating the still-pending task IDs and a short explanation. The model on any subsequent invocation of the session sees this notice and can avoid re-spawning equivalent work.

## Capabilities

### Modified Capabilities

- `background-tasks`: defines the new `WaitForActiveTasks` registry primitive, the non-interactive lifecycle contract (wait before turn-end return), and the synthetic Assistant text message injected on wait timeout.
- `bash-background-mode`: clarifies that ack semantics are unchanged but the synchronous wait at end-of-turn means the model effectively gets per-turn synchronous completion in non-interactive mode.
- `task-async-mode`: same — async ack still fires, but the parent agent's turn end blocks until the subagent reaches terminal state in non-interactive mode.
- `monitor-tool`: defines that monitor tasks are treated identically to bash/task in the non-interactive wait, AND that `max_events` is the agent's primary mechanism for bounding a monitor's lifetime when used in flow steps. The runtime does NOT auto-kill monitors at turn end.
- `task-notifications`: clarifies that auto-resume via `ResumeSession` is suppressed in non-interactive mode (because `IsSessionBusy` stays true throughout the wait); the synthetic pair still lands in the DB and the in-flight `agent.Run` consumes it on the next cycle.
- `flow-runtime-resume`: flow step runner MUST set the non-interactive flag on every `agent.Run` it spawns, AND honor the per-step `timeout` field by wrapping the agent's ctx with `context.WithTimeout`. Adds the new `Step.Timeout` YAML field.

### Unchanged Capabilities (explicit non-changes)

- `chat-bridge`: bridge integration is unchanged. The existing message broker subscriber publishes synthetic-Assistant + agent's-final-reaction messages to chat as it does today.
- `bridge-http-api`, ACP SSE: synthetic completions and post-wait assistant messages stream through the existing `message.created` / `message.part.updated` events.
- `cron-tool`: cron's session-busy lock + synthetic write semantics are unchanged. Cron never spawns background bash/task/monitor itself.

## Impact

**Code**
- `internal/task/registry.go`: new `WaitForActiveTasks` + per-task done channel signalling on `MarkFinished` / `Kill`.
- `internal/llm/agent/agent.go`: new `RunWith(ctx, sessionID, content, maxTurnsOverride, RunOptions)` entry point + `RunOptions{NonInteractive bool}`. `Run` becomes a shim calling `RunWith` with zero options. New end-of-turn wait + re-cycle loop in `processGeneration`. Wait respects the surrounding `ctx`; no agent-config timeout knob.
- `internal/flow/flow.go`: new `Step.Timeout time.Duration` field parsed from `timeout: <duration>` in step YAML.
- `internal/flow/service.go`: per-step ctx is wrapped with `context.WithTimeout(parent, step.Timeout)` when `step.Timeout > 0`; otherwise the process-wide default (env var) is applied if set; otherwise the surrounding ctx is passed unwrapped. Then `agent.RunWith(..., RunOptions{NonInteractive: true})`.
- `cmd/flow.go`, `cmd/acp.go`, headless `prompt` entry: pass `NonInteractive: true` for one-shot invocations and honor the env-var default when the call site has no explicit deadline.
- `internal/llm/agent/agent.go`: on wait timeout, write a synthetic Assistant text message (role=Assistant, parts=TextContent, `Synthetic: true`) enumerating still-pending task IDs and a short explanation, then break the outer loop.
- No per-agent config field. The wait timeout is governed entirely by the caller's ctx, which the caller derives from step.Timeout / env var / surrounding deadline.

**APIs**
- `agent.Service.Run` signature gains an options parameter (or a new `RunWith` variant) — backward-compatible if introduced as a new method; otherwise a touch on every caller.
- `task.Registry` interface gains `WaitForActiveTasks` and `PendingForSession`.
- No changes to tool input schemas; no changes to the synthetic-pair shape.

**Operational**
- Flow steps that previously returned prematurely now correctly wait. Total flow-step latency increases by however long the background work takes — by design.
- The per-cycle wait timeout (5min default) sets the worst-case latency cap. Mis-tuned workflows hit it and surface a "tasks still running" note to the model.
- Interactive paths (TUI, bridge, cron-fired) are unaffected — `NonInteractive: false` skips the new wait entirely.

**Testing**
- Unit test for `WaitForActiveTasks`: closes promptly when all pending tasks transition; respects ctx cancellation; respects timeout.
- Integration test for `processGeneration` in non-interactive mode: bash `run_in_background` followed by `struct_output`; verify the returned struct_output is the *post-completion* response, not the premature one.
- Integration test for monitor auto-kill at turn end in non-interactive mode.
- Regression test for interactive mode: same flow without `NonInteractive` flag returns immediately (no wait).
- Extension of `scripts/test/background.sh` to cover the non-interactive end-of-turn wait path.

**Non-goals (deferred)**
- Cross-session task waiting (e.g. a parent flow step waits on tasks spawned by an unrelated session) — out of scope; current model is per-session.
- A "wait for specific task by id" primitive exposed to the model — not needed; the runtime handles the wait deterministically without prompt involvement.
- Forcibly killing background subprocesses on ctx cancellation — the wait unblocks but the spawned subprocesses continue. ctx cancellation cascading into `cmd.Wait` will reap them in practice for processes that respond to SIGPIPE / EOF; resilient hangs require an explicit `taskstop` from the agent or a separate cleanup pass. Track as follow-up if it surfaces in production.
- Per-agent or per-tool override of the non-interactive wait behaviour — the precedence chain (step.Timeout → env var → unbounded) is enough surface area for v1. Per-agent overrides can be added later if a single deployment needs heterogeneous timeouts across the same flow.
