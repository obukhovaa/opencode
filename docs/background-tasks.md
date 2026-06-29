# Background tasks in opencode

This document covers the runtime contract for the agent-facing background-task tools (`bash run_in_background`, `task async`, `monitor`, `tasklist`, `taskstop`) and how their lifecycle interacts with the three execution modes: interactive (TUI / chat bridge), non-interactive (flow steps, headless CLI, ACP), and cron-driven.

Spec context: `openspec/specs/background-tasks/`, `openspec/specs/bash-background-mode/`, `openspec/specs/task-async-mode/`, `openspec/specs/monitor-tool/`, `openspec/specs/tasklist-taskstop-tools/`, `openspec/specs/task-notifications/`, `openspec/specs/flow-runtime-resume/`.

## Quick model

When the model calls one of the spawn tools with the background flag:

```jsonc
// bash
{ "tool": "bash", "input": { "command": "make test", "run_in_background": true } }
// task
{ "tool": "task", "input": { "subagent_type": "workhorse", "task_title": "...", "async": true, "prompt": "..." } }
// monitor
{ "tool": "monitor", "input": { "cmd": "tail", "args": ["-F", "/var/log/app.log"], "pattern": "ERROR", "max_events": 1 } }
```

The tool returns immediately with an ack containing a `task_id` and an `output_file` path. The actual work runs in the background. When it terminates, the runtime injects a synthetic `Assistant(ToolCall) + Tool(ToolResult)` pair into the session message log — the same shape a synchronous call would produce, marked with `synthetic: true` so the chat bridge doesn't emit a duplicate 🔧 indicator.

## Two lifecycles, one notification shape

### Interactive mode (TUI, chat bridge)

The agent's `Run` returns as soon as the model emits its terminal turn — the ack tool result was sent, the model wrote "I'll let you know when it finishes", and the turn closes. Later, when the background work completes:

1. `EnqueueTaskCompletion` writes the synthetic pair.
2. `IsSessionBusy(sessionID)` returns false (no `Run` in flight on this session).
3. `task.deps.ResumeSession` kicks off a fresh `agent.Run` on the same session.
4. The new turn's assistant message is published via the message broker and fans out to the TUI / bridge / SSE consumers as a regular assistant reply.

The user (or chat thread) sees a new agent message appear when the work is done. No new wiring; the existing message broker carries it.

### Non-interactive mode (flow steps, headless CLI, ACP one-shot)

The caller invokes `agent.RunWith(ctx, sessionID, content, maxTurns, RunOptions{NonInteractive: true})`. The runtime then:

