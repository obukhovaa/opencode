## Why
CD-5490 recovers C3 approval questions after missed delivery. The orchestrator must answer the original request, but posting to the raw question-service reply endpoint bypasses the bridge router's pending/nudge state and stale-click cache.

## What Changes
- Add an authenticated router reply route carrying an exact request ID and expected session ID.
- Settle the question through the in-process service while clearing router pending state and remembering answered labels.
- Refuse a stale/cross-session reply without generic inbound or a new agent turn.
- Ignore a delayed Created event for a request already settled before fan-out.

## Capabilities
### New Capabilities
None.
### Modified Capabilities
- bridge-http-api: request-bound replies update the question router's state.
- chat-bridge: reply text prefers an exact option label over an index.

## Impact
Bridge service HTTP registration, router state transition, local tests and bridge documentation. No model/provider call, runtime configuration, migration, automatic answer, or deployment. c2-agent !232 will use this route; runners must carry it before C3 !781 rolls out.
