## Why

The no-poll contract (`background-tasks`, `bash-background-mode`) promises that a
non-interactive agent never has to `sleep`: background work is tracked, the turn is held
open until it finishes, and a foreground `sleep` is redirected to that wait. In Langfuse
trace `81aca169553f1c1d5f983ac6a665b742`
(`piano-developer/developer-review-apply-changes/apply-fixes`, v0.18.5) every one of those
guarantees failed silently and the agent burned 120s of wall clock on `sleep 120`.

Three defects compounded. Each is independently sufficient to defeat the redirect:

1. **A `run_in_background` task can complete without its work completing.** The agent sent
   `nohup … ./gradlew … & echo "PID=$!"` *with* `run_in_background: true`. The registry
   tracked the wrapper shell, which forked and exited immediately —
   `state=completed started=09:30:31Z finished=09:30:31Z exit=0` — while gradle ran on
   untracked for 3m11s. The ack still asserted *"The task is running … do NOT sleep"*,
   which was already false when it was printed, and the synthetic completion carried the
   wrapper's two echo lines instead of the build output.
2. **Task visibility is scoped to the exact session.** `PendingForSession` filters on
   `t.SessionID != sessionID` (`internal/task/registry.go:113`). The gradle task was
   registered by a `piano-coder` **subagent**; the `sleep 120` was issued by the
   `piano-developer` **parent**, which could not see its own child's work.
3. **The wait regex only matches `sleep N[; echo …]`.** The actual command —
   `sleep 120; grep -nE … | head -20; echo "---tail---"; tail -5 …` — fails `pureWaitRe`
   (`bash_wait.go:21`). Even with 1–2 fixed, this shape would still sleep.

Secondary, same trace: monitor silence is indistinguishable from monitor failure. The
agent started a second monitor on the same file 16s after the first, called `tasklist`
twice against the "do NOT poll" instruction, then slept.

## What Changes

- **Reject self-detaching background commands.** When `run_in_background: true`, a command
  whose effective top level ends in `&` or invokes `nohup`/`setsid`/`disown` is refused
  with guidance. The rejection text MUST also foreclose the foreground fallback, because
  that route is *worse*: `nohup` sits in `safeReadOnlyCommands` (`bash.go:64`) behind a
  prefix match, so foreground `nohup … &` runs with no permission prompt and no task record
  at all. This change removes `nohup` from that list as part of the same fix.
- **Scope the foreground-wait redirect to the caller's session and its direct children.**
  Deliberately *not* `root_session_id`: a flow assigns one root to every step
  (`internal/flow/service.go:256`) and steps run concurrently, so root scope would make a
  `sleep` in one parallel branch block on an unrelated branch's work. One level of
  descent covers the parent→subagent case in the trace without that blast radius.
- **Widen the wait itself, not just the pre-check.** `WaitForActiveTasks` re-snapshots
  scope internally (`registry.go:138`), so a widened pre-check over an unwidened wait would
  return in microseconds and report tasks it never waited for. Scope becomes a field on
  `WaitOptions`, applied at both sites.
- **Move `tasklist` and `taskstop` to the same scope.** Otherwise the redirect hands the
  model a `task_id` it can neither list nor kill (`tasklist.go:75`, `taskstop.go:80`),
  blocking it on work it cannot observe or escape.
- **Intercept `sleep N` with a trailer.** A leading `sleep <n>` followed by `;`/`&&` is
  split: the runtime performs the wait, then executes the remainder verbatim. Guarded
  against self-bypass (a trailer that is itself a wait) and skipped entirely when the wait
  ended on a cancelled ctx.
- **State the yield contract in the monitor ack** and report scanned lines in `tasklist`,
  so a healthy silent monitor is legible and the model knows that ending its turn — not
  sleeping — is how it waits.

## Cut from this proposal

An earlier draft added `WaitOptions{UntilFirstEvent: true}` so that a monitors-only pending
set would wait on the next monitor event instead of being skipped. **Cut: it does not
work.** `drainAndEmit` returns immediately on an empty buffer (`monitor.go:322`), so a
monitor whose pattern has not yet matched emits nothing indefinitely and `tail -F` never
exits — neither exit condition can fire, and the wait runs to the step deadline. That is
the step-length block the monitor exclusion exists to prevent, and it is exactly the
traced scenario (the monitor was silent because `BUILD SUCCESSFUL` had not printed).
Monitors therefore remain excluded from the redirect, and the ack/legibility work above is
the mitigation for the monitors-only case.

## Non-goals

- Changing the end-of-turn drain's scope or its `IncludeMonitor: true` semantics.
- Time-bounding monitors. `max_events` stays an event count, not a timeout.
- Interactive runs. Every behavior here remains gated on `IsNonInteractive(ctx)`.
- Reparenting orphaned processes. A rejected command is not silently rewritten.
- **Making subagents drain.** See the follow-up below — it is the larger root cause and
  deserves its own change.

## Follow-up: subagents never drain

Both reviews surfaced a defect one level below this change. `agent-tool.go:191` and
`agent-tool-async.go:68` launch subagents through `a.Run(...)`, the shim that passes
zero-value `RunOptions` — *"interactive mode, no end-of-turn wait"* per its own comment at
`agent.go:716`. `agent.go:1254` then breaks out of the outer loop when `!NonInteractive`,
and `agent.go:853` overwrites the inherited non-interactive marker with `false`.

So **no subagent has ever drained, and no subagent's `sleep` has ever been redirected.** In
the trace, coder #1 would have returned in 3 seconds even with the gradle task tracked
correctly, and the leaked `monitor_GUU6VTD4…` leaked because no drain ran — not because
`taskstop` was missed. Fixing that changes turn semantics for every subagent in the
product and must not ride along here.