1. Runs the inner agentic loop as usual — including the model emitting a tool_use for `bash run_in_background` (or `task async`, or `monitor`) and receiving the ack as a tool result.
2. When the model emits its terminal turn, the OUTER loop checks `task.GlobalRegistry().PendingForSession(sessionID, nil)`.
3. If there are pending tasks, the outer loop calls `WaitForActiveTasks(ctx, sessionID, WaitOptions{IncludeMonitor: true})` and blocks the same goroutine until they finish (or the ctx deadline trips — see [Timeouts](#timeouts)).
4. While the wait runs, `IsSessionBusy` continues to return true (the original `RunWith` goroutine still holds `activeRequests`), so `ResumeSession` is naturally a no-op — synthetic completions land in the DB without spawning a parallel agent run.
5. Once the wait returns, the runtime reloads the session's message history (which now contains the synthetic completion pair) and re-enters the inner agentic loop for one more cycle. The model observes the synthetic Tool result and produces a final response that reflects the post-completion state.
6. Only then does `RunWith` return — and the caller (flow runner / CLI / ACP handler) gets the post-completion `AgentEvent`, not the premature ack.

The wait is naturally bounded by the surrounding `ctx`. No internal timeout knob.

## Bounding monitor lifetime

`monitor` is the one tool with indefinite lifetime by design. In non-interactive contexts, the agent must bound it explicitly so the step doesn't block forever:

1. **`max_events: N`** — terminates the monitor after N coalesce windows containing matching events. The canonical "wait for ONE marker line then exit" pattern is `max_events: 1` with a specific pattern (`BUILD_PASSED|BUILD_FAILED`, `READY`, etc.).
2. **Finite-running `cmd`** — `kubectl logs <pod>` without `-f`, `tail -n 200 ...`, etc. The monitor exits when the subprocess exits.
3. **Explicit `taskstop`** within the same agent turn before emitting the terminal turn.
4. **The step's `timeout`** (see [Timeouts](#timeouts)) is the safety net — if none of the above applies AND no upstream event ever fires, the step's deadline cancels the wait and a synthetic timeout note is injected.

The runtime does NOT auto-kill monitors at turn end. Auto-killing would defeat monitor's primary use case (wait for an external pipeline / log event), forcing agents back to `bash sleep` polling loops.

## Timeouts

Three sources, in precedence order:

| Source | Where | Notes |
|---|---|---|
| `Step.Timeout` | Flow YAML `timeout: 15m` on the step. | Highest priority. Cascades into `agent.RunWith`'s ctx via `context.WithTimeout`. Parsed via Go's `time.ParseDuration` — any valid duration string works. |
| `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` env var | Deploy environment. | Fallback default when the step has no explicit `timeout`. Parsed once at process start; SIGHUP-style reloads require a process restart. Malformed / non-positive values are logged and ignored. |
| Unbounded | When neither of the above is set. | The wait is bounded only by the orchestrator's surrounding ctx (e.g. an overall flow deadline). If there is no surrounding deadline, the wait blocks until the work completes or the process exits. |

When the wait returns `ctx.Err()`, the runtime writes a synthetic Assistant text message into the session log enumerating the still-pending task IDs, kinds, `started_at` timestamps, `output_file` paths, and any descriptions. The message has `Synthetic: true` so the chat bridge skips it for outbound indicators; non-bridge consumers (transcript export, SSE replay, the model on any subsequent `agent.Run` on this session) observe it as ambient context.

This means the model can react to a previous step's timeout when a flow is re-triggered. Without this, a re-run on the same session would replay the same dead-end work without knowing why the previous attempt stopped.

## Output files

Every background task writes its full output to `<config.Data.Directory>/tasks/<task_id>.out`. The path is included in the ack response and in the `tasklist` output. The agent can use the `Read` tool to inspect partial progress at any time. Files are swept at opencode boot — there is no per-task cleanup on shutdown because the process owns the data directory.

## What does NOT change in non-interactive mode

- The synthetic-pair shape is identical to interactive mode (`Assistant(ToolCall) + Tool(ToolResult)`, both marked `synthetic: true`).
- The chat bridge integration is unchanged. Flow steps don't go through the bridge; for the rare router-initiated step that IS bound to a chat session, the bridge's existing parts-broker subscriber publishes the post-wait assistant message exactly as it would for a fresh `agent.Run`.
- Cron is unaffected. Cron holds its own session-busy lock during `writeSyntheticMessages`, so `IsSessionBusy` returns true and `ResumeSession` is skipped — preserving the pre-existing "cron writes and moves on" semantics.

## Worked example

```yaml
# .opencode/flows/run-integration-tests.yaml
name: run-integration-tests
description: Run the integration suite against the dev cluster, report results.
flow:
  steps:
    - id: kick-off
      agent: coder
      prompt: |
        Run `make integration-tests` against the dev cluster. The suite takes
        ~10 minutes. Use bash with run_in_background:true so this step
        progresses correctly under the non-interactive wait. When the run
        finishes, produce a struct_output with status=passed|failed and the
        last 50 lines of output.
      timeout: 20m
      output:
        schema:
          type: object
          required: [status, tail]
          properties:
            status: { type: string, enum: [passed, failed] }
            tail:   { type: string }
```

The orchestrator launches the flow. Under the hood:

1. Step kick-off invokes `agent.RunWith(stepCtx, sess.ID, prompt, 0, RunOptions{NonInteractive: true})`. `stepCtx` is `context.WithTimeout(parent, 20m)`.
2. Coder calls `bash run_in_background: true` with `make integration-tests`. The tool returns an ack with a `task_id` like `shell_5KFKDU…` and an `output_file` path.
3. Coder emits `struct_output` with `status: pending, tail: ""` (or it could just emit a brief acknowledgment turn).
4. The inner agentic loop exits with the terminal turn. The outer loop checks pending tasks → finds the bash → calls `WaitForActiveTasks(ctx, sess.ID, …)`.
5. The bash subprocess runs for ~10 minutes. When it exits, `bashWaitAndNotify` writes the synthetic pair (`Assistant(ToolCall bash) + Tool(ToolResult: "<output>\nExit code 0")`).
6. The wait unblocks. The outer loop reloads messages, re-enters the inner loop. Coder sees the synthetic tool result, emits `struct_output { status: passed, tail: "<last 50 lines>" }`.
7. `RunWith` returns the post-completion `AgentEvent`. The flow runner advances to the next step (if any) with the real result.

If the 20m timeout trips before the bash finishes:

1. Wait returns `ctx.Err()`.
2. Synthetic Assistant text message lands: `[wait-timeout] 1 background task(s) did not complete within the step's deadline (context deadline exceeded). - task_id=shell_5KFKDU… kind=bash started=… output_file=… desc="..."  ...`.
3. Outer loop breaks. The step returns its pre-wait `AgentEvent` (likely with `status: pending` or text-only).
4. A re-triggered flow on the same session reads the timeout note in the message history and can decide to wait longer, taskstop the orphan, or abort with a recorded reason.
