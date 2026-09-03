# Design: background-task-stall-detection

## Why per-kind scope dissolves the hard problem

The obvious worry is telling a wedged task apart from a slow one — and for a `monitor`
that looks genuinely hard: it spawns a script that polls an external source, makes no
LLM calls (that is the point — it is the cheap way to wait), and by design emits
nothing at all until its pattern matches. Any output- or event-based liveness signal
would flag a perfectly healthy monitor as dead.

That problem disappears once scope follows the task's lifecycle handle:

| Kind | Handle | Liveness | In scope |
|---|---|---|---|
| `bash` | `Proc: *os.Process` (`bash_background.go`) | definitional — the process runs or exits | no |
| `monitor` | `Proc: *os.Process` (`monitor.go`) | definitional | no |
| `task` | `Cancel: context.CancelFunc` (`agent-tool-async.go`) | **inferred** — nothing observes an LLM loop | **yes** |
| `cron` | synthetic completions | not a long-running wait | no |

For process-backed tasks there is nothing to infer. The OS already answers the
question, `MarkFinished` already fires on exit, and a silent poller is doing its job.
Only the subagent kind has no authority to ask — it is a goroutine running an LLM loop,
and "wedged" and "thinking" look identical from outside. That is also exactly the kind
that wedged in production.

So monitors are not distinguished from stalled tasks; they are never candidates.

## What already bounds a monitor

Verified against the code and spec, because the exemption rests on it:

- `max_events` — default 200, range [1, 10000] (`monitor.go`, `monitor-tool` spec).
  On reaching it the subprocess is SIGTERMed and a terminal notification fires. The
  **model sets this**, so a step that means "wait for exactly one deploy-complete line"
  says `max_events: 1` and gets a bounded wait it chose.
- `taskstop` — the model can end it explicitly.
- Natural subprocess exit.
- The flow deadline, for the never-matching case.

One refinement worth stating plainly: `max_events` is an **event-count** threshold, not
a timeout. There is no time bound on a monitor anywhere — `min_interval_ms` is only the
coalesce window. A monitor whose pattern never matches runs until the subprocess exits,
`taskstop`, or the flow deadline, and the `monitor-tool` spec already says so
explicitly ("Monitor without bound + no step timeout blocks until orchestrator
cancels"). So the model can express "wait for N events" but not "wait at most T
minutes". Adding a time bound to monitors would be a separate, deliberate decision —
this change does not make it, and stall detection must not become one by accident.

## The progress signal for subagent tasks

The subagent's own session message log. Every generation, tool call and tool result is
persisted there, so "the most recent message on the subagent's session" is the cheapest
faithful proxy for "the loop is turning".

This needs one plumbing change: `Task.SessionID` is the **parent** session — it is what
`PendingForSession` keys on — and the subagent's session id (`taskSession.ID`) is
currently passed only to `waitAsyncAndNotify`, never recorded. Hence
`Task.AgentSessionID`.

Rejected alternatives:

- **Output-file mtime.** For `task` kind the output file is written once, at completion
  (`docs/background-tasks.md`), so it carries no progress information at all.
- **A cost/generation counter.** Would work, but the message log is already persisted,
  already indexed by session, and survives the process; a counter would be new state to
  keep correct.

## Choosing the threshold

A subagent is legitimately silent for as long as its longest single tool call. Measured
ceilings in the current tree:

| Blocking call | Ceiling |
|---|---|
| `bash` foreground | 10 min hard cap (`MaxTimeout`, clamped in `bash.go`) |
| MCP tool call | 5 min default (`mcpCallToolTimeout`), raisable per server via `callToolTimeoutSeconds` |
| MCP transport start + handshake | 20s + 30s (the latter added by `obukhovaa/opencode#45`) |

So the threshold must exceed the largest per-call budget in the deployment. Default
**30 min** gives 3x headroom over the `bash` hard cap and 6x over the default MCP
budget, while still turning a multi-hour park into a bounded one. In the production
incident the subagent's last message was at 06:07:14; a 30-minute threshold fires at
~06:37 instead of the flow deadline at ~08:06, recovering ~1h29m *and* letting the step
route with the `struct_output` it had already produced.

The knob is configurable because `callToolTimeoutSeconds` has no upper bound: a
deployment that sets a 45-minute MCP budget must raise the stall threshold above it or
it will kill healthy work. This relationship is the one thing an operator has to
understand, so it goes in the docs and in the config field description.

Setting the threshold to zero or a negative value disables detection, restoring exactly
today's behaviour. That is the escape hatch if the heuristic misfires in a deployment we
did not anticipate.

## Why kill rather than report

`Registry.Kill` already does precisely the right four things: marks the state terminal
up front, stamps `finishedAtNanos`, closes the `done` channel, and invokes `t.Cancel`.
So a stalled task killed through it is indistinguishable, downstream, from one killed by
`taskstop`:

1. The drain's `WaitForActiveTasks` observes a terminal task and returns for the
   ordinary reason.
2. The existing `EnqueueTaskCompletion` path writes the synthetic completion pair into
   the parent session, so the model learns the task died and why.
3. The parent re-cycles, and `captureStructOutput`'s existing contract applies — a
   re-emitted `struct_output` overrides the earlier one, and if the model does not
   re-emit, the earlier capture survives.

The step therefore completes and routes. Merely *logging* a suspected stall would leave
the park in place, which is the actual defect.

## Relationship to the drain's no-own-timeout requirement

Worth being explicit, because it is easy to misread this change as violating it. The
`background-tasks` requirement says:

> The wait MUST NOT impose its own timeout — the surrounding `ctx` is the sole deadline
> source.

That governs the **wait**. Stall detection governs **task lifecycle**. After this change
the wait still blocks until every task is terminal, with no deadline of its own; a
stalled task just becomes terminal sooner, by the same mechanism `taskstop` already
uses. No legitimately-running task is capped, which is what the requirement protects.

So no amendment to that requirement is needed — a correction to an earlier reading of
mine, which assumed it would be.

## Non-goals

- **Busy-looping subagents.** The other known pathology — a subagent burning turns on
  `sleep`/`wait`/`true` plus `tasklist` polls — *does* write messages, so stall
  detection will not see it and should not try to. That is what the anti-spin redirect
  in `bash-background-mode` addresses.
- **Time-bounding monitors.** See above; a deliberate separate decision.
- **Replacing the flow deadline.** It stays the outer net. This makes the common case
  recover gracefully long before it.
