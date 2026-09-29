# Refuse Pool-Mode Inbound for Sessions No Live Step Owns

## Why

A warm-pool pod's container can restart mid-step. The orchestrator's inbound binding still points at the pod, so operator replies reach `POST /router/inbound` on a process with no flow run. The handler accepts them, and the message goes to the workspace default agent, which runs on the lost step's session. The chat looks alive, but the step never advances.

## What Changes

- In pool mode, `POST /router/inbound` returns `409 {"sessionNotOwned": true}` unless a live interactive flow step in this process owns the peer's session, and does not enqueue the message.
- In pool mode, the inbound dispatcher never hands a message to the default agent.
- The flow engine marks a session interactive before binding it and clears the mark after unbinding, so a step's first reply is not refused.
- Daemon and per-Job pods are unchanged.

## Capabilities

### Modified Capabilities

- `bridge-http-api`: pool-mode ownership gate on `POST /router/inbound`.

## Impact

- `cmd/serve.go`, `internal/bridge/service/` (`service.go`, `http_inbound.go`, `inbound.go`), `internal/flow/service.go`.
- Orchestrators that retry every non-2xx should treat `sessionNotOwned` as permanent.
