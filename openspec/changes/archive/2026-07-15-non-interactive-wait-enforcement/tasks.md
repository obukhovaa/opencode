## 1. Propagate non-interactive signal + sessionID onto the tool ctx

- [x] 1.1 Add a single unexported `nonInteractiveContextKey` in the package that owns `GetContextValues` (the shared tool-ctx helpers). SessionID/messageID ALREADY flow to tools via `GetContextValues` (`bash.go:255`) — do NOT add a new sessionID channel.
- [x] 1.2 In `processGeneration`, before dispatching tool calls, add `opts.NonInteractive` to the tool-execution ctx alongside the existing sessionID/messageID values. Ensure it is present on the ctx passed into every tool `Run`. Runtime-only signal, never persisted.
- [x] 1.3 Extend `GetContextValues` (or add a sibling `NonInteractiveFromContext(ctx) bool`) so `bash` can read the marker; the tool continues to read sessionID from the existing `GetContextValues`.
- [x] 1.4 Unit test: the ctx carries the non-interactive marker in non-interactive runs and not in interactive runs; sessionID continues to resolve as today.

## 2. Anti-spin: foreground `sleep` interception in bash tool

- [x] 2.1 In `internal/llm/tools/bash.go::Run` (foreground branch, before `sh.Exec`), read the non-interactive marker + sessionID from ctx.
- [x] 2.2 Add `isPureWaitCommand(cmd string) (bool, requestedSleep time.Duration)` — true iff the trimmed command is solely `sleep <n>` optionally followed by `;`/`&&` + a single `echo …`. Keep detection conservative and unit-tested against the CD-4761 forms (`sleep 300; echo done`, `sleep 120; echo waited`).
- [x] 2.3 When (non-interactive ∧ `isPureWaitCommand` ∧ `reg.PendingForSession(sessionID, nonMonitorFilter)` non-empty): capture the pre-wait pending snapshot, skip `sh.Exec`, call `reg.WaitForActiveTasks(ctx, sessionID, task.WaitOptions{IncludeMonitor: false})`, then diff against a post-wait snapshot to return a synthetic bash-style `ToolResponse` enumerating the tasks that reached terminal state (id, kind, output_file), plus a one-line note that the runtime waited instead of sleeping. On `ctx.Err()`, return a result noting the deadline elapsed and which tasks are still pending. Monitors are NOT counted and NOT waited on here (see `bash-background-mode`).
- [x] 2.4 All other cases (interactive, no pending non-monitor tasks, only-monitor pending, or non-pure-wait command) run the command unchanged — no behavior change.
- [x] 2.5 Reuse the existing `internal/task` import already present in `internal/llm/tools` (`bash_background.go`, `monitor.go`, `tasklist.go` all use `task.GlobalRegistry()`); `internal/task` deliberately does not import back (`internal/task/deps.go`). No new dependency edge is required.
- [x] 2.6 Unit tests: (a) interception fires for a pure-wait command with pending non-monitor tasks in non-interactive mode; (b) passes through when interactive; (c) passes through when no pending tasks; (d) passes through for a non-pure command; (e) passes through when only monitors are pending; (f) ctx-deadline path returns the still-pending note.

## 3. Drain-to-empty loop in the agent

- [x] 3.1 In `agent.go`, wrap the existing `WaitForActiveTasks` call (:927) so that after a `nil` return it re-reads `PendingForSession(sessionID, nil)` and, if non-empty, waits again — looping until pending is empty or the ctx is cancelled, bounded by the existing outer-cycle guard.
- [x] 3.2 Preserve the existing `injectWaitTimeoutNote` behavior on `ctx.Err()` (enumerate still-pending tasks, break outer loop).
- [x] 3.3 Do NOT change `registry.WaitForActiveTasks` snapshot-at-start semantics.
- [x] 3.4 Unit test: two spawn waves (second registered after the first wait begins) — the drain loop returns only when the session has zero pending tasks.
- [x] 3.5 Verify the drain does not spuriously consume `effectiveMaxTurns`: a bare `WaitForActiveTasks` re-wait (with no new model invocation) must NOT increment the turn counter; only cycles that re-invoke the model count. If a fan-out produces more model re-invocation cycles than `effectiveMaxTurns`, the loop MUST terminate via the existing max-turns / `injectWaitTimeoutNote` path rather than returning silently with tasks still pending.

## 4. Bound detached subagents to a step-scoped context

