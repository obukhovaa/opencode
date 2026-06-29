## Context

Background tasks (`bash run_in_background`, `task async`, `monitor`) inject a synthetic Assistant(ToolCall) + Tool(ToolResult) pair into the message log when they complete, and call `task.deps.ResumeSession(sessionID)` to start a fresh `agent.Run` on the session if idle (see the original `background-task-monitor-notify` change). This works for **interactive** sessions because something keeps the session alive — a user typing into the TUI, a Slack thread, an open bridge — long enough for the auto-resumed turn to produce an observable response.

The same pattern fails for **non-interactive** callers:

- `flow.Service.Run` invokes `agent.Run` per step and propagates the resulting `AgentEvent` (with its `struct_output`) back to the flow runner. The runner then advances to the next step. There is no second turn — the auto-resumed `agent.Run` fires onto a session whose orchestrator has already moved on.
- The headless `cmd/flow.go` / `cmd/prompt` / `cmd/acp.go` paths similarly hand the single `AgentEvent` to a caller that returns control to the shell or the SSE consumer.

Prompt-only guardrails ("don't use background mode here") are insufficient because:
- The model can forget.
- Silent failure of a flow step is a *correctness* bug, not a usability nit.
- Even if the prompt is perfect, the agent may legitimately reach for background mode (e.g. because the test it wants to run exceeds the 600s synchronous bash cap), and the runtime should make that work, not break it.

We need a deterministic runtime behaviour: when the caller cannot observe later notifications, `agent.Run` MUST hold the turn open until pending background work completes — so the model has a chance to react to the result inside the same `Run` invocation.

## Goals / Non-Goals

**Goals:**
- Make `bash run_in_background` + `task async` + `monitor` work correctly inside flow steps, headless `prompt`, and ACP one-shot calls. "Correctly" = the call returns AFTER background tasks the agent spawned have completed AND the agent has had a chance to react to them.
- Preserve interactive-mode behaviour byte-for-byte. The TUI and bridge dispatch loops must continue to see the auto-resumed turn as a fresh `agent.Run` invocation, not bundled into the originating one.
- Keep the failure mode safe: a deliberately-hung background task must not block a non-interactive caller forever. Provide a configurable per-cycle timeout.
- No new tool surface. The model doesn't need to know "you're non-interactive, behave differently" — the runtime enforces it.

**Non-Goals:**
- Cross-session waiting (parent waits on tasks from a sibling session). Out of scope for v1.
- A prompt-driven `taskwait` tool. Explicit prompt-level control over waiting is exactly the soft-guarantee model we're moving away from.
- Re-classifying monitor as a foreground tool. Monitor stays event-driven; we just auto-kill stragglers at non-interactive turn end.

## Decisions

### D1. Explicit `nonInteractive` signal on `agent.Run`

`agent.Service.Run` gains an explicit flag (signature change OR new options struct — see D2). Set by the caller. The runtime does NOT sniff `session.ParentSessionID` or `ctx.Value(IsTaskAgentContextKey)` because both can be set in interactive contexts (a TUI user spawning a `task` subagent is interactive, not flow-step-driven).

Callers that set `NonInteractive: true`:
- `flow.Service.Run` — every per-step `agentSvc.Run` call in `internal/flow/service.go`.
- `cmd/flow.go` — when invoking flows from the CLI.
- `cmd/prompt.go` / equivalent headless prompt path.
- `cmd/acp.go` one-shot agent invocation.

Callers that leave it `false` (default):
- TUI loop.
- `internal/bridge/service/dispatch.go` — chat-bridge dispatches.
- Cron scheduler (cron has its own session-busy lock + synthetic-write contract; no wait needed).
- The auto-resume path inside `internal/app/app.go::taskDeps.ResumeSession` — these are always interactive.

### D2. API shape: introduce `RunOptions`

