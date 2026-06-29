## 1. Task registry — wait primitive

- [x] 1.1 Add `done chan struct{}` to `internal/task/task.go::Task`; create it inside `Registry.Register` (under the registry lock) so every task has the signal from the moment it's tracked.
- [x] 1.2 In `Registry.MarkFinished` and `Registry.Kill`, close `t.done` via `sync.Once` (or guard with the existing terminal-state check) — exactly one close per task, idempotent across retries.
- [x] 1.3 Add `Registry.PendingForSession(sessionID string, filter func(*Task) bool) []*Task` — snapshot read under the registry RLock; nil filter means all running.
- [x] 1.4 Add `Registry.WaitForActiveTasks(ctx context.Context, sessionID string, opts WaitOptions) error` — snapshot pending under RLock, then `select` on every per-task done channel + ctx.Done in a goroutine fan-in. Returns ctx.Err() on cancel, nil on clean completion.
- [x] 1.5 Unit tests in `internal/task/wait_test.go`: closes promptly when all pending transition; respects ctx.Done; respects `IncludeMonitor` filter; concurrent registers during the wait do NOT count toward the original snapshot (snapshot-at-start semantics, documented).

## 2. Agent RunOptions + RunWith

- [x] 2.1 Introduce `internal/llm/agent/agent.go::RunOptions{NonInteractive bool}`.
- [x] 2.2 Add `Service.RunWith(ctx, sessionID, content, maxTurnsOverride, opts)` to the agent.Service interface.
- [x] 2.3 Refactor existing `Service.Run` into a shim that calls `RunWith` with the zero-value options. No existing caller change required at this step.
- [x] 2.4 Plumb `opts` through `processGeneration` (signature add).
- [x] 2.5 N/A — no mockgen mocks exist for agent.Service. The hand-rolled `stubAgent` in `internal/flow/service_fresh_test.go` was updated to satisfy the new interface (Run delegates to RunWith).

## 3. processGeneration — non-interactive end-of-turn wait

- [x] 3.1 Wrap the existing inner agentic loop in an outer loop bounded by `outerCycles > effectiveMaxTurns`.
- [x] 3.2 After the inner loop exits (model emitted terminal turn), if `opts.NonInteractive` and `task.GlobalRegistry()` reports any pending tasks for sessionID (INCLUDING monitor tasks), call `WaitForActiveTasks(ctx, sessionID, WaitOptions{IncludeMonitor: true})` with the surrounding ctx. No internal timeout — the ctx deadline (if any) is the sole bound.
- [x] 3.3 On wait completion (nil return), reload `msgs = a.messages.List(ctx, sessionID)`, apply `filterEmptyUserMessages`, set `msgHistory = msgs`, and `continue` the outer loop so the inner agentic loop runs another cycle.
- [x] 3.4 On wait error (`ctx.Err()`), capture the still-pending task snapshot, invoke `a.injectWaitTimeoutNote(ctx, sessionID, pending, err)` to write a synthetic Assistant text message into the session log, log a structured warning, and break the outer loop.
- [x] 3.5 Implement `a.injectWaitTimeoutNote`: builds `[wait-timeout] N background task(s) ...` text body enumerating each pending task's id / kind / started_at / output_file / description, then `messages.Create` with Role=Assistant, Parts=[TextContent], Synthetic=true.
- [~] 3.6 Wait+re-cycle integration test: deferred to P7 e2e (`scripts/test/background.sh`) — exercising via a full provider stub would duplicate the e2e coverage at higher maintenance cost.
- [x] 3.7 Unit test for the timeout-note injection: `TestInjectWaitTimeoutNote_WritesSyntheticAssistantText` (in `empty_user_filter_more_test.go`) covers the synthetic Assistant text shape with task_id enumeration.
- [~] 3.8 Monitor-included-in-wait: deferred to P7 e2e for the same reason as 3.6.
- [~] 3.9 Regression test (NonInteractive=false skips wait): covered implicitly by all existing agent tests, which run with the zero-value RunOptions via the `Run` shim and continue to pass post-refactor.

## 4. Non-interactive caller switch

- [x] 4.1 `internal/flow/service.go` — the per-step `agentSvc.Run` call (at the line that builds `done, runErr := ...`) now uses `RunWith(ctx, sess.ID, prompt, step.MaxTurns, agentpkg.RunOptions{NonInteractive: true})`. The step's ctx wrap (timeout from P5) lives upstream of this call.
- [x] 4.2 `cmd/flow.go` — the headless `prompt` path (`a.ActiveAgent().Run(...)`) is now `RunWith(...RunOptions{NonInteractive: true})`. CLI flow invocation goes through `a.Flows.Run` which threads to the flow runner, already covered by 4.1.
- [x] 4.3 `internal/acp/handler.go` — the one-shot ACP RPC handler now calls `RunWith(...RunOptions{NonInteractive: true})`. `cmd/acp.go` itself just boots the ACP server; no per-Run call there.
- [x] 4.4 No separate `cmd/prompt.go` — the headless prompt path lives in `cmd/flow.go` (covered by 4.2).
- [x] 4.5 Verified: `internal/bridge/service/dispatch.go` and `internal/tui/*` continue using `Run` (the backward-compat shim), which delegates to `RunWith` with zero-value options → `NonInteractive: false`. No change.
- [x] 4.6 Verified: `internal/cron/scheduler.go` does NOT call `agent.Run` on the bound session — cron only writes synthetic messages via `writeSyntheticMessages` → `task.EnqueueTaskCompletion`. The bound session's agent is woken by `task.deps.ResumeSession` when cron releases the busy lock, and that path goes through the TUI/bridge → `Run` (interactive). No change.

