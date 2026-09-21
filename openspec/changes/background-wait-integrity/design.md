# Design

## Evidence: trace 81aca169553f1c1d5f983ac6a665b742

Session `1789550842-developer-review-apply-changes-resolve-team`, step span 379.1s.
The session column is which agent's registry the task landed in — it is the crux.

| Time (UTC) | Session | Event |
|---|---|---|
| 09:30:04 | parent → `piano-coder` #1 | subagent span opens (29.7s) |
| 09:30:31.366 | coder #1 | `bash run_in_background:true` → `nohup … ./gradlew … &` → `shell_43PXMJMQ…` |
| 09:30:31 | coder #1 | **task already terminal**: `state=completed finished=09:30:31Z exit=0` |
| 09:30:34 | — | coder #1 returns after 3s. gradle still running. |
| 09:30:42 | parent → `piano-coder` #2 | subagent span opens (58.6s) |
| 09:31:05 | coder #2 | `tasklist{state:all}` → the completed shell task (poll #1) |
| 09:31:34.154 | coder #2 | `monitor` #1 `tail -F /tmp/tmp.cMcghk` → `monitor_GUU6VTD4…` |
| 09:31:36.908 | coder #2 | `tasklist{state:running}` → only the monitor (poll #2) |
| 09:31:41 | — | coder #2 returns; `monitor_GUU6VTD4…` never stopped |
| 09:31:50.568 | **parent** | `monitor` #2 on the same file → `monitor_5PHPNRPT…` |
| 09:31:56.732 | **parent** | `bash sleep 120; grep …` → **lat 120.1s, ran verbatim** |
| 09:34:00 | parent | `taskstop monitor_5PHPNRPT…` only |

`BUILD SUCCESSFUL in 3m 11s`: the real work outlived its "completed" task by ~3 minutes.

## Why each gate failed, and the fix

### 1. Detachment (`bash_background.go`)

`cmd.Wait()` tracks the wrapper shell. `nohup … &` forks and the shell exits, so the task
is terminal in milliseconds. Every guarantee downstream reads that terminal state as "work
done": the completion notification fires early with wrong content, the drain declines to
hold, and the pending lookup returns empty.

Fix is a pre-spawn input gate, not a runtime heuristic — a command that detaches its own
work is unanswerable at the process level, and rewriting it silently would be worse than
refusing it. Detection is deliberately conservative: a top-level trailing `&` (not `&&`,
not `&` inside quotes or a subshell) or a `nohup`/`setsid`/`disown` word at command
position. Anything ambiguous is allowed through, because a false rejection blocks
legitimate work while a false accept only reproduces today's behavior.

**The gate must close the foreground route in the same breath.** `nohup` is in
`safeReadOnlyCommands` (`bash.go:62-64`) and `IsSafeReadOnlyCommand` is a prefix match
(`tools.go:301-311`), so foreground `nohup ./gradlew … &` short-circuits the entire
permission block at `bash.go:167,173`. A model told only "drop the `&`" may instead drop
`run_in_background`, trading a tracked-but-lying task for an untracked, unprompted orphan.
Removing `nohup` from that list is part of this change, not a follow-up.

Ordering note: the `if params.RunInBackground` branch sits at `bash.go:200`, *after* the
permission block. The detach check must be hoisted to ~`:166`.

### 2. Scope: caller session + direct children — NOT the flow root

`PendingForSession` is exact-match (`registry.go:113`). The naive fix is `root_session_id`,
and it is wrong. A flow assigns **one** root to every step:

```go
rootSessionID := fmt.Sprintf("%s-%s-%s", sessionPrefix, sessionFlowID, f.Spec.Steps[0].ID)  // flow/service.go:256
```

and steps run concurrently (`service.go:493`). Root scope would therefore make a `sleep` in
parallel branch A block on branch B's unrelated background work. The trace's own session
name matches that root pattern exactly, so this is not hypothetical.

Scope is instead **the calling session plus its direct children** — one level of descent.
That covers the parent→subagent case that produced the incident without reaching sideways
into sibling branches. Deeper nesting degrades to today's behavior, which is acceptable:
the hole it leaves is strictly smaller than the one being closed.

Two implementation consequences the first draft missed:

- **The registry cannot resolve the caller's identity on its own.** Carrying a parent id on
  `task.Task` lets the registry *bucket* tasks, but `PendingForSessionTree(callerID)` still
  needs to know who the caller's children are. `internal/llm/tools` does not import
  `internal/session` (`bashTool` holds only `{permissions, registry}`, `bash.go:48-51`), so
  the seam is a `RootSessionIDContextKey` set in `agent.go` beside `SessionIDContextKey`
  (`:847`), where the session row is already loaded. Without it the lookup silently
  degrades to exact scope and the whole fix becomes a no-op that no registry-level test
  would catch.
