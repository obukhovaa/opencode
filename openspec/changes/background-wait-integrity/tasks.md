## 1. Reject self-detaching background commands

- [ ] 1.1 Add `internal/llm/tools/bash_detach.go` with `detectSelfDetach(cmd string) (construct string, ok bool)`.
  No reusable tokenizer exists (`shell/interactive.go:55-61` is a first-token check by design;
  `flow/predicate.go:202` is a different grammar), so this is ~100 LOC of quote/subshell-aware
  scanning: single quotes, double quotes, `$(…)`, backticks, `(…)`. Flag only a top-level
  trailing `&` (never `&&`) or top-level `nohup`/`setsid`/`disown` at command position.
- [ ] 1.2 Call it from `bashTool.Run` on the background path. **The `if params.RunInBackground`
  branch is at `bash.go:200`, after the permission block — hoist the check to ~`bash.go:166`.**
  Return `NewTextErrorResponse` naming the construct.
- [ ] 1.3 Error text MUST foreclose the foreground fallback, not just name the fix — otherwise
  the model drops `run_in_background` instead and lands on a permission-exempt path.
- [ ] 1.4 **Remove `nohup` from `safeReadOnlyCommands` (`bash.go:62-64`).** `IsSafeReadOnlyCommand`
  is a prefix match (`tools.go:301-311`), so foreground `nohup … &` currently skips the whole
  permission gate. This is the trace's exact command; it belongs in this change.
- [ ] 1.5 Table-driven `bash_detach_test.go`: positives (`nohup x &`, `x &`, `x & echo $!`,
  `setsid x`, `disown`), negatives (`a && b`, `echo 'a & b'`, `x 2>&1`, `x &>log`, `&` inside
  a subshell), plus the synchronous-call passthrough.
- [ ] 1.6 Extend `bash_background_test.go`: a rejected command registers no task, creates no
  output file, and returns an error ToolResult rather than an ack.

## 2. Session-and-children scoping

- [ ] 2.1 Add `RootSessionIDContextKey` (caller session identity) in `internal/llm/tools`, and set
  it in `agent.go` beside `SessionIDContextKey` at ~`:847`, where the session row is already
  loaded. **Without this the registry cannot resolve who the caller's children are and the
  whole fix degrades to exact scope silently.**
- [ ] 2.2 Carry the owning session's parent on `task.Task` at registration. Sites:
  `bash_background.go:49`, `monitor.go:202`, `agent-tool-async.go:76`, and
  `cron/scheduler.go:528` (the fourth site, previously unlisted). Only the agent-tool site has
  the session row today; the others take it from the new ctx key.
- [ ] 2.3 Add `WaitScope` (`ScopeExactSession` zero value | `ScopeSessionAndChildren`) as a field
  on `task.WaitOptions`; add `PendingForSessionTree`. **Honor the scope in BOTH
  `PendingForSession*` and the internal re-snapshot at `registry.go:138` — widening only the
  pre-check at `bash_wait.go:80` returns in microseconds having waited for nothing.**
- [ ] 2.4 Point `interceptForegroundWait` at the widened lookup AND pass
  `Scope: ScopeSessionAndChildren` to `WaitForActiveTasks`. Leave `drainSessionTasks` /
  `waitForTasksWithProgress` (`agent.go:1334`) on the zero-value (exact) scope.
- [ ] 2.5 Move `tasklist` (`tasklist.go:75`) and `taskstop` (`taskstop.go:80-81`) to the same
  scope, marking child-owned rows. Without this the model is blocked on a task it can neither
  list nor kill.
- [ ] 2.6 Fix the interception note: a child-owned task's completion is enqueued on the child's
  session (`bash_background.go:137`), so the note must point at `output_file` instead of
  promising "synthetic completion results follow in the conversation" (`bash_wait.go:109`).
- [ ] 2.7 Tests: parent sees child task; parallel siblings excluded; childless session degrades
  to exact; drain does NOT pick up child tasks; **and a plumbing test that fails if the ctx key
  is missing** (a registry-only test would pass against a no-op).

## 3. Split `sleep N` from its trailer

- [ ] 3.1 Replace `pureWaitRe` with `leadingWaitRe`: `^sleep <n>[smhd]?` **anchored to a
  following `;` or `&&`**, or the bare form. Returns `(duration, trailer, ok)`.
  `sleep 5 & echo bg` must stay non-matching (`bash_wait_test.go:42`).