```go
// internal/llm/agent/agent.go
type RunOptions struct {
    NonInteractive bool // hold the turn until pending background tasks complete
    // future: per-call max-turns override, per-call task wait timeout, etc.
}

type Service interface {
    ...
    // Run keeps its existing 4-arg signature (backward-compat for the
    // common interactive callers). RunWith accepts options.
    Run(ctx context.Context, sessionID, content string, maxTurnsOverride int) (<-chan AgentEvent, error)
    RunWith(ctx context.Context, sessionID, content string, maxTurnsOverride int, opts RunOptions) (<-chan AgentEvent, error)
}
```

`Run` internally delegates to `RunWith` with the zero-value options. Flow / CLI / ACP callers switch to `RunWith`. No churn in interactive call sites.

*Alternative considered (rejected):* a context-derived flag. Pro: zero API change. Con: invisible coupling; harder to reason about which callers opt-in; encourages "I'll just set the context flag" workarounds that drift from the explicit intent.

### D3. Registry `WaitForActiveTasks` primitive

```go
// internal/task/registry.go
type WaitOptions struct {
    // IncludeMonitor: wait for monitor tasks too. Default false because
    // monitors are open-ended; callers that wait on them must supply their
    // own deadline via ctx.
    IncludeMonitor bool
}

// PendingForSession returns the snapshot of currently-running tasks for the
// session. Filter is optional (nil = all running).
func (r *Registry) PendingForSession(sessionID string, filter func(*Task) bool) []*Task

// WaitForActiveTasks blocks until every running, non-filtered task for
// sessionID transitions to a terminal state (StateCompleted / StateFailed
// / StateKilled), or ctx is cancelled. Returns ctx.Err() on cancel,
// nil on clean completion.
//
// Internally: snapshot the pending set under the registry lock, then
// select on each task's per-task done-channel + ctx.Done. The done
// channel is closed atomically when MarkFinished / Kill runs.
func (r *Registry) WaitForActiveTasks(ctx context.Context, sessionID string, opts WaitOptions) error
```

Per-task done channels are stored on `*Task` as `done chan struct{}` (created in `Register`, closed exactly once in `MarkFinished` and `Kill`, both already idempotent under the registry lock). Adding this signal is mechanical and doesn't change observable semantics for existing callers.

### D4. End-of-turn wait inside `processGeneration`

The existing agentic loop in `internal/llm/agent/agent.go::processGeneration` exits when the model emits a terminal turn (`end_turn` or `struct_output`). The new behaviour wraps that loop:

```go
agenticLoop:
for outerCycles := 0; outerCycles < effectiveMaxTurns; outerCycles++ {
    // ── existing inner agentic loop runs here ──
    // produces agentMessage, structOutput, finalMsg

    if !opts.NonInteractive {
        break agenticLoop
    }

    reg := task.GlobalRegistry()
    if reg == nil {
        break agenticLoop
    }
    pending := reg.PendingForSession(sessionID, nil)
    if len(pending) == 0 {
        break agenticLoop
    }

    // Wait for ALL pending tasks (including monitor). The caller's ctx
    // carries the deadline — set by the flow runner from step.Timeout,
    // or the OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT env var, or
    // unbounded if neither is set.
    waitErr := reg.WaitForActiveTasks(ctx, sessionID, task.WaitOptions{IncludeMonitor: true})
    if waitErr != nil {
        // ctx cancelled or deadline elapsed. Surface as a synthetic
        // Assistant text message into the session log so the model on
        // any subsequent agent.Run sees the reason and can avoid
        // re-spawning equivalent work.
        a.injectWaitTimeoutNote(ctx, sessionID, pending, waitErr)
        break agenticLoop
    }

    // Reload messages (synthetic completions just landed), filter the
    // historical empty-user-turn corruption, re-enter the inner loop.
    fresh, err := a.messages.List(ctx, sessionID)
    if err != nil { ... return error ... }
    fresh = filterEmptyUserMessages(fresh)
    msgs = fresh
    msgHistory = msgs
    // continue agenticLoop to run another inner-loop cycle
}
```

