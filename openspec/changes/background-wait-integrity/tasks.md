## 1. Reject self-detaching background commands

- [x] 1.1 `internal/llm/tools/bash_detach.go`: quote/subshell/backtick-aware `scanShellShape` (top-level operators and command words), `detectSelfDetach` (any top-level `&` control operator, or `nohup`/`setsid`/`disown` at command position; `&&` and the redirects `2>&1`/`&>` are not control operators; ambiguity → allowed through), `hasTopLevelCompound`, `selfDetachRejection`.
- [x] 1.2 `bashTool.Run` gates `run_in_background` BEFORE the permission block (`bash.go:173`) and returns an error ToolResult naming the construct.
- [x] 1.3 The rejection text forecloses the foreground fallback.
- [x] 1.4 `nohup` removed from `safeReadOnlyCommands`.
- [x] 1.4a `IsSafeReadOnlyCommand` returns false for any top-level control operator, redirect, command substitution or subshell (`hasTopLevelCompound`; ambiguity → not safe). `AllowParallelism` inherits the stricter answer.
- [x] 1.5 `bash_detach_test.go`: positives, negatives, ambiguity, the safe-list table, and a full-`Run` rejection test (no task, error result, synchronous passthrough).
- [x] 1.6 Covered by 1.5's full-Run test (`TestBashRun_RejectsSelfDetachingBackgroundCommand`).

## 2. Session-and-children scoping

