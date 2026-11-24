# Background tasks in opencode

This document covers the runtime contract for the agent-facing background-task tools (`bash run_in_background`, `task async`, `monitor`, `yield`, `tasklist`, `taskstop`) and how their lifecycle interacts with the three execution modes: interactive (TUI / chat bridge), non-interactive (flow steps, headless CLI, ACP), and cron-driven.

Spec context: `openspec/specs/background-tasks/`, `openspec/specs/bash-background-mode/`, `openspec/specs/task-async-mode/`, `openspec/specs/monitor-tool/`, `openspec/specs/yield-tool/`, `openspec/specs/tasklist-taskstop-tools/`, `openspec/specs/task-notifications/`, `openspec/specs/flow-runtime-resume/`.

## Quick model

When the model calls one of the spawn tools with the background flag:

```jsonc
// bash
{ "tool": "bash", "input": { "command": "make test", "run_in_background": true } }
// task
{ "tool": "task", "input": { "subagent_type": "workhorse", "task_title": "...", "async": true, "prompt": "..." } }
// monitor
{ "tool": "monitor", "input": { "command": "tail -F /var/log/app.log", "pattern": "ERROR|FATAL", "max_events": 1 } }
// When there is no independent work left:
{ "tool": "yield", "input": {} }
```

Each spawn tool returns immediately with an ack containing a `task_id` and an `output_file` path. The actual work runs in the background. A monitor reports matching lines while it runs; every task reports its terminal outcome. The runtime injects each report as a synthetic `Assistant(ToolCall) + Tool(ToolResult)` pair into the session message log, marked with `synthetic: true` so the chat bridge doesn't emit a duplicate 🔧 indicator.

## Working and waiting

Continue independent work after the acknowledgment. Reports are added to the model's
history at tool boundaries, after the current tool-call/result batch completes.
When nothing independent remains, call `yield` alone with `{}`. The runtime resumes
the same session when a report or queued user message is available. No success claim
or `struct_output` is required to wait, and yielding does not cancel tasks.
If the agent's tool allowlist excludes `yield`, end the turn instead; the same
mode-specific release or wait behavior applies.

In TUI and bridge sessions, yielding releases the run just like a normal `end_turn`.
User input can start the next turn, and background reports use the existing idle
auto-resume path. In non-interactive runs, yielding keeps the run open and suspends
model generation until the first report or inbox message. It continues immediately
if a report is already available or there are no active tasks. All flow steps use
`NonInteractive: true`, including interactive steps that accept reviewer input.
Yielding does not finish the flow step; the surrounding run/step deadline bounds
the wait.

Read completion reports already in context before yielding. Once the session has
no active background tasks, `yield` says so and generation continues immediately;
process the results instead of yielding again or checking `tasklist` merely to
confirm completion. Yield observes registered work in the current session. To
wait for a remote CI job, start a finite background waiter that exits on either
success or failure. Reviewer interaction follows the active session or interactive
flow step's own instructions; the background-wait guidance does not initiate it.
The presence of a question tool alone does not establish a reviewer binding.

With Langfuse tracing enabled, flow/headless waits emit a `background_wait` span
separately from the short `yield` tool span. Its numeric `suspended_ms` metadata
counts only time blocked waiting for events, excluding model generation,
compaction, report reads and question-tool time. `wake_reason` separates
`background_report`, `inbox`, `no_pending_tasks`, `cancelled`, `deadline`, `error`
and `unavailable`; `reports` counts delivered report pairs. `trigger` is `yield`
or `end_turn`. `pending_tasks` and `pending_task` / `pending_bash` /
`pending_monitor` describe the active work at entry. A report ready at the yield
boundary records zero suspension. Ordinary end turns with no pending work do not
emit wait spans. Interactive turn release is not an in-run suspension.

For agent-job efficiency, group `background_wait` observations by `trigger` and
`wake_reason`, filter `pending_task > 0` for subagent waits, and aggregate
`suspended_ms`. Zero-suspension `no_pending_tasks` yields identify stale wait
requests; a zero-suspension `background_report` means a result was already ready.
Neither question-tool latency nor the entire following generation belongs in
background waiting time.

A streaming monitor can wake the session on an intermediate event while its process
continues running. Handle the event, continue work, then yield again if needed.
Use `taskstop` when the watch is no longer useful, never merely because it is silent.
Normal end-of-turn waiting remains supported for non-interactive runs.

