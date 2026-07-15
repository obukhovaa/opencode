## Why

The `non-interactive-task-wait` change (archived 2026-06-29) introduced a deterministic runtime guarantee: an agent running under `RunOptions{NonInteractive: true}` (flow step, headless `prompt`, ACP one-shot) holds its turn open until pending background tasks (`bash run_in_background`, `task async`, `monitor`) reach a terminal state, so the model reacts to their completions inside the same `Run` and the flow step's `struct_output` reflects the finished work.

Production incident **CD-4761** proved the guarantee is **bypassable**. The wait (`internal/llm/agent/agent.go:910-934`) is reached ONLY after the inner agentic loop breaks on a non-tool-use finish reason (`agent.go:902`). On `FinishReasonToolUse` the loop `continue`s (`agent.go:894`) and control returns to the model after every tool cycle. A model that never emits a bare terminal turn never reaches the wait.

In CD-4761 the `implement-with-openspec` step spawned ~20 `task async` workhorses, then busy-waited with foreground `bash "sleep 300; echo done"` + `read <task>.out` for ~30 minutes. Each incoming synthetic completion SIGTERM-killed the running sleep (exit 143), which the model rationalized ("the bash sleep keeps getting aborted (likely by incoming notifications)") and kept polling. It never emitted `struct_output`, so the deterministic wait never fired — no `[wait-timeout]` note was ever injected — and the step consumed its entire budget until its node died. The notification WRITE path worked perfectly (21 synthetic pairs landed in the parent session); what failed is that **nothing forced the parent to block on them**, and nothing neutralized the model's instinct to self-spin with `sleep`.

Two structural facts compound the gap:
1. `NonInteractive` lives only in the agent's `opts` (`agent.go:89`); it is NOT propagated into the tool-execution `ctx`, so the `bash` tool cannot currently make a non-interactive-aware decision.
2. The only prompt text forbidding polling lives in `CoderPrompt` (`internal/llm/prompt/coder.go`), which is skipped entirely whenever an agent supplies a custom system prompt via `info.Prompt` (`prompt.go:317`) — as `composer-developer` does. So the deployed flow-step agent never saw the "do NOT poll" contract; the `task async` ack even advertises an `output_file:` path.

