## 1. Structured progress

- [x] 1.1 `bridge.RunProgress` and `bridge.ProgressRelayer`
- [x] 1.2 `runProgress` builds a `RunProgress` per flush, keeping the last failure's tool and reason apart
- [x] 1.3 `deliverProgress` relays it to non-editing `ProgressRelayer` peers, stamped with the session id
- [x] 1.4 A relayer that accepted the update gets no failure lines for it; a rejected update falls back to them

## 2. External relay

- [x] 2.1 `progress` relay kind and `relayProgress` sub-object, every key always present
- [x] 2.2 `Adapter.SendProgress`, recorded as a failed send on a non-202

## 3. Tests

- [x] 3.1 Dispatcher: relay receives running → in-flight → one terminal state with the session id; editing peer unchanged; no failure line
- [x] 3.2 Rejected progress falls back to the failure line
- [x] 3.3 Adapter: frame shape, non-202 error, disabled adapter, implements `ProgressRelayer` and not `MessageEditor`
- [x] 3.4 Coalesced failures: an accepting relay gets the count and the last failure, a text-only peer both lines; a terminal state names nothing in flight
