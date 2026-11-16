# Design

## Context

The engine already persists a 1-based FlowState.Iteration and sends it in step events. The HTTP status record omits it; the driver therefore reconstructs iteration zero after a lost stream. Session IDs are reused for self-loops, and event versus snapshot timestamps have different sources and precision.

## Goals / Non-Goals

**Goals:** carry the existing positive iteration through current/completed status records and driver recovery. Preserve the legacy zero/absent contract as unknown.

**Non-Goals:** a new execution-ID generator, flow lifecycle changes, model notifications, Preview delivery, deployment or approval changes. No assertion that iteration alone is globally unique: consumers must bind it to the correct run/job and step session. A new run can reuse iteration numbers.

## Decisions

- Add an optional integer `iteration` to the producer status record, copied directly from FlowState. A retrying notification does not replace the active record.
- Decode it in the driver. Snapshot recovery fills unknown iteration; when positive iterations disagree for the same step, the newer execution wins over timestamps. Do not carry a previous execution's streamed totals or waiting state into a newer snapshot execution.
- A legacy snapshot with absent/zero iteration follows existing recovery behavior and cannot erase a known positive iteration. A snapshot of another run remains refused.
- Use tests for cold reconstruction, repeated reads, retrying state, later executions within the same timestamp, stale snapshots and backward compatibility. No timestamp-derived identifier.
- Arne approved the step-iteration contract via CoS on 2026-11-16. The producer goes to the arnesund fork and an upstream PR for Artem; the consumer goes to a paired c2-agent MR. Consumers bind the counter to their run/job and step session. Deployment remains separate.

## Risks / Trade-offs

- Older producers have unknown identity; a consumer requiring reliable deduplication must fail closed until compatible producers are deployed.
- The status response still describes one current step, not all parallel active steps. This draft does not solve that separate completeness boundary or authorise treating an absent Preview as ready.
- Adding this field is a prerequisite, not proof of end-to-end exactly-once Preview delivery. Evolution still needs an atomic receipt/chat transaction and a binding to its current run/execution.