Key invariants:
- `a.activeRequests` keeps the session marked busy throughout the entire outer loop, so `task.deps.IsSessionBusy(sessionID)` returns true → `ResumeSession` is a no-op → no concurrent `agent.Run` races our wait.
- `outerCycles < effectiveMaxTurns` caps the worst-case loop count.
- If the inner agentic loop hits `effectiveMaxTurns` itself, the outer loop never iterates; behaviour is unchanged.
- No monitor auto-kill. Monitors are included in the wait set on equal footing with bash/task; the agent bounds their lifetime via `max_events`, `taskstop`, or by spawning a finite-running `cmd`. See D5.

### D5. Monitor tasks in non-interactive mode

Monitor's value proposition — "wait for event X to happen in a stream" — is exactly the kind of work flow steps legitimately need to do (e.g. wait for an external CI pipeline to emit a "PIPELINE_PASSED" line). Auto-killing monitors at turn end would force agents to fall back to blocking `bash sleep` loops, which is precisely what monitor was designed to replace.

Decision: monitor tasks are treated identically to bash/task in the non-interactive wait. They are NOT auto-killed. The agent is responsible for using one of the following to bound the monitor's lifetime:

1. **`max_events: N`** — the monitor self-terminates after N matching events. For a flow step waiting on "the build to finish", `max_events: 1` with a pattern like `BUILD_COMPLETED|BUILD_FAILED` is the canonical pattern.
2. **A finite-running `cmd`** — `kubectl logs <pod>` (no `-f`) exits when the pod's log buffer is drained; `tail -n 100 /var/log/app.log` exits after printing.
3. **Explicit `taskstop`** within the same agent turn before emitting the terminal turn — the model can call taskstop in cycle N and emit end_turn in cycle N+1.
4. **The step's `timeout`** (see D6) as the safety net — if the model fails to bound the monitor by any of the above AND no upstream event ever fires, the surrounding ctx eventually cancels the wait and the synthetic timeout note (D4) is injected.

The previous draft of this design proposed `auto-kill at turn end` as a safety net. Rejected because:
- It eliminates monitor's primary use case in flow steps.
- The step `timeout` already provides a safety net at a more appropriate granularity (the entire step, not just monitor cleanup).
- An auto-kill would write a `StatusKilled` synthetic completion AFTER the model's terminal turn, which could re-trigger the outer-loop re-cycle if not careful — invasive for marginal benefit.

Alternative considered (rejected): refuse `monitor` invocations entirely when `NonInteractive: true`. Pro: zero footgun. Con: kills the legitimate use case.

### D6. Wait timeout sources (step → env → unbounded)

There is no per-agent timeout knob. The wait inside `processGeneration` uses only the surrounding `ctx`. The caller sets up that ctx according to a strict precedence chain:

1. **Per-step `timeout` field** (highest priority). `Step` in `internal/flow/flow.go` gains:
   ```go
   type Step struct {
       ...
       Timeout time.Duration `yaml:"timeout,omitempty"` // Go duration string; 0 = unset
   }
   ```
   When `> 0`, the flow runner wraps its agent.RunWith ctx with `context.WithTimeout(parent, step.Timeout)`.

2. **`OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` env var** (fallback default). Process-wide. Parsed as a Go duration string. Applied only when the step has no explicit `Timeout` AND the call site does not already carry a finite deadline.

3. **Unbounded** (when neither is set). The surrounding ctx — supplied by the orchestrator that invoked the flow — is the only ceiling. This matches the principle that "an autonomous orchestrator already controls overall flow time; per-step bounds are an optional finer-grained safety net".

The step `timeout` always wins. If both are set, the env default is ignored for that step. If only the env default is set, every step inherits it. If neither, no per-step bound exists.

The headless CLI / ACP one-shot paths follow the same logic: if the caller supplies an explicit deadline via ctx, use it; otherwise apply the env default; otherwise unbounded.