- [x] 2.1 `tools.ParentSessionIDContextKey` + `ParentSessionIDFromContext`; `agent.RunWith` sets it from `session.ParentSessionID` beside `IsTaskAgentContextKey` (`agent.go:875`) — the parent, never `RootSessionID`.
- [x] 2.2 `task.Task.ParentSessionID` stamped at the three registration sites: `bash_background.go`, `monitor.go`, `agent-tool-async.go`. (The proposal's "fourth site" `cron/scheduler.go` registers no task — it only enqueues a completion — so there is nothing to stamp there.)
- [x] 2.3 `task.WaitScope` (`ScopeExactSession` zero value | `ScopeSessionAndChildren`) on `WaitOptions`; `inScope`; `PendingForSessionTree` / `ListBySessionTree`; `WaitForActiveTasks` snapshots with `opts.Scope`.
- [x] 2.4 `interceptForegroundWait` pre-checks with `PendingForSessionTree` AND waits with `Scope: ScopeSessionAndChildren`; `drainSessionTasks` keeps the exact scope.
- [x] 2.5 `tasklist` lists the tree with `owner=<session>` on child-owned rows; `taskstop` accepts a child-owned task and still refuses siblings.
- [x] 2.6 The note separates own completions ("results follow in the conversation") from child-owned ones ("read output_file; the result was delivered to that session").
- [x] 2.7 Tests: `TestScope_SessionAndChildrenIsOneLevelOfDescent`, `TestWaitForActiveTasks_ScopeIsHonoredByTheWaitItself`, `TestInterceptForegroundWait_ParentWaitsOnChildTask`, `…_SiblingsAndGrandchildrenExcluded`, `TestDrainSessionTasks_IgnoresChildTasks`, tasklist/taskstop scope tests; the e2e driver's child-scope section runs the full bash `Run` path.

## 3. Split `sleep N` from its trailer

- [x] 3.1 `leadingWaitRe` / `splitLeadingWait(cmd) (duration, trailer, ok)` anchored to `;` or `&&`; `sleep 5 & echo bg` stays non-matching.
- [x] 3.2 `interceptForegroundWait(ctx, params, workdir, sessionID)` receives the params (timeout) and the workdir.
- [x] 3.3 The synchronous path is extracted into `runForeground(ctx, command, workdir, timeoutMs) (output, exitCode, tempPath, err)`; `Run` and the trailer share it.
- [x] 3.4 Guards: `stripLeadingWaits` (self-bypass), no trailer on `ctx.Err()`, real `ExitCode` / `TempFilePath` on the result metadata.
- [x] 3.5 No second permission check; pinned in a comment on `interceptForegroundWait` and at the call site.

## 4. Monitor ack and tasklist legibility

- [x] 4.1 Monitor ack states the no-sleep / turn-held yield contract and points at `tasklist`'s scanned-line count.
- [x] 4.2 `task.Task.AddScannedLines` / `ScannedLines` (atomic), bumped from `monitorState.scanLoop` via `monitorState.tk`.
- [x] 4.3 `tasklist` prints `scanned_lines=N` on `kind=monitor` rows.
- [x] 4.4 `TestMonitor_AckYieldContractAndScannedLines`, `TestTask_ScannedLinesCounter`.

## 5. Invert the assertions this change contradicts

- [x] 5.1 `TestBashRun_InterceptsSleepEndToEnd` now asserts the trailing `echo` DID run (after the note, exit code 0 in metadata).
- [x] 5.2 `TestInterceptForegroundWait_PassthroughOnlyMonitors` kept as-is.
- [x] 5.3 `cmd/background-e2e` `SleepInterceptNoEcho` → `SleepInterceptTrailerRan`; `scripts/test/background.sh` updated.
- [x] 5.4 `TestIsPureWaitCommand` → `TestSplitLeadingWait` with trailer expectations; the rows that flipped are marked.

## 6. Subagents inherit the no-poll contract (GENAI-140)

- [x] 6.1 `agent-tool.go` and `agent-tool-async.go` launch through `a.RunWith(…, subagentRunOptions(callerCtx))`; the async path reads the marker from the caller ctx, not the detached run ctx.
- [x] 6.2 `TestSubagentRunOptions_InheritsNonInteractive`.
- [x] 6.3 Spec: `background-tasks` ADDED "Subagents inherit the caller's non-interactive marker"; `docs/background-tasks.md` "Subagents drain too".

## 7. Crash recovery of a killed step — opencode half of GENAI-352

- [x] 7.1 `flow.RunOptions{Fresh, RecoverRunning}` + `Service.RunWithOptions`; `Run` is the shim. The `hasRunning` replay path is skipped when `RecoverRunning` is set; `hasResumableWork` + `collectResumableSteps` then re-enter the running step with its persisted args.
- [x] 7.2 `POST /flow/run` body `recoverRunning`; `flowStartOptions.recoverRunning`; `flowRunner.run` takes `flow.RunOptions`.
- [x] 7.3 `TestRunRecoverRunningResumesRunningStep` (re-executed to a terminal state, nothing deleted, no new row).
- [x] 7.4 Spec deltas on `flow-runtime-resume` and `flow-api`; `docs/flows.md` crash-recovery paragraph.

## 8. Docs and verification

- [x] 8.1 `docs/background-tasks.md`: scope guards rewritten (children scope, trailer, monitors excluded), "Why `nohup … &` breaks `run_in_background`", "Subagents drain too".
- [x] 8.2 No `Config` surface change — no schema regeneration.
- [x] 8.3 `scripts/test/background.sh` + driver: child-scope intercept (fast, owner-marked), tasklist/taskstop scope, detach rejection, monitor ack — 33 checks.
- [x] 8.4 `make test`.

## 9. Downstream (c2-agent, GENAI-352's orchestrator half — its own change `killed-step-resume`)

- [ ] 9.1 Job `activeDeadlineSeconds` derived from `JOB_STARTUP_TIMEOUT + JOB_TIMEOUT` plus grace.
- [ ] 9.2 On a killed runner pod with a step in flight, one bounded continuation job with `recoverRunning: true` instead of a terminal failure; card wording.
- [ ] 9.3 Bump the opencode pin to the release carrying 7.x.

## Not in this change

- `UntilFirstEvent` / waking a monitors-only wait on the next monitor event. Cut — see proposal.
- Registry entry eviction. Entries are never deleted and monitors use `exec.Command` rather
  than `CommandContext`, so leaked monitors persist for the process lifetime. Bounded here
  because monitors stay out of the redirect and, with 6.x, a flow-step subagent's own drain
  now bounds the monitors it spawns.
