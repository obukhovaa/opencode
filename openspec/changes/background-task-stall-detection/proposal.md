## Why

The non-interactive end-of-turn drain waits for every pending background task to
reach a terminal state. A task that can never reach one parks the whole flow step
until the orchestrator's flow deadline — even when the step's own work is finished
and its `struct_output` has already been accepted.

That happened in production: job `eb4854de1a99d68d` (GENAI-270) burned 1h50m of a
2h15m job budget on an `implement` step whose MR was already open and mergeable. An
async subagent had issued two MCP tool calls that could never return, so the subagent
never terminated, so the drain never returned, so the flow never routed.

The specific unbounded wait behind that incident is fixed separately (an unbounded
`c.Initialize` in `runTool`, `obukhovaa/opencode#45`). This change addresses the
class rather than the instance: **any** subagent that stops making progress produces
the same multi-hour park, and the runtime currently has no way to tell.

The flow-level timeout is and remains the outer safety net, and it works — it is what
would eventually have ended this job. What is missing is a *graceful* inner bound.
The difference matters: the flow deadline kills the job and loses the step's accepted
output, whereas terminating one demonstrably-dead task lets the parent re-cycle and
finish the step normally with the output it already had.

The bound has to live in opencode, not in the orchestrator. Only opencode owns the
task registry and can see whether a task is actually making progress; the orchestrator
sees a heartbeating pod and nothing else. And a per-step wall-clock number is the wrong
instrument regardless — step cost varies legitimately from run to run, so any value
large enough to be safe is too large to be useful.

## What Changes

- **Stall detection for subagent-backed tasks.** A `task` (async subagent) whose own
  session has persisted no new message for longer than a configurable threshold is
  treated as stalled and killed through the existing `Registry.Kill` path. That marks
  it terminal, signals its `done` channel, and cancels its context — so the drain
  returns for the ordinary reason (every task terminal), the existing synthetic
  completion note lands in the parent session, and the parent re-cycles and finishes.

- **Process-backed tasks are explicitly out of scope.** `bash` and `monitor` tasks
  carry a real `*os.Process`; their liveness is definitional rather than inferred — the
  process is running or it is not. A `monitor` polling an external source with no
  output for an hour is doing precisely its job, and killing it on silence would break
  the tool. Monitors are bounded by `max_events`, `taskstop`, subprocess exit, and the
  flow deadline, which is what the `monitor-tool` spec already prescribes.

- **`Task` learns the subagent's own session id.** `Task.SessionID` is the *parent*
  session (it is what `PendingForSession` keys on); the subagent's session id is
  currently passed only to `waitAsyncAndNotify` and never recorded. Progress cannot be
  measured without it.

- **The drain's contract is unchanged.** Notably this needs **no** amendment to the
  `background-tasks` requirement that "the wait MUST NOT impose its own timeout — the
  surrounding `ctx` is the sole deadline source". That clause governs the *wait*; stall
  detection governs *task lifecycle*. The wait still blocks until every task is
  terminal, with no deadline of its own; a stalled task simply becomes terminal sooner.
  No legitimately-running task is ever capped.

## Capabilities

### New Capabilities

- `background-task-stall-detection`: progress-based termination of subagent background
  tasks — the per-kind scope and its rationale, the progress signal, the configurable
  threshold and its lower bound, the kill-and-notify behaviour, and the explicit
  preservation of the drain's existing no-own-timeout contract.

## Impact

**`github.com/opencode-ai/opencode`**

- `internal/task/task.go`: `Task.AgentSessionID` (the subagent's own session), and a
  progress accessor.
- `internal/task/registry.go`: stall evaluation over `KindTask` entries, and the
  kill-on-stall path reusing `Kill`'s existing terminal semantics.
- `internal/llm/agent/agent-tool-async.go`: record `taskSession.ID` on the registered
  task.
- `internal/config/config.go` + `cmd/schema/main.go` + regenerated
  `opencode-schema.json`: the threshold knob.
- `docs/background-tasks.md`: the stall contract, the per-kind scope table, and the
  relationship between the threshold and the largest single tool-call budget.

**Independent of `obukhovaa/opencode#45`.** Either can merge first. If stall detection
lands first it will catch that unbounded handshake as a stalled task, which is the
defence-in-depth the two changes are meant to provide together.