- [ ] 3.2 Thread `workdir` and `timeout` into `interceptForegroundWait` — its signature is
  `(ctx, command, sessionID)` today and it receives neither (`bash.go:152-165` computes both).
- [ ] 3.3 Extract `bash.go:220-261` (persistAndTruncate on stdout+stderr, stderr/interrupted/
  exit-code composition, `TempFilePath`, `BashResponseMetadata`, the empty-output case) into a
  shared helper. There is no reusable synchronous path to call today.
- [ ] 3.4 Guards: (a) drop a leading wait inside the trailer so `sleep 0; sleep 300` cannot
  bypass; (b) skip the trailer entirely when the wait returned `ctx.Err()`; (c) populate real
  `ExitCode`/`TempFilePath` — `bash_wait.go:129-133` sets neither, so a failing trailer would
  report success.
- [ ] 3.5 Do NOT re-gate the trailer on permissions: `bash.go:173-199` already evaluated the
  full command string. Add a comment pinning this, since it is the opposite of task 1.2's order.

## 4. Monitor ack and tasklist legibility

- [ ] 4.1 Extend the ack in `monitor.go:239` with the no-sleep + turn-held sentence mirroring
  `bash_background.go:79`. **With `UntilFirstEvent` cut this is the only mitigation for the
  monitors-only case, so it must say plainly that ending the turn is how you wait.**
- [ ] 4.2 Add an exported atomic scanned-line counter to `task.Task` with a `ScannedLines()`
  reader, bumped from `monitorState.scanLoop` (`monitor.go:285-294`). **Not on `monitorState`:
  it is unexported and `tasklist` reads only `task.Task` via `reg.ListBySession`.**
- [ ] 4.3 Surface it on `kind=monitor` rows in `tasklist`.
- [ ] 4.4 Tests: ack contains "do NOT sleep" and the turn-held clause; non-zero scanned count
  for a matched-nothing monitor; counter never triggers a kill, stall flag, or notification.

## 5. Invert the assertions this change contradicts

- [ ] 5.1 `bash_wait_test.go:253-255` asserts the trailing `echo` must NOT execute. It now does.
- [ ] 5.2 `bash_wait_test.go:159-168` `TestInterceptForegroundWait_PassthroughOnlyMonitors` —
  **keep as-is.** Monitors remain excluded now that `UntilFirstEvent` is cut.
- [ ] 5.3 `cmd/background-e2e/main.go:492` (`SleepInterceptNoEcho`) and its consumers at
  `scripts/test/background.sh:200,214`.
- [ ] 5.4 `TestIsPureWaitCommand` rows flipping `false`→`true`: `"sleep 5; ls"` (:37),
  `"sleep 5; echo a; echo b"` (:38), `"sleep 5; echo done > out.txt"` (:40),
  `"sleep 5; echo $(date)"` (:41).

## 6. Docs and verification

- [ ] 6.1 Rewrite the "Scope guards" list in `docs/background-tasks.md:63-67` (session+children,
  trailer splitting, monitors still excluded).
- [ ] 6.2 Add a "Why `nohup … &` breaks `run_in_background`" note with the trace as the worked
  example, and the `safeReadOnlyCommands` prefix-match hazard.
- [ ] 6.3 No `Config` surface changes — no `cmd/schema/main.go` / `opencode-schema.json`
  regeneration. State this explicitly in the PR.
- [ ] 6.4 Add a `scripts/test/` e2e for the cross-session case: background task on a child
  session + parent `sleep` → intercepted, no 120s wall clock.
- [ ] 6.5 `make test`.

## Not in this change

- `UntilFirstEvent` / waking a monitors-only wait on the next monitor event. Cut — see proposal.
- Making subagents drain (`agent-tool.go:191` → `a.Run` → `RunOptions{}` → `agent.go:1254`).
  This is the deeper root cause and changes turn semantics for every subagent in the product.
- Registry entry eviction. Entries are never deleted and monitors use `exec.Command` rather
  than `CommandContext` (`monitor.go:185`), so leaked monitors persist for the process
  lifetime. Bounded here only because monitors stay out of the redirect.
