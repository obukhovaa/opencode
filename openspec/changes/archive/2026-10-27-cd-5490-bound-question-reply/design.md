## Context
An external widget can retain an original question request across reconnect. A label-only inbound can answer a replacement question, while raw question-service HTTP replies leave router pending state behind.

## Goals / Non-Goals
Provide a synchronous exact-request reply owned by the bridge router. Preserve stale-click suppression and stop future nudges after a successful answer. General question cancellation synchronization and persisted chat replay are separate work.

## Decisions
1. Register `POST /router/questions/{requestID}/reply` beside the existing router routes, behind the API server's existing Basic authentication. Require sessionId and exactly one of answers or text. Text is the existing single-prompt bridge codec, parsed against the authoritative request in the runner. Buttons send their option's label, so the codec matches an exact label before an index, and keeps a Multiple reply whole when the whole text is one label; this also fixes numeric labels on the chat path. Answer counts, multiplicity and custom permission are validated, and option labels canonicalized.
2. Under the router mutex, validate the authoritative question-service request/session and answer count, call its in-process Reply, delete the matching router pending entry and remember answered labels before returning 200. Refuse a different current router request and any stale/missing/cross-session service request with 409; never enqueue inbound.
3. Support a live service request whose Created event has not reached the router yet. Fan-out checks that request is still live under the same mutex. If a bound reply wins inside buffered auto-answer, the failed buffered Reply re-checks liveness before restoring pending state and requeues the original input at the head of the inbox as follow-up, so later buffered messages keep their order; a ClearSession guard prevents restoring input to an ended step.
4. Queued input carrying ids retains cd-5480 D3 priority even before fan-out: close the live question as unanswered and refuse the selected reply; keep the queued input.
5. Receipts last five minutes and are capped at 256, evicting the oldest. Sweeps also expire them. Return `{ok:true, alreadyAnswered:true}` for an identical replay, and false for a first success. Refusals carry stable `code`; malformed shape or invalid values are 400, lifecycle/session conflicts are 409. C2 sends one bounded POST with its original text; it must not preflight GET /question on a retry after settlement.
6. Keep the raw question API for its existing clients. The external repair uses only the router operation; old runners return 404, and callers must not fall back.

## Risks / Trade-offs
- **[Older runner has no route]** → **Mitigated**: fail closed without generic or raw-service fallback; deploy the engine before the producer/consumer chain.
- **[Concurrent or stale answer]** → **Mitigated**: exact request/session checks plus the question service's atomic LoadAndDelete permit only one successful answer; a newer router request is never removed.
- **[Answer before fan-out]** → **Mitigated**: the router refuses a delayed Created event after service settlement.
- **[Nudge already being sent]** → **Accepted**: an in-flight reminder can finish; future sweeps find no pending request after a successful reply.
- **[Receipt expiry, eviction, process loss or removed binding]** → **Accepted**: acknowledgements are bounded and in-process. Outside that window delivery can remain uncertain; inspect run state. Never retry as generic inbound, and never execute an old request again.
- **[Fan-out already past its live check]** → **Accepted**: an in-flight widget can finish posting after a competing answer, but no pending state is restored and no later nudge or duplicate answer occurs.
- **[General cancellation synchronization]** → **Accepted**: this route synchronizes successful selected replies only. Other clients retain existing behavior, and C3 cancellation UI is outside this repair.

## Migration Plan
No migration. Review/release the engine, deploy it to relevant runners, then c2-agent !232, then evolution !781 after migration 037/!763. No live run is answered or restarted by tests.
