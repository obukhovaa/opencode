## 1. tool_reference filter (D4)

- [x] 1.1 `convertMessages(messages, declared)`; `declaredToolNames` over the `convertTools` output; send, stream and count_tokens build tools first (`requestFor`).
- [x] 1.2 Undeclared references dropped before the ordered/fallback choice; a turn that lost one drops its reasoning; an emptied search is skipped with its `server_tool_use`.
- [x] 1.3 One WARN per request with the dropped count, tool names, turns and whether thinking was disabled.
- [x] 1.4 Open tool-use turn: `disableThinking`, thinking param cleared, thinking blocks stripped, interleaved-thinking beta removed (`requestOptions`).
- [x] 1.5 Tests: `anthropic_toolreference_test.go`; count_tokens tests declare the tools their history found.

## 2. Stop and tool calls (D2)

- [x] 2.1 `/stop`, `/cancel` aliases; help text; docs/bridge.md.
- [x] 2.2 `Service.cancelSession`, `App.StopSessionQueue`, `sessionDispatch.abortQueued` + abort epoch; used by `/abort` and `/reset`.
- [x] 2.3 `/agent` refused while the bound session is busy.
- [x] 2.4 Stdio client evicted on `errMCPCallTimeout`.
- [x] 2.5 `runToolBounded` + `toolCallLimit` at both tool-run sites; `toolCallTimeoutSeconds` in config and schema.
- [x] 2.6 Tests: `commands_cancel_test.go`, `TestMCPStdio_EvictedAfterItsCallTimeout`, `tool_watchdog_test.go`, `config_tool_call_timeout_test.go`, `TestMCPHTTP_HungCallDoesNotBlockOtherSessions` (HTTP freeze reproduction).

## 3. Agent per session (D3)

- [x] 3.1 Migrations `20261014120000_add_session_agent_name` (mysql, sqlite), schema, `SetSessionAgentName` query.
- [x] 3.2 `session.Session.AgentName`, `Service.SetAgentName`.
- [x] 3.3 `App.AgentForSession` in the dispatcher and the heartbeat paths; `/agent <x>` and every bridge run persist the agent.
- [x] 3.4 Tests: restart on the same DB, unknown name falls back, heartbeat uses the session's agent.

## 4. RST_STREAM retry (D5)

- [x] 4.1 The retry forces a new HTTP/2 connection and logs the previous and new stream ids; test against an HTTP/2 `httptest` server counting connections.