Permission rejection, cancellation and error finish reasons end the run immediately,
even when reports or reviewer input are waiting. They do not enter the background
wait or start an auto-resume when the stopped turn releases its slot. Unseen reports
remain in history and queued reviewer input stays available for a later run.
Output-token truncation (`max_tokens`) retains the existing background re-entry behavior.

For one outcome, use background bash with a finite command that exits on success or
failure. A polling loop **inside** that background script may sleep between checks;
it uses no model calls while waiting. Use monitor for multiple events or filtered
streams; `max_events: 1` also supports a single marker. Cover failure as well as
success so a failed job cannot leave a success-only watch silent forever.

`monitor` accepts either a shell `command` (executed by `bash -c`, like background
bash) or the legacy direct executable `cmd` with an `args` array. Mixing the modes
is rejected. `pattern` defaults to `.` (every nonempty line), uses RE2, and matches
merged stdout **and stderr**. All output is retained in `output_file`. Keep filters
selective and enable line buffering in pipeline programs. Existing `cmd`/`args`
callers and monitor permission rules remain supported; shell mode checks the full
command string under the `monitor` permission key.

## Two lifecycles, one notification shape

### Interactive mode (TUI, chat bridge)

The agent's `Run` returns when the model emits a terminal turn or calls `yield`.
It releases the session's run slot so user input stays responsive. When background
work reports an event or completes:

1. `EnqueueTaskCompletion` writes the synthetic pair.
2. `IsSessionBusy(sessionID)` returns false (no `Run` in flight on this session — the check reads the process-global session-run ledger, so a run held by ANY agent instance counts).
3. `task.deps.ResumeSession` kicks off a fresh `agent.Run` on the same session.
4. The new turn's assistant message is published via the message broker and fans out to the TUI / bridge / SSE consumers as a regular assistant reply.

The user (or chat thread) sees the resumed agent's reply through the existing message
broker. A monitor can trigger this reply before its subprocess exits. A report that
races with run release remains available for the session's resume handling.

**Flow-owned tasks are exempt from auto-resume.** A task spawned under a flow step's step-scoped context is marked `FlowOwned` at spawn time, and its completions never trigger `ResumeSession`.

What reacts instead is the step's own run. A flow step is one `agent.RunWith`
call, containing multiple model cycles. Reports are consumed between tool batches;
an explicit `yield`, or a normal terminal turn with pending work, waits for the first
report or queued user input and re-enters the model loop under the step's own agent, tools and output
schema. A streaming event wakes it without terminating the monitor. `ResumeSession`
cannot substitute for this: it uses the workspace's active agent, which may differ
from the step's agent (GENAI-239). The process-global run ledger prevents parallel
runs on the session.
This also applies to interactive flow steps: accepting reviewer input does not change
their `NonInteractive: true` runtime mode.
The one case with no reader is a flow-owned task that **outlives** its step. Background `bash` and `monitor` deliberately detach their subprocess from the turn context, so if the step's run exits first (step timeout, turn-budget exhaustion, shutdown) the subprocess keeps running and its completion pair lands in a session nothing will generate on again. The abandoned tasks are named in the synthetic wait-timeout note the runtime injects on that path, and the pair itself stays in the message log for the transcript, `tasklist`, and any later iteration of the step — but no model turn consumes it. This is deliberate: the alternative is the zombie turn above. Async `task` subagents are not exposed to it — their context derives from the step-scoped one, so the step's completion cancels them and their terminal pair is written before the step unwinds.

See `openspec/specs/session-run-exclusivity` and the `task-notifications` spec.

### Non-interactive mode (flow steps, headless CLI, ACP one-shot)

The caller invokes `agent.RunWith(ctx, sessionID, content, maxTurns, RunOptions{NonInteractive: true})`. The runtime then:

1. Runs the inner agentic loop, delivering background acknowledgments immediately.
2. At tool boundaries, appends newly persisted synthetic reports to the next model
   request, keeping every tool-call/result pair intact.
3. When the model calls `yield`, or ends its turn while background work remains,
   waits for the first report or queued user input. It subscribes before checking
   durable history and pending tasks, avoiding missed wakeups.
4. On an event, resumes the model loop with that report. A monitor may remain active;
   the model can react, do more work, stop an obsolete watch, or yield again.
5. Keeps the session's run slot throughout waiting, suppressing separate auto-resumes.
6. Returns only when the model finishes with no pending work, or the run is bounded
   by cancellation/turn limits. A final `struct_output` cannot bypass pending work.

