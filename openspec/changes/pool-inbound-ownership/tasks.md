## 1. Implementation

- [x] 1.1 Pass `--pool-mode` into the bridge service as `PoolMode`.
- [x] 1.2 `handleInbound`: pool-mode ownership check, `409 sessionNotOwned` when unowned.
- [x] 1.3 `dispatchInbound`: in pool mode, never fall through to the default-agent dispatcher.
- [x] 1.4 Flow engine: mark the session before bind, unmark after unbind or on bind failure.

## 2. Tests

- [x] 2.1 `/router/inbound`: 409 with no binding, 409 with an unmarked binding, 202 when owned, 202 in non-pool mode.
- [x] 2.2 Dispatch in pool mode never runs the default agent for an unowned session.
- [x] 2.3 Marker is set inside both bind hooks and cleared after a failed bind.