Rationale: flow-driven execution is the central use case for opencode in headless mode, and step boundaries are the natural unit at which "how long should this take" is known. Per-agent timeouts don't fit because the same agent can run inside flows with vastly different deadlines (a 30s lint vs a 30m integration suite, both using `coder`).

### D7. Synthetic Assistant text message on wait timeout

When the wait returns `ctx.Err()` (timeout or cancellation), the runtime MUST write a synthetic Assistant message to the session log BEFORE breaking the outer loop. Shape:

```
Role: message.Assistant
Parts:
  - TextContent{Text: "[wait-timeout] N background task(s) did not complete within the step's deadline:\n - task_id=<id> kind=<bash|task|monitor> started=<RFC3339> cmd_or_desc=<truncated>\n ... \nThe step's terminal turn above was produced WITHOUT observing these tasks' completions. On any subsequent agent.Run on this session, investigate the per-task output_file before re-spawning equivalent work."}
Synthetic: true
```

The message uses Role=Assistant + TextContent (not a ToolCall pair) because there's no originating ToolCall to mirror — the timeout is a runtime decision, not an agent action. `Synthetic: true` ensures the bridge filter (`if ev.Payload.Synthetic { return }`) skips it for outbound chat indicators; non-bridge consumers (transcript, ACP SSE replay, the model on re-invocation) still observe it.

Why a separate assistant text message and not extending the synthetic-pair shape: the pair shape is `Assistant(ToolCall) + Tool(ToolResult)`. Forcing a fake tool call here would require fabricating a tool name and ID, which doesn't reflect what actually happened. A plain assistant-text turn is honest and renders correctly in every consumer.

### D8. ResumeSession behaviour unchanged

`task.deps.ResumeSession` (in `internal/app/app.go`) is gated on `IsSessionBusy`. During the non-interactive wait, the original `agent.Run` is still running and holds the session-busy slot. Synthetic completions fire `EnqueueTaskCompletion`, which calls `IsSessionBusy` → returns true → `ResumeSession` does NOT spawn a parallel `agent.Run`. The synthetic pair is committed atomically; the in-flight `agent.Run` picks it up on its next inner-loop cycle.

This means the auto-resume code path is "self-disabling" for non-interactive callers — no separate code path needed. Tests should explicitly assert this: a background completion arriving during a non-interactive Run does NOT spawn a parallel goroutine.

### D9. Bridge integration: no new wiring

The chat bridge subscribes to the message broker. In all modes:
- The synthetic completion pair is published as `pubsub.CreatedEvent` for both messages (with `Synthetic: true` on the Assistant — bridge filter suppresses tool-indicators).
- The agent's eventual final assistant message (text reply to the synthetic completion) is published as a regular non-synthetic CreatedEvent — bridge fans it out to chat.

Interactive sessions: the final message lands via the auto-resumed `Run`. Non-interactive sessions: the final message lands within the original `Run` after the wait. Same broker, same bridge subscriber, same outbound chat surface.

The bridge does NOT need to distinguish interactive vs non-interactive. It just publishes new assistant messages, full stop.

### D10. ACP / serve / SSE integration: no new wiring

The HTTP `/event` SSE stream subscribes to the same message/parts brokers (see `internal/api/handler_event.go`). All message creations — synthetic and non-synthetic — flow through. ACP clients see synthetic completions as normal `message.created` events with `synthetic: true` in the payload (already exposed). The post-wait final response arrives as another `message.created`. Existing consumers continue to work.

A non-interactive caller that does `POST /flow/run` and waits on the SSE stream until the flow completes will observe: synthetic pair → final assistant message → step done. No new event types.

### D11. Cron remains unchanged

Cron's `writeSyntheticMessages` already runs under the cron-owned session-busy lock and never invokes `agent.Run` on the bound session itself (cron's "agent run" happens against a separate task-tool subagent session). The non-interactive wait logic doesn't apply.

## Risks / Trade-offs