The wait is naturally bounded by the surrounding `ctx`. No internal timeout knob.

### Anti-spin: foreground `sleep` is redirected to the wait

The normal end-of-turn wait is reached when the model emits a terminal turn;
`yield` provides an explicit wait earlier. A model that instead issues foreground
`bash sleep` calls between `tasklist`/`read` probes can consume its step budget
without reaching either wait.

The runtime therefore intercepts the poll primitive itself. In a non-interactive run, when the session **or a subagent it spawned** has pending non-monitor background tasks and the model issues a foreground `bash` command that *begins* with a wall-clock wait (`sleep <n>`, alone or followed by `;`/`&&` and anything else), the tool does NOT execute the sleep. It blocks on `WaitForActiveTasks(ctx, sessionID, WaitOptions{IncludeMonitor: false, Scope: ScopeSessionAndChildren})` — bounded by the step deadline — then runs the **trailer** (the part after the separator) verbatim through the ordinary synchronous path, and returns a synthetic result: the interception note, the tasks that reached terminal state during the wait, and the trailer's output with its real exit code. The traced shape `sleep 120; grep -nE 'BUILD SUCCESSFUL' /tmp/log | head -20` therefore waits for the build and then runs the probe, instead of burning 120 s. The requested sleep duration is irrelevant.

Scope guards:

- Interactive runs (`NonInteractive: false`) never intercept — a TUI `sleep 5` runs verbatim.
- No pending non-monitor tasks in the session or its direct children → the command runs verbatim (an idle sleep is pointless but harmless).
- Only-monitors-pending → the command runs verbatim; a stray sleep must not become a block on a monitor's whole lifetime. Call `yield` alone to wait for its next event.
- The scope is the calling session plus sessions whose parent is the caller — one level of descent, never the flow `root_session_id`, which every parallel step of a flow shares. `tasklist` and `taskstop` use the same scope (child-owned rows carry `owner=<session>`), so the model can observe and cancel whatever it is blocked on. The end-of-turn drain stays exact-session: a parent never blocks at end of turn on a child's tasks.
- `sleep 5 & echo bg` runs verbatim: `&` is not a separator, that command backgrounds the sleep.
- A trailer that is itself a sleep is dropped (`sleep 0; sleep 300` cannot bypass the guard); when the wait ends on a cancelled context the trailer is not run at all; the trailer is never re-checked for permission (the gate already evaluated the whole command string).

### Why `nohup … &` breaks `run_in_background`

`run_in_background` tracks the wrapper shell it spawns. A command that detaches its own work — `nohup ./gradlew build > /tmp/log 2>&1 &`, a trailing `&`, `setsid`, `disown` — makes that shell exit immediately while the real work runs on untracked: the task is "completed" in milliseconds with the wrong output, the completion notification fires early, the drain declines to hold, and a later `sleep` finds nothing pending (Langfuse trace `81aca169553f1c1d5f983ac6a665b742`: gradle ran for 3 m 11 s after its task reported `exit=0`). The bash tool therefore **refuses** such a command when `run_in_background: true` is set, before anything is spawned, and tells the model to drop the detachment and keep the flag. The rejection also forecloses the foreground fallback, because that route is worse: `nohup` used to be on the permission-exempt safe list, and the exemption was a prefix match on the whole string, so `nohup … &` — or `echo go; ./gradlew build &` — ran with no prompt and no task record. `nohup` is no longer exempt, and the exemption now covers only *simple* commands: anything with a top-level `;`, `&`, `&&`, `||`, `|`, newline, redirect, `$(…)`, backtick or subshell goes through the ordinary permission evaluation. Synchronous calls are not gated — backgrounding inside a foreground command is the caller's own business.

### Subagents drain too

Subagents launched by the `task` tool — synchronous or `async` — inherit the caller's `NonInteractive` marker. Under a flow step a subagent therefore holds its own turn open until its background tasks finish and has its foreground sleeps redirected, exactly like the step's primary agent; in the TUI nothing changes. Before this, every subagent was launched through the zero-value `Run` shim, which switched both halves of the no-poll contract off: a `piano-coder` subagent would start a 3-minute build and answer 3 seconds later, and a subagent with nothing but `tasklist` and `sleep 1` to wait with sat spinning for 18 minutes — a pattern stall detection cannot see, since polling emits a steady stream of progress signals (GENAI-140).

### Detached subagents are bounded by the step

