## Purpose

Delta spec for the `bridge-http-api` capability. Amends the `POST /router/inbound` 429
response to carry a `Retry-After` header and machine-readable saturation body so an
orchestrator mediator can back off intelligently rather than guessing. The existing
single-retry policy of the c2-agent orchestrator makes a blind 429 a real message-loss
risk when the channel is transiently saturated.

## MODIFIED Requirements

### Requirement: POST /router/inbound 429 carries Retry-After and saturation body

When `POST /router/inbound` returns `429 Too Many Requests` because the shared inbound
channel is full (`default:` branch in the non-blocking select), the response SHALL include:

1. A `Retry-After` header with an integer value (seconds) indicating the minimum wait
   before retrying. The value SHALL be derived from the expected drain rate; a value of
   `1` (one second) is a safe conservative default for v1.
2. A JSON response body of the form:
   ```json
   {
     "error": "inbound dispatcher full",
     "retryAfterSeconds": <N>,
     "dispatcherSaturated": true
   }
   ```
   The `dispatcherSaturated: true` field is a stable machine-readable signal that allows
   mediators to distinguish a capacity-related 429 from a rate-limiting 429 that might
   originate from other middleware.

The existing `"inbound dispatcher full; retry"` string is replaced by the structured body
above. Callers that parse only the status code are unaffected (they still receive 429).

#### Scenario: Channel full returns enriched 429

- **GIVEN** the shared `inboundCh` (cap 64) is full when `POST /router/inbound` arrives
- **WHEN** the non-blocking select takes the `default:` branch
- **THEN** the response is `429 Too Many Requests` with:
  - `Retry-After: 1` (or the computed value) in the response header
  - JSON body `{"error":"inbound dispatcher full","retryAfterSeconds":1,"dispatcherSaturated":true}`

#### Scenario: Normal enqueue returns 202 Accepted unchanged

- **GIVEN** the shared `inboundCh` has capacity
- **WHEN** `POST /router/inbound` arrives with a valid body
- **THEN** the response is `202 Accepted` with `{"ok":true}`; no behavior change

#### Scenario: Mediator observes dispatcherSaturated to distinguish from rate-limit

- **GIVEN** an orchestrator mediator receives a 429 from `POST /router/inbound`
- **WHEN** the mediator inspects `dispatcherSaturated` in the response body
- **THEN** `true` indicates a capacity constraint (retry after the header-specified delay);
  absent or `false` would indicate a different 429 origin (distinguishing future cases)
