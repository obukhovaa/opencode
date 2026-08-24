# Tasks

- [x] Add `internal/llm/agent/session_locks.go`: global ledger, `SessionBusy`, `acquireSessionSlot`, `releaseSessionSlot`.
- [x] `RunWith`: atomic global acquire replaces the per-instance check-then-store; release as outermost defer.
- [x] `IsSessionBusy` / `TryLockSession` / `UnlockSession` re-based on the global ledger.
- [x] `Cancel`: cross-instance fallback via the global slot holder (cron sentinel skipped).
- [x] `task.Task.FlowOwned` field + stamp in all three spawn paths (task async, bash background, monitor).
- [x] `EnqueueTaskCompletion`: skip auto-resume for flow-owned tasks (after pair write + MarkFinished).
- [x] `taskDeps.IsSessionBusy` → `agent.SessionBusy`.
- [x] Unit tests: cross-instance exclusion, unlock-vs-run-holder, cross-instance cancel, acquire atomicity (`internal/llm/agent/session_locks_test.go`); flow-owned resume suppression for terminal + monitor-event completions (`internal/task/background_test.go`).
- [x] E2E: `cmd/background-e2e` scenario 9 (plain spawn still resumes; flow-owned spawn does not) + `scripts/test/background.sh` asserts.
- [x] `go build ./...`, `make test`, `make test-e2e` green.