`task async` subagents run detached from the parent's per-turn context so a turn ending never kills them. Under a flow step they now derive from a **step-scoped context** the flow runner installs (`tools.StepScopedContextKey`): the subagent survives any number of parent turns, but the step's deadline elapsing — or the step completing — cancels every subagent spawned during the step (each gets its `StatusFailed` completion via the normal notification path; `taskstop` still yields `StatusKilled`). Interactive callers don't install a step scope, so their async subagents keep the original unbounded `context.Background()` lifetime.

## Bounding monitor lifetime

`monitor` is the one tool with indefinite lifetime by design. In non-interactive contexts, the agent must bound it explicitly so the step doesn't block forever:

1. **`max_events: N`** — terminates the monitor after N coalesce windows containing matching events. The canonical "wait for ONE marker line then exit" pattern is `max_events: 1` with a specific pattern (`BUILD_PASSED|BUILD_FAILED`, `READY`, etc.).
2. **Finite-running `command` or `cmd`** — `kubectl logs <pod>` without `-f`, `tail -n 200 ...`, etc. The monitor exits when the subprocess exits.
3. **Explicit `taskstop`** after processing the events you need. A yielded monitor can wake the model before its subprocess ends.
4. **The step's `timeout`** (see [Timeouts](#timeouts)) is the safety net — if none of the above applies AND no upstream event ever fires, the step's deadline cancels the wait and a synthetic timeout note is injected.

The runtime does NOT auto-kill monitors at turn end. Auto-killing would defeat monitor's primary use case (wait for an external pipeline / log event), forcing agents back to `bash sleep` polling loops.

## Timeouts

Three sources, in precedence order:

| Source | Where | Notes |
|---|---|---|
| `Step.Timeout` | Flow YAML `timeout: 15m` on the step. | Highest priority. Cascades into `agent.RunWith`'s ctx via `context.WithTimeout`. Parsed via Go's `time.ParseDuration` — any valid duration string works. |
| `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` env var | Deploy environment. | Fallback default when the step has no explicit `timeout`. Parsed once at process start; SIGHUP-style reloads require a process restart. Malformed / non-positive values are logged and ignored. |
| Unbounded | When neither of the above is set. | The wait is bounded only by the surrounding ctx. Without a deadline it waits for a report or inbox input; a successful final return still requires no pending work. |

Deploying with neither source set is the configuration to avoid: a task that can never
finish then holds its step until the process exits, and a step whose `struct_output` was
already accepted still never routes. Prefer setting
`OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` deployment-wide even when individual steps
carry no `timeout`.

While the drain waits it logs the tasks it is still waiting on — id, kind and age —
every 60 seconds (`Non-interactive drain: still waiting on background tasks`). This is
observability only and never shortens the wait; it exists so a step legitimately held
open by a long task is distinguishable in the log from a hung or dead process.

## Stall detection

A task that can never reach a terminal state would otherwise hold its step until the
flow deadline, discarding an already-accepted `struct_output`. So on each drain progress
tick the runtime also checks whether any pending **subagent** task has stopped making
progress, and kills the ones that have through the same path `taskstop` uses.
Its completion report wakes the parent, which can react while other tasks remain
active. Successful final return still requires no pending work.

Scope follows the task's lifecycle handle, and the exemptions are deliberate:

| Kind | Handle | Liveness | Stall-checked |
|---|---|---|---|
| `bash` | `*os.Process` | definitional — the process runs or exits | no |
| `monitor` | `*os.Process` | definitional | no |
| `task` | `context.CancelFunc` | inferred — nothing observes an LLM loop | **yes** |
| `cron` | synthetic completion | not a long-running wait | no |

**A `monitor` is supposed to sit silent.** It spawns a script that polls an external
source without LLM calls — that is the cheap way to wait — and emits nothing at all
until its pattern matches. Silence is its healthy state, so inferring death from it
would break the tool. Monitors are bounded by `max_events` (which the model chooses, so
a step meaning "wait for one deploy-complete line" says `max_events: 1`), `taskstop`,
natural subprocess exit, and the flow deadline. Note that `max_events` is an
event-**count** threshold, not a timeout: monitors are deliberately not time-bounded,
and stall detection must not become a backdoor time bound on them.

For a subagent task the progress signal is the most recent message persisted on the
subagent's own session — any generation, tool call or tool result counts. A task with no
recorded subagent session is never considered stalled, so a missing signal cannot
manufacture a kill.