- **The wait must be widened too, not just the pre-check.** `WaitForActiveTasks` takes a
  bare `sessionID` and re-snapshots exact scope internally (`registry.go:138` → `:113`).
  Widening only the pre-check at `bash_wait.go:80` opens the gate and then returns in
  microseconds having waited for nothing — and, combined with fix 4, runs the trailer
  against pre-completion state. Slow-and-correct becomes fast-and-wrong. Scope therefore
  becomes a field on `WaitOptions`, honored at both sites.

**The management surface moves with it.** `tasklist` (`tasklist.go:75`) and `taskstop`
(`taskstop.go:80-81`, *"Task %s does not belong to this session"*) are exact-scoped. Left
alone, the redirect would block the parent on a `task_id` it can neither list nor kill,
with `killStalled` not applying (`KindTask`-only, `agent.go:1425-1432`) and `stepCtx`
frequently carrying no deadline at all (`flow/service.go:2602-2615`). Both tools move to
the same scope in this change.

One honest caveat: the synthetic completion is enqueued on the **task's owning session**
(`bash_background.go:137`). For a child-owned task the parent will not receive it, so the
redirect's note must point at `output_file` rather than promise a completion in the
conversation.

### 3. Regex narrowness (`bash_wait.go:21`)

`pureWaitRe` was fitted to the CD-4761 samples. The observed shape is the more natural one
— sleep, then probe. Widening the matcher to enumerate safe trailers is a losing game.

Instead, split: match a leading `^sleep <n>[smhd]?` **anchored to a following `;` or `&&`**
and treat the remainder as a trailer. The anchor matters: `sleep 5 & echo bg` must keep
failing, since `&` is neither separator (`bash_wait_test.go:42`).

Three guards the first draft lacked:

- **Self-bypass.** If the trailer is itself a wait (`sleep 0; sleep 300`), executing it
  verbatim sleeps 300s under a result that reads *"Do NOT sleep or poll"*. The trailer is
  re-checked and a leading wait in it is dropped.
- **Cancelled ctx.** When the wait ends on `ctx.Err()`, the trailer is NOT executed —
  `sh.Exec` on a dead ctx yields "Command was aborted before completion" appended under a
  deadline note, which explains nothing.
- **Exit code.** `bash_wait.go:129-133` sets no `ExitCode`/`TempFilePath`, so a failing
  trailer would report success. The trailer's result must carry real metadata.

Execution reuses the synchronous path, but there is no helper to call: `bash.go:220-261`
(truncation via `persistAndTruncate`, stderr composition, exit code, temp-file metadata,
the empty-output case) is inline in `Run`. It must be extracted. `interceptForegroundWait`
also needs `workdir` and `timeout` threaded into its signature — today it takes only
`(ctx, command, sessionID)`.

**Permission is already covered and this is load-bearing:** interception sits at
`bash.go:207`, *after* the permission block at `:173-199`, which evaluated the full
`params.Command` string — trailer included. The trailer needs no second check, and
re-checking it would double-prompt. This is the opposite of the ordering the detach gate
needs, so both are stated explicitly in the spec.

### 4. Monitor legibility (`monitor.go:239`, `tasklist.go`)

The monitor ack says "Do NOT poll" but never states the yield contract that
`bash_background.go:79` spells out. A model told only what not to do, with no feedback and
no events arriving, has one primitive left. That is the observed sequence: second monitor,
two `tasklist` polls, then sleep.

With `UntilFirstEvent` cut, this is now the *entire* answer for the monitors-only case, so
it carries more weight than it did in the first draft. The ack must say plainly that
ending the turn without a tool call is how you wait.

The scanned-line counter cannot live in `monitorState`: that struct is unexported, owned by
its three goroutines, and reachable from nothing else (`monitor.go:262-277`), while
`tasklist` reads only `task.Task` via `reg.ListBySession` (`tasklist.go:75`). It must be an
exported atomic on `task.Task` with a `ScannedLines()` reader, bumped from `scanLoop`.

## Risks

- **Rejecting `&` breaks an existing working flow.** A repo-wide sweep found nothing pairing
  `nohup`/`setsid`/`disown`/trailing-`&` with `run_in_background: true`. Detection is
  conservative and the message names the fix.
- **One level of descent is arbitrary.** It is. It is chosen because it closes the observed
  hole at a cost that is auditable, where root scope is not.
- **Trailer reordering.** Plain `&&` short-circuit is preserved. The shape that genuinely
  inverts is the watchdog idiom `sleep 300 && kill $(cat /tmp/pid)`, which now fires after
  the task it was meant to bound. Rare, but the blanket "strictly safer" claim in the first
  draft was wrong and is withdrawn.
- **Leaked monitors persist.** Registry entries are never deleted and monitors use
  `exec.Command`, not `CommandContext` (`monitor.go:185`), so a leaked `tail -F` stays
  `StateRunning` for the process lifetime. Under any widened scope that is a growing
  hazard; it is bounded here only because monitors stay excluded from the redirect.
