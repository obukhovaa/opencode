## Why

An MCP server that starts but never answers wedges a tool call **forever**, and the
wedge propagates all the way up to the flow job. Observed in production (Piano
`ai-agents` prod-euc1, opencode `v0.16.3`, job `eb4854de1a99d68d`, tracked as
GENAI-270): a `developer-react-on-jira` `implement` step ran 2h13m with **1h50m of
total silence** on a step whose work was already finished — its MR was open and its
`struct_output` had been accepted.

`runTool` bounds the call but not the handshake that precedes it
(`internal/llm/agent/mcp-tool.go:562`):

```go
_, err := c.Initialize(ctx, initRequest)          // raw ctx: UNBOUNDED
...
callCtx, cancelCall := context.WithTimeout(ctx, callTimeout)
result, err := c.CallTool(callCtx, toolRequest)   // bounded, mcpCallToolTimeout (5m)
```

Every MCP tool invocation runs `StartClient` (bounded 20s), then `Initialize`
(unbounded), then `CallTool` (bounded 5m). The middle step has no deadline of its own,
so a server that accepts a request and never replies parks the calling goroutine for
the life of the process.

That is what happened, and the attribution is by elimination rather than from a stack.
An `mcp-atlassian` child process spawned at 06:07:18 was still alive 1h50m later — `mcpTool.Run` does `StartClient` with a
`defer c.Close()`, so a live child process is proof that `runTool` never returned. The
process had no writable channel back to opencode: its fd 1 (stdout, the MCP response
channel) pointed at `/dev/null` with no pipe write-end anywhere in its fd table, while
opencode still held the read end. The pod had logged
`ERROR: Error reading from stdout: read |0: file already closed` moments earlier.

Two properties turned one wedged call into a multi-hour job hang:

1. **The wedge is invisible to the existing 5-minute budget.** Both tool parts stayed
   persisted as `status: running` — the model had finished streaming the calls and no
   result ever came back. The block sits upstream of `CallTool`, so
   `mcpCallToolTimeout` never applied.

2. **The wedge is load-bearing for the whole step.** The wedged calls belonged to an
   async `task` subagent, so the subagent never reached a terminal state, so the
   parent's non-interactive end-of-turn drain never returned.

Note what this change deliberately does **not** touch. The `background-tasks` spec
states that the drain "MUST NOT impose its own timeout — the surrounding `ctx` is the
sole deadline source", with the deadline coming from `Step.Timeout` or
`OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT`. That requirement stands and is correct.
In the observed incident **neither source was configured** — no flow step declared a
`timeout`, and the env var was unset in the pod — so `stepCtx` fell through to "parent
ctx unwrapped" and the drain had no deadline at all. That is a deployment gap in the
Piano workspace and Helm chart, not an opencode defect, and it is out of scope here.
This change fixes the defect that made an unbounded drain fatal rather than merely
theoretical: a tool call that can never complete.

## What Changes

- **The MCP handshake inside a tool call gets its own deadline.** `runTool`'s
  `c.Initialize` call is wrapped in `mcpInitTimeout`, mirroring the existing `callCtx`
  pattern, and reports the same style of actionable timeout error the `CallTool`
  deadline branch already produces — so the model receives a normal tool error and can
  choose another approach instead of hanging.

- **The client-cache waiter stops being unbounded.** `getToolsAttempt` waits on a bare
  `<-entry.done` whose only stated bound is a comment ("the fetcher closes entry.done
  within mcpInitTimeout"). A fetcher wedged against a mute server never reaches its
  `defer close(entry.done)`, so every waiter blocks forever. The receive becomes a
  `select` over `entry.done`, the registry ctx, and an `mcpInitTimeout` backstop.

- **A wedged or timed-out client is always closed.** Timing out the handshake must not
  leak the child process the handshake was talking to; the close path is made
  unconditional so no MCP server process outlives the tool call that spawned it.

- **The drain says something while it waits.** The end-of-turn drain currently logs
  once on entry and then nothing, which is what produced 1h50m of unbroken silence and
  made this incident hard to distinguish from a dead process. It gains a periodic
  progress log naming the still-pending tasks. This is observability only — no timeout
  is introduced and the "sole deadline source" requirement is restated unchanged.

## Capabilities

### New Capabilities

- `mcp-call-deadline-bounds`: every wait on an MCP server during tool resolution and
  tool invocation is bounded by an explicit deadline — the per-call handshake, the
  shared client-cache entry wait, and client cleanup on every exit path — so no MCP
  server can park an agent turn indefinitely.

### Modified Capabilities

- `background-tasks`: the non-interactive end-of-turn drain emits a periodic
  still-waiting log naming the pending tasks. The existing prohibition on the drain
  imposing its own timeout is explicitly preserved.

## Impact

**`github.com/opencode-ai/opencode`**

- `internal/llm/agent/mcp-tool.go`: bound `Initialize` in `runTool` (L562) with
  `mcpInitTimeout` and an actionable timeout message; replace the bare `<-entry.done`
  receive in `getToolsAttempt` (L377) with a bounded `select`; make the client close
  path unconditional so a timed-out handshake cannot leak its child process.
- `internal/llm/agent/mcp-tool_test.go`: extend the existing `fakeMCPClient` with
  configurable per-method blocking; add cases for a handshake that never returns, a
  cache fetcher that never closes `entry.done`, and the no-leak assertion.
- `internal/llm/agent/agent.go`: periodic still-waiting log in the non-interactive
  drain loop (`drainSessionTasks`, L1246).
- `internal/llm/agent/drain_test.go`: assert the progress log fires while a task is
  pending and that no timeout is introduced.
- Docs: `docs/mcp.md` (or the MCP section of `AGENTS.md`) gains the deadline table for
  the three MCP wait points; `docs/background-tasks.md` notes the drain progress log.

**Out of scope, tracked elsewhere (GENAI-270 follow-ups on the Piano side)**

- Setting `OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT` in the c2-agent Helm chart, and
  declaring per-step `timeout` values in the `piano-developer` shared flows, so the
  drain has a deadline source at all.