We need runtime enforcement that makes wall-clock self-waiting impossible while background tasks are pending in non-interactive mode, plus supporting robustness (bound detached subagents to the step deadline, drain all tasks across turns, and deliver the no-poll contract regardless of the agent's custom prompt).

## What Changes

- **Anti-spin (core).** In non-interactive mode, while the session has pending non-monitor background tasks, the `bash` tool SHALL NOT execute a foreground wall-clock wait. A foreground command whose sole effect is to sleep (leading `sleep`, optionally followed by a trivial `echo`) is intercepted and converted into a deterministic `WaitForActiveTasks(ctx)` drain; the tool returns a synthetic result enumerating the tasks that completed during the wait. The model's poll instinct thus converges on the correct runtime wait instead of burning wall-clock. This requires propagating the non-interactive signal into the tool-execution context (the session id already flows to tools via `GetContextValues`). Long-lived monitors are excluded from the redirect (they are bounded by the end-of-turn drain, not a mid-turn sleep).
- **Drain-to-empty.** The non-interactive end-of-turn wait re-checks pending tasks after each `WaitForActiveTasks` return and repeats until the session has no pending tasks (or ctx cancels), covering tasks the model spawned across multiple turns. The registry primitive keeps its snapshot-at-start semantics; the drain loop lives in the agent.
- **Bounded detached subagents.** `task async` subagents no longer run on `context.Background()` (`agent-tool-async.go:46`). They inherit a step-scoped context that survives the parent's per-turn cancellation but is bounded by the caller's deadline (`Step.Timeout` / `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT`). A step that times out cancels its detached subagents instead of leaking ~20 unbounded runs.
- **No-poll contract reaches every agent.** The "background tasks are event-driven; do NOT poll/sleep" guidance is delivered independent of the agent's custom system prompt. The `task async` ack stops presenting `output_file` as a progress-polling target and states that the runtime enforces the wait.

## Capabilities

### Modified Capabilities

- `background-tasks`: the non-interactive hold-the-turn guarantee is strengthened to be non-bypassable (anti-spin) and to drain all pending tasks across turns; adds a requirement that no-poll guidance is delivered regardless of the agent prompt.
- `bash-background-mode`: defines foreground-`sleep` interception in non-interactive mode when tasks are pending, and the non-interactive signal propagated into the tool context.
- `task-async-mode`: detached subagents inherit a bounded step-scoped context (not `context.Background()`); ack format no longer frames `output_file` as a poll target and states the runtime enforces the wait.
- `flow-runtime-resume`: the flow runner exposes a step-scoped context (living for the whole step, bounded by `Step.Timeout`) usable for detached subagents.

### Unchanged Capabilities (explicit non-changes)

- `task-notifications`: the `EnqueueTaskCompletion` write path, synthetic-pair shape, `synthetic` flag, and `IsSessionBusy`-gated auto-resume are correct and unchanged. This change does NOT touch delivery — only enforcement.
- `monitor-tool`, `tasklist-taskstop-tools`, `cron-tool`, `chat-bridge*`, `bridge-http-api`: unchanged.
- Interactive callers (TUI, bridge dispatch): `NonInteractive: false` skips all of the above; foreground `sleep` still runs normally; behavior is byte-for-byte identical.

## Impact

**Code**
- `internal/llm/agent/agent.go`: propagate `opts.NonInteractive` into the tool-execution ctx (sessionID already flows via `GetContextValues`); wrap the existing `WaitForActiveTasks` call in a drain loop that re-reads `PendingForSession` until empty or ctx done; plumb a step-scoped ctx into the async spawn path.
- `internal/llm/tools/bash.go` / `internal/llm/tools/shell/shell.go`: in the foreground `Run` path, when the ctx marks non-interactive AND `reg.PendingForSession(sessionID)` has non-monitor tasks AND the command is a pure wait (leading `sleep`), skip `sh.Exec` and instead call `reg.WaitForActiveTasks(ctx, sessionID, WaitOptions{IncludeMonitor: false})`, returning a synthetic bash-style result summarizing completed tasks. (Foreground bash cannot exceed `MaxTimeout` 10min — `bash.go:57` — so today's polling sleeps are already capped/killed by the shell; interception replaces the doomed sleep with a correct, deadline-bounded wait.)
- `internal/llm/agent/agent-tool-async.go:46`: derive `runCtx` from the step-scoped ctx instead of `context.Background()`; keep `cancel` for `taskstop`.
- `internal/flow/service.go`: expose a step-scoped ctx (bounded by the existing `stepCtx` timeout) that outlives a single turn's ctx, and hand it to the agent for detached subagents.
- `internal/llm/prompt/prompt.go`: always append the background-tasks no-poll section (move it into an always-appended block such as `taskToolReportingPrompt`, or append unconditionally after `info.Prompt` is applied at `prompt.go:317`).
- `internal/llm/agent/agent-tool-async.go:79-82`: reword the ack; drop the framing of `output_file` as a progress-poll target.

**APIs**
- No tool-schema changes; no synthetic-pair shape change. New internal plumbing: a non-interactive marker on the tool ctx (sessionID is already available to tools), and a step-scoped ctx passed to the async spawn.
- `task.Registry` interface unchanged (`PendingForSession` / `WaitForActiveTasks` already exist).

**Operational**
- Flow steps that fan out async subagents now block deterministically and are bounded by the step timeout; the ~30-minute self-poll failure mode is eliminated. A hung batch is capped by `Step.Timeout` and surfaces the existing `[wait-timeout]` note.
- Interactive paths (TUI, bridge, cron-fired) are unaffected.

**Testing**
- Unit: bash sleep-interception fires only when (non-interactive ∧ pending tasks ∧ pure-sleep command); passes through normally otherwise and in interactive mode.
- Unit: drain loop across two spawn waves returns only when the session has zero pending tasks.
- Unit: async subagent ctx is cancelled when the step-scoped ctx deadline elapses; NOT cancelled merely by the parent turn ending.
- Unit: prompt assembly includes the no-poll section for a custom-prompt (`info.Prompt != ""`) agent.
- Regression: `NonInteractive: false` still executes foreground `sleep` verbatim.
- Extend `scripts/test/background.sh` with a non-interactive sleep-interception assertion.

**Non-goals (deferred)**
- Detecting arbitrary poll patterns beyond a leading `sleep` (e.g. tight `tasklist` / `read .out` loops — those return immediately and do not burn wall-clock; if a model spins on them it still eventually emits a terminal turn and hits the drain wait).
- Cross-session waiting; hard-killing unresponsive subprocesses on ctx cancel (the cancel signals them; a reaper remains a separate follow-up).
- Any change to the notification write path or synthetic-pair shape.
