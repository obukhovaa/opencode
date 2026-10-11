## 1. Structured progress

- [x] 1.1 `bridge.RunProgress` and `bridge.ProgressRelayer`
- [x] 1.2 `runProgress` builds a `RunProgress` per flush, keeping the last failure's tool and reason apart
- [x] 1.3 `deliverProgress` relays it to non-editing `ProgressRelayer` peers, stamped with the session id; failure lines unchanged

## 2. External relay

- [x] 2.1 `progress` relay kind and `relayProgress` sub-object, every key always present
- [x] 2.2 `Adapter.SendProgress`, recorded as a failed send on a non-202

## 3. Tests

- [x] 3.1 Dispatcher: relay receives running → in-flight → one terminal state with the session id; editing peer unchanged; failure line still sent
- [x] 3.2 Adapter: frame shape, non-202 error, disabled adapter
