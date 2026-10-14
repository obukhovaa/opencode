# Daemon Runtime Resilience (CD-5305)

## Why

The dev c3-daemon was down for about 18 hours on 2026-10-13/14. In the opencode half of the incident, `/stop` was not a chat command, so it was queued as a prompt behind the run it was meant to stop; `/reset` unbound the session without cancelling that run; `/agent` switched the agent mid-run, so the next message started a second run in the same process while the first was still stuck in a tool call. After the pod restarted, the `/agent` choice was gone and the session was replayed under the daemon record's agent. That agent did not declare the tools an earlier server-side tool search had found, Bedrock rejected every request (400 non-stream, `RST_STREAM INTERNAL_ERROR` on the stream), and each retry claimed a fresh connection it did not make.

## What Changes

- **`tool_reference` filter.** `convertMessages` takes the names the request declares (from the same `convertTools` output, deferred and activated tools included) and drops replayed references to anything else. A turn that lost a reference drops its reasoning; a search left with nothing is removed with its `server_tool_use`; plain `tool_use` blocks stay. When the affected turn is the open tool-use turn (last assistant turn, ends in `tool_use`, only tool results after it), that one request goes out with thinking disabled, no thinking blocks and no interleaved-thinking beta. One WARN per request. `count_tokens` uses the same filter.
- **`/stop` and `/cancel`** are aliases of `/abort`. A shared `cancelSession` cancels the run (any agent instance), the app drain worker and queue, and the dispatcher's queued inbound; a message waiting out the busy session gives up. `/reset` cancels before it unbinds. `/agent <x>` is refused while the bound session is busy.
- **Stdio eviction on the call's own timeout.** A stdio client whose `tools/call` ran out its per-call timeout is retired, like one the caller abandoned.
- **Agent-loop watchdog.** `toolCallTimeoutSeconds` (agent-level, top-level default 1200, 0 = off) bounds every tool call except `task`, `question` and `bash`; an MCP tool gets at least its server's call timeout + 60s. On expiry the call's ctx is cancelled, the call abandoned and an error result recorded. A cancelled run no longer waits on a tool that ignores its ctx.
- **Agent persisted per session.** `sessions.agent_name`, written by `/agent <x>` on a bound session and stamped on every bridge-dispatched run. The dispatcher and the heartbeat paths run a session under `App.AgentForSession`: the persisted agent if it is a registered primary agent, else the active one with a WARN.
- **RST_STREAM retry on a new connection.** The retry forces a new HTTP/2 connection and logs the stream ids.

## Capabilities

### Modified Capabilities

- `chat-bridge`: `/stop`, `/cancel`; `/abort` and `/reset` cancel queued work; `/agent` refused while busy; agent persisted per session.
- `deferred-tools`: replayed tool references are limited to the declared tools.
- `mcp-client-pool`: stdio client retired on its own call timeout.
- `mcp-call-deadline-bounds`: agent-loop watchdog per tool call.

## Impact

- `internal/llm/provider/anthropic.go` (`convertMessages`, `declaredToolNames`, `requestFor`, `requestOptions`), the stream retry branches.
- `internal/bridge/service` (`cancel.go`, `commands.go`, `dispatch.go`, heartbeat paths), `internal/app` (`StopSessionQueue`, `AgentForSession`).
- `internal/llm/agent` (`mcp-tool.go`, `tool_watchdog.go`, `agent.go`), `internal/config` (`toolCallTimeoutSeconds`), `opencode-schema.json`.
- `internal/db` migration `20261014120000_add_session_agent_name`, `internal/session`.
