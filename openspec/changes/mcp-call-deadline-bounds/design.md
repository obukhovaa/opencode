# Design: mcp-call-deadline-bounds

## The three MCP wait points and their current bounds

| # | Wait | Location | Bound today | Bound after |
|---|---|---|---|---|
| 1 | Spawn / transport start | `StartClient` → `c.Start(startCtx)` | 20s explicit | unchanged |
| 2 | Protocol handshake | `runTool` → `c.Initialize(ctx, …)` | **none** (raw ctx) | `mcpInitTimeout` (30s) |
| 3 | Tool invocation | `runTool` → `c.CallTool(callCtx, …)` | `mcpCallToolTimeout` (5m, per-server override) | unchanged |
| 4 | Shared cache entry | `getToolsAttempt` → `<-entry.done` | **none** (bare receive) | `select` over done / registry ctx / `mcpInitTimeout` |

Points 2 and 4 are the gaps. They are independent: 2 is per-call and per-client, 4 is
process-wide and shared across every agent, so a single wedged fetcher at 4 is the
wider blast radius even though 2 is what fired in the observed incident.

## Why `mcpInitTimeout` and not a new knob

`mcpInitTimeout` (30s) already means "how long one MCP server gets to become usable" —
it bounds `StartClient` + `Initialize` + `ListTools` on the registry fetch path
(`fetchCtx` at L400). Reusing it for the per-call handshake keeps one concept rather
than two, and needs no config surface or schema change. A server that cannot complete
a handshake in 30s is broken, not slow: `Initialize` is a single request/response with
no work behind it. Deliberately **not** reusing `mcpCallToolTimeout` (5m) — that budget
exists for tools that legitimately do slow work (CI queries, builds), which a handshake
never does.

Per-server overridability is intentionally omitted. `callToolTimeoutSeconds` exists
because tool latency is genuinely server-specific; handshake latency is not.

## Point 2 — bounding the handshake

Mirror the existing `callCtx` shape immediately above it, including the same care about
attributing the deadline:

```go
initCtx, cancelInit := context.WithTimeout(ctx, mcpInitTimeout)
result, err := c.Initialize(initCtx, initRequest)
cancelInit()
if err != nil {
    if ctx.Err() == nil && initCtx.Err() == context.DeadlineExceeded {
        return tools.NewTextErrorResponse(fmt.Sprintf(
            "MCP server for tool %q did not complete its handshake within %s …",
            toolName, mcpInitTimeout)), nil
    }
    return tools.NewTextErrorResponse(err.Error()), nil
}
```

Two details that matter:

- **`cancelInit()` is called explicitly, not deferred.** A `defer` would hold the
  timer for the whole (up to 5-minute) `CallTool` that follows. The existing
  `defer cancelCall()` is fine because `callCtx` is the last thing in the function.
- **The `ctx.Err() == nil` guard is copied verbatim from the `CallTool` branch.** It
  keeps us from blaming our own 30s budget when the real cause was an upstream
  cancellation — the same reasoning the existing comment gives.

The returned value stays a **tool error, not a Go error** (`NewTextErrorResponse`,
`nil` error), consistent with every other failure in `runTool`. That is what lets the
model see the failure and route around it instead of the turn dying.

## Point 4 — bounding the shared cache wait

The current code is:

```go
if loaded {
    // cache/reuse — wait for the (possibly in-flight) fetch. Bounded:
    // the fetcher closes entry.done within mcpInitTimeout.
    <-entry.done
```

The comment states an invariant the code does not enforce. It holds only if the
fetcher always reaches its `defer close(entry.done)`, and the fetcher's own work is
bounded by `fetchCtx` (`mcpInitTimeout`) — so in principle it does. The failure mode is
a fetcher blocked in a call that ignores its ctx, which is precisely what a mute stdio
server produces. `defer` does not run until the function returns, so the entry stays
open and **every** waiter — including agents with no interest in that server — blocks
forever.

The fix is a bounded `select`. On expiry the waiter must not poison the shared entry
for others; it returns no tools for that server and lets the fetcher own the entry's
lifecycle, matching the existing behavior when `entry.err != nil`:

```go
select {
case <-entry.done:
    // existing expiry / cache-hit handling, unchanged
case <-r.discoveryCtx().Done():
    return toolsToAdd
case <-time.After(mcpInitTimeout):
    logging.Warn("MCP client cache wait exceeded mcpInitTimeout; skipping server",
        "server", name)
    return toolsToAdd
}
```

The backstop duration is the same `mcpInitTimeout` the comment already claims, so this
is enforcement of the documented contract rather than a behavior change. Note the
waiter timeout must be **at least** the fetcher's own budget, otherwise a healthy
slow-start server would be skipped by waiters while the fetcher is still legitimately
working. They are equal here, which is the minimum safe value; if flakiness appears in
practice the waiter's backstop is the one to lengthen, not the fetcher's.

## Point 2/4 — not leaking the child process

`mcpTool.Run` already has `defer c.Close()`, so the close happens once `runTool`
returns — which the handshake bound now guarantees. The remaining hole is
`StartClient` returning a client whose `Start` succeeded but which is then abandoned on
an early error path. Audit the three `return` sites between `StartClient` and
`defer c.Close()` and ensure each closes, so the invariant becomes: **no MCP server
process outlives the tool call that spawned it.** The production incident left a live
child process plus unreaped `node` and `git` zombies under pid 1, so this is worth
asserting rather than assuming.

## Drain observability (background-tasks delta)

The drain logs `Non-interactive turn complete with pending background tasks; waiting
before re-cycling` on entry, then nothing. In the incident that produced 1h50m of
silence, indistinguishable in the pod log from a hung or dead process.

Add a ticker (60s) inside `drainSessionTasks`'s wait that logs the still-pending task
IDs, kinds and ages. This is strictly additive: the wait condition is untouched, no
deadline is introduced, and the `background-tasks` requirement that the drain impose no
timeout of its own is restated in the delta so a future reader does not mistake the
ticker for one.

## Alternatives considered

**Give `WaitForActiveTasks` a per-task timeout.** Rejected — it directly contradicts
the standing `background-tasks` requirement that "the wait MUST NOT impose its own
timeout", and it treats the symptom. With point 2 fixed the task terminates on its own,
which is the behavior the drain was designed around. (This was the first fix proposed
on GENAI-270, before reading the spec; recording it here so the ticket and the change
do not disagree silently.)

**A goroutine-leak watchdog.** Rejected as too broad for this change: it would catch
this class of bug generically, but the specific unbounded waits are few, known, and
cheap to bound directly.

**Making the per-call handshake configurable per server.** Rejected; see "Why
`mcpInitTimeout` and not a new knob".

## Diagnostic notes worth keeping

The signature of this failure, for whoever hits the next variant:

- `status: running` on a persisted tool part means only "the model finished streaming
  the call and no result has come back" (`resolveToolStatus`, `internal/api/convert_message.go`).
  It says nothing about whether execution began, so it does not by itself locate the
  block. Do NOT reach for a `started` / `state.time.start` field to settle that — the API
  model (`APIToolState`, `internal/api/types.go`) carries no timestamps at all, and a jq
  path into one returns a null that reads like evidence and is not. This cost a wrong
  root-cause claim on GENAI-270 before the process evidence below corrected it.
- An MCP child process still alive long after its call is the load-bearing evidence: it
  proves `runTool` never returned, because `mcpTool.Run` closes via `defer c.Close()`.
  `ls -l /proc/<pid>/fd` on a mute stdio server shows fd 1 on `/dev/null` with no pipe
  write-end while opencode still holds the read end. From there the block is pinned by
  elimination — inside `runTool` the only waits are `Initialize` (unbounded) and
  `CallTool` (bounded, so it would have returned) — not by direct observation.
- `/debug/pprof` is **not** registered on the opencode API (404 even authenticated), so
  a goroutine dump is unavailable on a live pod and `SIGQUIT` would kill the job. The
  usable substitute is the API itself: Basic auth with `OPENCODE_SERVER_PASSWORD`, then
  `GET /session/{id}/message` and read the persisted tool-part states.