`backgroundTasks.stallThreshold` (default `30m`) sets the budget; `0` or any negative
value disables detection entirely. **It must exceed the largest single tool-call budget
reachable in the deployment**, because a subagent is legitimately silent for as long as
its longest blocking call:

| Blocking call | Ceiling |
|---|---|
| `bash` foreground | 10m hard cap |
| MCP tool call | 5m default, raisable without limit via `callToolTimeoutSeconds` |
| MCP transport start + handshake + close | 20s + 30s + 30s |
| Provider retry backoff | ~10m over `maxRetries` (8) of exponential backoff, and unbounded when the provider sends a `Retry-After` that is honoured |

The default clears the shipped ceilings with room to spare. If you raise an MCP server's
`callToolTimeoutSeconds` at or above the stall threshold, raise the threshold too or
healthy subagents will be killed mid-call — the runtime logs a warning naming any server
in that state, so check the log after changing either value.

Provider retry backoff is worth noting because it is completely silent: a subagent being
rate-limited persists no message while it backs off. The default threshold clears the
default backoff, but a proxy that returns a long `Retry-After` can exceed it.

Stall detection runs alongside explicit non-interactive yield and end-of-turn waits. It does **not** cover the anti-spin
redirect (`internal/llm/tools/bash_wait.go`), which waits on the registry directly: a
wedged subagent reached by a model self-poll is still bounded only by the surrounding
ctx.

A progress answer that cannot be obtained — a failed read of the subagent's session — is
treated as unknown, never as a stale timestamp. Substituting one would read as a stall
for any task already older than the threshold and kill healthy work on a single
transient read failure.

This is not a timeout on the wait. No task that is making progress is ever terminated,
however long it has been running in total — see the note above that the surrounding
`ctx` remains the only deadline on the wait itself.

When the wait returns `ctx.Err()`, the runtime writes a synthetic Assistant text message into the session log enumerating the still-pending task IDs, kinds, `started_at` timestamps, `output_file` paths, and any descriptions. The message has `Synthetic: true` so the chat bridge skips it for outbound indicators; non-bridge consumers (transcript export, SSE replay, the model on any subsequent `agent.Run` on this session) observe it as ambient context.

This means the model can react to a previous step's timeout when a flow is re-triggered. Without this, a re-run on the same session would replay the same dead-end work without knowing why the previous attempt stopped.

## Output files

Every background task writes its full output to `<config.Data.Directory>/tasks/<task_id>.out`. The path is included in the ack response and in the `tasklist` output. For `bash`/`monitor` tasks the subprocess streams into the file as it runs; for `task async` the subagent's final response is written at completion. The file is the canonical post-completion record — the acks deliberately do not frame it as a progress-polling target (reading it in a sleep loop is the exact anti-pattern the anti-spin interception neutralizes). Files are swept at opencode boot — there is no per-task cleanup on shutdown because the process owns the data directory.

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
        can continue independent work. When idle, call yield alone with {}.
        After its completion report, produce a struct_output with status=passed|failed and the
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
3. Coder continues any independent work, then calls `yield` alone with `{}`.
4. The runtime waits for the first background report or queued user message while retaining the flow step's run slot. A normal `end_turn` with pending work uses the same wait.
5. The bash subprocess runs for ~10 minutes. When it exits, `bashWaitAndNotify` writes the synthetic pair (`Assistant(ToolCall bash) + Tool(ToolResult: "<output>\nExit code 0")`).
6. The completion wakes the run. Coder sees the synthetic tool result and emits `struct_output { status: passed, tail: "<last 50 lines>" }`. An earlier report or reviewer input could have resumed it while the suite was still running.
7. With no pending tasks, `RunWith` returns the post-completion `AgentEvent`. The flow runner advances to the next step (if any) with the real result.

If the 20m timeout trips before the bash finishes:

1. Wait returns `ctx.Err()`.
2. Synthetic Assistant text message lands: `[wait-timeout] 1 background task(s) did not complete before the non-interactive wait ended (context deadline exceeded). - task_id=shell_5KFKDU… kind=bash started=… output_file=… desc="..."  ...`.
3. An explicit yield returns the wait error. A normal end-of-turn wait breaks the outer loop with its latest `AgentEvent`. Neither path establishes that the pending work succeeded.
4. A re-triggered flow on the same session reads the timeout note in the message history and can decide to wait longer, taskstop the orphan, or abort with a recorded reason.
