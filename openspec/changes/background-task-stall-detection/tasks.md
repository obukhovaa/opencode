# Tasks: background-task-stall-detection

## 1. Record the subagent's own session on the task

- [ ] 1.1 Add `AgentSessionID string` to `task.Task` (`internal/task/task.go`) with a
  comment stating that `SessionID` is the PARENT session (what `PendingForSession` keys
  on) and this is the subagent's own session, needed as the progress signal

- [ ] 1.2 Populate it in `agent-tool-async.go`'s `task.Task` literal from
  `taskSession.ID` — the value is already in scope, currently passed only to
  `waitAsyncAndNotify`

## 2. Progress signal

- [ ] 2.1 Define the progress-source interface the registry needs (last message
  create/update time for a session id), so `internal/task` does not take a dependency
  on the message store directly — wire the concrete implementation where the registry
  is constructed

- [ ] 2.2 Implement `lastProgressAt(t *Task) time.Time`: most recent message on
  `t.AgentSessionID`, falling back to `t.StartedAt` when the subagent has persisted
  none. A `KindTask` entry with an empty `AgentSessionID` reports no stall

## 3. Threshold configuration

- [ ] 3.1 Add the stall-threshold config field (duration, default 30m) in
  `internal/config/config.go`; zero or negative disables detection

- [ ] 3.2 Field description MUST state the operator-facing constraint: the value must
  exceed the largest single tool-call budget in the deployment (`bash` foreground hard
  cap 10m; MCP per-call budget, default 5m but raisable without limit via
  `callToolTimeoutSeconds`)

- [ ] 3.3 `cmd/schema/main.go` + regenerate `opencode-schema.json`; add the
  `viper.Unmarshal` round-trip test required by CLAUDE.md for new config fields

## 4. Detection and termination

- [ ] 4.1 Implement stall evaluation in `internal/task/registry.go` over `KindTask`
  entries in `StateRunning` only; exempt `KindBash`, `KindMonitor`, `KindCron` by an
  explicit kind check, not by an implicit property

- [ ] 4.2 Terminate via the existing `Kill` semantics so downstream behaviour is
  identical to `taskstop` (terminal state stamped up front, `done` signalled, `Cancel`
  invoked)

- [ ] 4.3 `logging.Warn` on termination naming task id, `AgentSessionID`, and the age
  of last observed progress

- [ ] 4.4 Decide and document where evaluation is driven from — reuse the drain's
  existing progress ticker rather than adding a second timer, so a disabled threshold
  costs nothing

## 5. Tests

- [ ] 5.1 `KindMonitor` with no events and no output past the threshold is NOT killed —
  the regression guard for the whole exemption; assert on kind, not on a proxy

- [ ] 5.2 `KindBash` silent past the threshold is NOT killed

- [ ] 5.3 `KindTask` with stale last-message is killed; state becomes `StateKilled`,
  `done` closes, `Cancel` fires

- [ ] 5.4 `KindTask` whose session gains a message before the threshold is NOT killed,
  and a task that keeps persisting messages is never killed however long it runs (the
  no-total-runtime-cap guarantee)

- [ ] 5.5 `KindTask` with an empty `AgentSessionID` is never killed

- [ ] 5.6 Threshold zero / negative disables detection entirely

- [ ] 5.7 Drain integration: a stalled task lets `drainSessionTasks` return `nil` (the
  every-task-terminal path) rather than `ctx.Err()`, and the synthetic completion pair
  lands in the parent session

- [ ] 5.8 `go test -race ./internal/task/ ./internal/llm/agent/ ./internal/config/`
  green; `go build ./...` clean

## 6. Docs

- [ ] 6.1 `docs/background-tasks.md`: the per-kind scope table with the `Proc` vs
  `Cancel` rationale, the progress signal, and the threshold-vs-tool-call-budget
  relationship

- [ ] 6.2 State explicitly in the docs that `monitor` is bounded by `max_events` /
  `taskstop` / subprocess exit / flow deadline and is NOT time-bounded, so a future
  reader does not "fix" that by extending stall detection to it

## 7. Verification

- [ ] 7.1 Reproduce the GENAI-270 shape: a subagent blocked in a tool call that never
  returns, with no step timeout and no `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT`;
  confirm the step now completes at the threshold with its `struct_output` intact
  instead of parking to the flow deadline

- [ ] 7.2 Confirm a `monitor` with `max_events: 1` whose pattern matches only after a
  long silence still delivers its event and terminal notification, unaffected