- **Flow step latency increases.** A step that previously returned in 2s now waits the full background-task duration. This is the explicit point of the change; the caller already gets latency proportional to the work being done. No regression for steps that don't use background tasks.

- **Wait is unbounded by default.** If no step `Timeout` and no env-var default are set, a hung background task blocks the flow step until the orchestrator's overall ctx cancels (or forever, if the orchestrator has no deadline). This is the explicit design choice — flow-driven autonomous execution prefers "let the orchestrator decide" over "guess a default that's wrong half the time". Mitigation: production flows SHOULD set per-step timeouts proportional to the work; the env var provides a process-wide floor for safety-conscious deployments.

- **Monitor used without bounding is a footgun.** A flow step that spawns `monitor` without `max_events`, without a finite cmd, and without taskstop, AND with no step timeout, will block forever. The synthetic Assistant timeout note (D7) provides observability on re-runs once the orchestrator cancels, but it doesn't recover from the first instance. Mitigation: the synthetic timeout note explicitly recommends using `max_events`; the prompt guidance for monitor already mentions max_events; production flows SHOULD set step timeouts.

- **Re-entering the inner loop changes the "shape" of a turn.** A flow step now has TWO inner-loop traversals (the original + the post-wait one) but a single outer `Run` invocation. Tools called in the post-wait cycle count against `effectiveMaxTurns` like any tool call. Mitigation: documented explicitly; max-turns budget is per-Run, not per-cycle.

- **Loop budget exhaustion.** If `effectiveMaxTurns` is 10 and the agent spawns a background task every cycle, the outer loop runs 10 times. Mitigation: cycle accounting is the same as the existing inner-loop accounting; existing maxTurns protections cover this.

- **Memory: per-task done channel.** Adds one `chan struct{}` per task. Negligible (<100 bytes/task).

- **Race detector exposure.** The new done-channel closes are inside the existing `MarkFinished` / `Kill` paths under the registry lock. No new lock-free paths; the race detector run we already do should cover.

## Migration Plan

1. **Add `task.Registry.WaitForActiveTasks` + per-task done channel.** Self-contained; no observable behaviour change.
2. **Add `agent.Service.RunWith` + `RunOptions`.** Keep `Run` as a thin shim. No existing callers change yet.
3. **Implement the outer loop + monitor auto-kill in `processGeneration`.** Gated entirely on `RunOptions.NonInteractive` — interactive paths bypass it. Add unit tests for the wait loop with a mock registry.
4. **Switch `flow.Service.Run` to call `agentSvc.RunWith(..., RunOptions{NonInteractive: true})`.** Run the flow integration tests.
5. **Switch `cmd/flow.go`, `cmd/acp.go`, headless prompt path.** Run the e2e scripts.
6. **Add config field + docs.** Update `cmd/schema/main.go` and `opencode-schema.json` if the timeout is exposed in `.opencode.json`.
7. **Add an e2e check in `scripts/test/background.sh`.** Trigger a flow step that spawns a background bash, verify the step returns *after* the bash completes and surfaces the post-completion struct_output.
8. **Update CHANGELOG and `docs/background-tasks.md`.**

Each step is independent and unit-testable.

## Open Questions

- Should the synthetic timeout note include the `output_file` paths for each unfinished task? Argument for: the model on re-invocation can immediately Read those files to see partial progress. Argument against: paths are already enumerated by `tasklist` if the model asks. Lean: include them — re-runs are exactly when the model needs ambient observability.
- Should `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` be parsed once at process start, or re-read on each `agent.RunWith` to allow runtime tuning via SIGHUP-style reloads? Lean: parse once, document that processes must restart to change it.
- Should `Step.Timeout` ALSO bound interactive flow-step uses (i.e. a flow run from the TUI honoring per-step timeouts), or only non-interactive paths? Lean: yes, bound both. The flow runner can apply `context.WithTimeout` regardless of who invoked it. Caller-side ctx cancellation is a pre-existing surface; this change just adds a way to opt in via YAML.