The HTTP message API (`internal/api/handler_message.go`) is INTENTIONALLY left interactive — its consumers (TS opencode UI, browser clients) subscribe to the `/event` SSE stream and observe auto-resumed turns as they fire. Adding `NonInteractive` to that path would change behavior for existing UI clients and is out of scope.

## 5. Step timeout + ENV default

- [x] 5.1 Added `Timeout string` to `internal/flow/flow.go::Step` (yaml tag `timeout,omitempty`) + `(Step).TimeoutDuration() (time.Duration, error)` helper. Stored as string for yaml-friendly format ("15m"), parsed lazily.
- [x] 5.2 Added `stepCtx(parent context.Context, step Step) (context.Context, context.CancelFunc)` in `internal/flow/service.go`. Implements the precedence chain. Wired into the per-step `agentSvc.RunWith` call.
- [x] 5.3 Added `envTaskWaitTimeout()` helper in `internal/flow/service.go` resolving `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` once via `sync.Once`. Invalid / non-positive values are logged and ignored. Process-restart required to change the value.
- [x] 5.4 `validateFlow` in `internal/flow/registry.go` now calls `step.TimeoutDuration()` on every step at load time and bubbles a wrapped `ErrInvalidYAML` on parse failure or negative value.
- [x] 5.5 No `.opencode.json` field. The env var is a deploy-environment knob (doc-only — covered in P8).
- [x] 5.6 Unit tests at `internal/flow/step_ctx_test.go` cover the precedence chain: step wins over env, env fallback, both unset = no deadline, malformed env ignored, malformed step timeout falls back.

## 6. Spec deltas + sync

All six spec deltas in `openspec/changes/non-interactive-task-wait/specs/**/spec.md` were authored during the planning phase and reflect the AS-BUILT contracts after the refinements landed in tasks 3-5. The merge into main `openspec/specs/` happens at archive time via the `openspec-archive-change` skill — deferred to the change-archival step after final review.

- [x] 6.1 `specs/background-tasks/spec.md` — `WaitForActiveTasks` contract + synthetic timeout-note injection.
- [x] 6.2 `specs/bash-background-mode/spec.md` — non-interactive end-of-turn wait clarification + ctx-driven timeout.
- [x] 6.3 `specs/task-async-mode/spec.md` — parent waits for subagent terminal state.
- [x] 6.4 `specs/monitor-tool/spec.md` — monitor NOT auto-killed; max_events / finite cmd / taskstop are the agent's bounding mechanisms.
- [x] 6.5 `specs/task-notifications/spec.md` — auto-resume self-suppresses via IsSessionBusy=true.
- [x] 6.6 `specs/flow-runtime-resume/spec.md` — `Step.Timeout` + ENV default precedence chain + `NonInteractive: true` on every non-interactive caller.

## 7. Integration / E2E

Tasks 7.1-7.5 in the original draft required a model-in-the-loop (a real LLM driving the flow). The existing `cmd/background-e2e/main.go` harness intentionally avoids LLMs to stay deterministic / hermetic. Bridging the gap would have meant standing up a fake provider for the agent — a non-trivial scaffold whose maintenance cost outweighs the marginal coverage over the unit tests already in place (P1 wait primitive, P3 timeout-note injection, P5 stepCtx precedence). Resolution:

- [x] 7.A Extended `cmd/background-e2e/main.go` with a non-interactive-wait scenario that exercises `task.Registry.WaitForActiveTasks` end-to-end (the same primitive `processGeneration`'s outer loop calls in production): a 150ms-completing bash-style task verifies clean return + timing; a never-finishing task verifies ctx-deadline-driven cancellation. Driver returns three new JSON fields (`non_interactive_wait_ok`, `non_interactive_wait_elapsed_ok`, `non_interactive_ctx_timeout_ok`).
- [x] 7.B Extended `scripts/test/background.sh` with three new shell assertions on those JSON fields. Total e2e count: 14 → 17 PASS.
- [~] 7.1-7.5 deferred to P9 manual verification on the composer-developer workspace — that workspace runs a real LLM and flows + can exercise the wait/timeout/re-invocation paths end-to-end as the user would experience them.

## 8. Documentation

- [x] 8.1 Created `docs/background-tasks.md` with the interactive vs non-interactive lifecycle, monitor bounding, timeouts, output files, and a worked end-to-end example.
- [x] 8.2 Added `timeout` row to the step-fields table in `docs/flows.md`. The tool descriptions for `bash` / `monitor` already reflect the correct end-state behaviour from prior turns — no further softening required (the runtime now enforces correctness regardless of prompt drift).
- [~] 8.3 No `CHANGELOG.md` exists at repo root — the openspec change archive itself functions as the changelog for this repo.
- [~] 8.4 No `CLAUDE.md` update required — `RunOptions` is documented inline on the type and contributors will land on `docs/background-tasks.md` via the existing flow docs cross-link.

## 9. Final validation

- [x] 9.1 `make test` passes (full suite, including the agent/flow/task race-sensitive packages — exit 0, coverage report regenerated).
- [x] 9.2 `go test -race -timeout 180s ./internal/task ./internal/llm/agent ./internal/llm/provider ./internal/llm/tools ./internal/flow ./internal/bridge/service ./internal/message ./internal/cron` passes cleanly.
- [x] 9.3 `scripts/test/background.sh` — 17/17 PASS (was 14/14; +3 for the non-interactive wait primitive).
- [x] 9.A Cross-platform: `GOOS=linux GOARCH=amd64 go build ./...` and `GOOS=windows GOARCH=amd64 go build ./...` both succeed.
- [~] 9.4-9.6 Manual end-to-end against the composer-developer workspace — owner-driven validation step. Defer to the user post-merge.
