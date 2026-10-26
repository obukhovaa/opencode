## Context
An external widget can retain an original question request across reconnect. A label-only inbound can answer a replacement question, while raw question-service HTTP replies leave router pending state behind.

## Goals / Non-Goals
Provide a synchronous exact-request reply owned by the bridge router. Preserve stale-click suppression and stop future nudges after a successful answer. General question cancellation synchronization and persisted chat replay are separate work.

## Decisions
1. Register `POST /router/questions/{requestID}/reply` beside the existing router routes, behind the API server's existing Basic authentication. Require `{sessionId, answers}` and non-empty answer selections.
2. Under the router mutex, validate the authoritative question-service request/session and answer count, call its in-process Reply, delete the matching router pending entry and remember answered labels before returning 200. Refuse a different current router request and any stale/missing/cross-session service request with 409; never enqueue inbound.
3. Support a live service request whose Created event has not reached the router yet. Fan-out checks that request is still live under the same mutex, preventing a delayed event from resurrecting it.
4. Keep the raw question API for its existing clients. The external repair uses only the router operation; old runners return 404, and callers must not fall back.

## Risks / Trade-offs
- **[Older runner has no route]** → **Mitigated**: fail closed without generic or raw-service fallback; deploy the engine before the producer/consumer chain.
- **[Concurrent or stale answer]** → **Mitigated**: exact request/session checks plus the question service's atomic LoadAndDelete permit only one successful answer; a newer router request is never removed.
- **[Answer before fan-out]** → **Mitigated**: the router refuses a delayed Created event after service settlement.
- **[Nudge already being sent]** → **Accepted**: an in-flight reminder can finish; future sweeps find no pending request after a successful reply.
- **[General cancellation synchronization]** → **Accepted**: this route synchronizes successful selected replies only. Other clients retain existing behavior, and C3 cancellation UI is outside this repair.

## Migration Plan
No migration. Review/release the engine, deploy it to relevant runners, then c2-agent !232, then evolution !781 after migration037/!763. No live run is answered or restarted by tests.