- [x] 4.1 In `internal/flow/service.go`, derive a step-scoped ctx that lives for the whole step (bounded by the existing `stepCtx` timeout / env default) and is independent of any single turn's ctx. Make it reachable by the agent for the async spawn path (e.g. via a ctx value or an agent field set per step).
- [x] 4.2 In `internal/llm/agent/agent-tool-async.go:46`, derive `runCtx` from the step-scoped ctx (still wrapped with `context.WithCancel` for `taskstop`) instead of `context.Background()`.
- [x] 4.3 Verify the subagent is NOT cancelled when the parent's per-turn ctx ends (the original reason `Background()` was chosen must still hold).
- [x] 4.4 Verify a step-deadline / step-completion cancels the subagent's `runCtx`; confirm cost rollup + a `StatusFailed` completion still fire on cancel via the existing `waitAsyncAndNotify` path (`StatusKilled` remains reserved for `taskstop`).
- [x] 4.5 Unit test: subagent survives a simulated turn-end but is cancelled when the step-scoped ctx is cancelled/deadlined.

## 5. No-poll guidance reaches every agent + ack hygiene

- [x] 5.1 In `internal/llm/prompt/prompt.go`, ensure the "# Background tasks (event-driven, no polling)" section is appended regardless of whether `info.Prompt` is set: extract it from `CoderPrompt` into a standalone const and append it for every agent with tool access (`reg.HasTools`) after `basePrompt` is chosen at :317. NOTE: it must NOT be merged into `taskToolReportingPrompt` (that block is gated on `mode == agent`, which would skip subagents like workhorse that have bash).
- [x] 5.2 Reword the `task async` ack (`agent-tool-async.go:79-82`): keep `output_file` for resume/inspection but remove any framing of it as a progress-poll target; state that in a flow/non-interactive step the runtime blocks the turn until the subagent completes and the agent MUST NOT sleep or poll.
- [x] 5.3 Reword the `bash run_in_background` ack (`bash_background.go`) consistently — do not invite "Read the output file to inspect progress in the meantime" in non-interactive contexts.
- [x] 5.4 Unit test: prompt assembly for a custom-prompt agent (`info.Prompt != ""`) includes the no-poll section.

## 6. Spec deltas + sync

- [x] 6.1 `specs/background-tasks/spec.md` — MODIFY the "Non-interactive `agent.Run` MUST hold the turn …" requirement (anti-spin + drain-to-empty); ADD "No-poll guidance is delivered independent of the agent's system prompt".
- [x] 6.2 `specs/bash-background-mode/spec.md` — ADD "Foreground wall-clock waits are redirected to the task wait in non-interactive mode"; MODIFY "Background spawn ack format" (drop the read-mid-flight/inspect-progress invitation so 5.3 doesn't violate the main spec).
- [x] 6.3 `specs/task-async-mode/spec.md` — MODIFY "Subagent lifecycle in async mode" (step-scoped ctx, not `context.Background()`) and "Async spawn ack format" (no poll framing).
- [x] 6.4 `specs/flow-runtime-resume/spec.md` — ADD "Flow runner exposes a step-scoped context for detached subagents".
- [x] 6.5 Merge deltas into main `openspec/specs/` at archive time (`openspec archive`).

## 7. Integration / E2E

- [x] 7.1 Extend `cmd/background-e2e/main.go` with a non-interactive sleep-interception scenario exercising `PendingForSession` + `WaitForActiveTasks` through the bash-tool path (a pending task + a pure-wait command → wait return, not sleep).
- [x] 7.2 Extend `scripts/test/background.sh` with an assertion on the new JSON field(s).
- [x] 7.3 Rollout validation plan documented in `design.md` (Migration / Rollout): after the next c2-agent image bump, re-run a fan-out flow step on the `composer-developer` workspace; confirm no foreground `sleep` executes while tasks are pending, the step returns `struct_output` after the batch drains, and a forced step timeout cancels subagents + injects the `[wait-timeout]` note. (Post-deploy step — cannot run pre-merge; requires the deployed workspace.)

## 8. Final validation

- [x] 8.1 `make test` passes (agent / flow / task / tools race-sensitive packages).
- [x] 8.2 `go test -race ./internal/task ./internal/llm/agent ./internal/llm/tools ./internal/flow ./internal/llm/prompt` clean.
- [x] 8.3 `scripts/test/background.sh` passes with the new assertion(s).
- [x] 8.4 `GOOS=linux GOARCH=amd64 go build ./...` and `GOOS=windows GOARCH=amd64 go build ./...` succeed.
- [x] 8.5 `openspec validate non-interactive-wait-enforcement --strict` passes.
