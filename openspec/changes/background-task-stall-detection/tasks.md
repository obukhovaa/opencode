# Tasks: background-task-stall-detection

## 1. Record the subagent's own session on the task

- [x] 1.1 Add `AgentSessionID string` to `task.Task` (`internal/task/task.go`) with a
  comment stating that `SessionID` is the PARENT session (what `PendingForSession` keys
  on) and this is the subagent's own session, needed as the progress signal

- [x] 1.2 Populate it in `agent-tool-async.go`'s `task.Task` literal from
  `taskSession.ID` — the value is already in scope, currently passed only to
  `waitAsyncAndNotify`

## 2. Progress signal

- [x] 2.1 Implemented differently, and better: no registry interface change at all.
  Detection lives in `internal/llm/agent` (which already holds the message service) and
  is passed to the drain as a `stallPolicy{threshold, lastProgress}` value. `internal/task`
  therefore stays a leaf package with no message-store dependency and no new interface
  method, and the drain stays independent of the agent — its zero-value policy disables
  detection, which is what every non-agent caller (including the existing tests) wants

- [x] 2.2 `agent.lastSubagentProgress` via `messages.ListLatest(ctx, AgentSessionID, 1)`,
  preferring `UpdatedAt` over `CreatedAt` so a long streaming generation also counts as
  progress. Falls back to `StartedAt` when the subagent has persisted nothing AND on any
  read error, so a failed lookup can never manufacture a stall. A `KindTask` entry with
  an empty `AgentSessionID` is skipped outright

## 3. Threshold configuration

- [x] 3.1 Add the stall-threshold config field (duration, default 30m) in
  `internal/config/config.go`; zero or negative disables detection

- [x] 3.2 Field description MUST state the operator-facing constraint: the value must
  exceed the largest single tool-call budget in the deployment (`bash` foreground hard
  cap 10m; MCP per-call budget, default 5m but raisable without limit via
  `callToolTimeoutSeconds`)

- [x] 3.3 `cmd/schema/main.go` + regenerate `opencode-schema.json`; add the
  `viper.Unmarshal` round-trip test required by CLAUDE.md for new config fields

## 4. Detection and termination

- [x] 4.1 Implemented in `internal/llm/agent/agent.go` (`stallPolicy.killStalled`), NOT
  in `internal/task/registry.go` as originally planned — see the 2.1 deviation, which
  keeps `internal/task` a leaf package. Scope is `KindTask` in `StateRunning` only, with
  `KindBash` / `KindMonitor` / `KindCron` exempt by an explicit kind check

- [x] 4.2 Terminate via the existing `Kill` semantics so downstream behaviour is
  identical to `taskstop` (terminal state stamped up front, `done` signalled, `Cancel`
  invoked)

- [x] 4.3 `logging.Warn` on termination naming task id, `AgentSessionID`, and the age
  of last observed progress

- [x] 4.4 Decide and document where evaluation is driven from — reuse the drain's
  existing progress ticker rather than adding a second timer, so a disabled threshold
  costs nothing

## 5. Tests

- [x] 5.1 `KindMonitor` / `KindBash` / `KindCron` past the threshold are NOT killed.
  Review caught that the first version asserted on a PROXY after all: the fixture blanked
  `AgentSessionID` for exempt kinds, so the other guard in `killStalled` was doing the
  work and dropping the kind check alone left the tests green. The fixture now gives
  exempt kinds a real session id, and dropping only the kind conjunct fails all three

- [x] 5.2 `KindBash` silent past the threshold is NOT killed

- [x] 5.3 `KindTask` past the threshold is killed: state becomes `StateKilled` and
  `done` closes. `Cancel` firing is NOT asserted — the fixture task carries a nil
  `Cancel`, so `Registry.Kill`'s cancel branch is not exercised by any test in the repo
  (pre-existing, but load-bearing here: if `Cancel` stopped firing the subagent's loop
  would keep turning after the drain returned)

- [x] 5.4 The no-total-runtime-cap guarantee is covered. The message-log behaviour is
  covered by `TestProgressFromMessages` against the extracted `progressFromMessages`
  (UpdatedAt-over-CreatedAt, zero timestamps, backdated clamp, newest-of-several,
  read error, no messages) — added after review found `lastSubagentProgress` had ZERO
  coverage and that flipping its fallback to the zero time passed the whole suite

- [x] 5.5 `KindTask` with an empty `AgentSessionID` is never killed

- [x] 5.6 Threshold zero / negative disables detection entirely

- [x] 5.7 Drain integration: `TestDrainReturnsAfterStallKill` asserts the `nil`
  (every-task-terminal) return rather than `ctx.Err()`. The synthetic-completion-pair
  assertion is NOT included: task termination signals the completion channel before the
  pair is written, so its arrival is not ordered ahead of the drain's return. The spec
  was corrected to describe that rather than promise it

- [x] 5.8 `go test -race ./internal/task/ ./internal/llm/agent/ ./internal/config/`
  green; `go build ./...` clean

## 6. Docs

- [x] 6.1 `docs/background-tasks.md`: the per-kind scope table with the `Proc` vs
  `Cancel` rationale, the progress signal, and the threshold-vs-tool-call-budget
  relationship

- [x] 6.2 State explicitly in the docs that `monitor` is bounded by `max_events` /
  `taskstop` / subprocess exit / flow deadline and is NOT time-bounded, so a future
  reader does not "fix" that by extending stall detection to it

## 7. Verification

- [ ] 7.1 Reproduce the GENAI-270 shape: a subagent blocked in a tool call that never
  returns, with no step timeout and no `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT`;
  confirm the step now completes at the threshold with its `struct_output` intact
  instead of parking to the flow deadline

- [ ] 7.2 Confirm a `monitor` with `max_events: 1` whose pattern matches only after a
  long silence still delivers its event and terminal notification, unaffected

## Completion notes

Tasks 1-6 implemented on `feat/GENAI-270-task-stall-detection`. `go build ./...` clean;
`go test -race ./internal/llm/agent/ ./internal/task/ ./internal/config/ ./internal/flow/`
green.

**Stacked on `obukhovaa/opencode#45`**, not independent as the proposal first said. The
detection hook reuses that PR's drain progress ticker rather than adding a second timer
(task 4.4), so this branch is based on it and #45 should merge first. The two changes
remain conceptually independent — either would have shortened the incident on its own.

The monitor exemption was verified to be a real guard rather than a tautology: replacing
the `t.Kind != task.KindTask` check with `if false` makes `TestKillStalledScope` fail on
all three exempt kinds, with the monitor task reaching `state=killed`. That is the test
that matters most here — killing a silent monitor would break the tool outright, since
polling an external source without LLM calls is exactly what it is for.

Deviations from the plan are recorded inline at 2.1 and 2.2. Tasks 7.1/7.2 (end-to-end
reproduction, and a `max_events: 1` monitor surviving a long silence in a real run) are
left for review — they need a live flow rather than unit scaffolding.
