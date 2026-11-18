## Context

Providers currently retry a stalled stream with a fixed request. The agent owns message history and inbox acknowledgements, so only it can add pending input safely.

## Goals / Non-Goals

Goals: cap stalled-call resends and preserve queued input across failed runs.
Non-goals: change HTTP retry policies, inactivity timeout, step retries, queue ownership, or interrupt calls for new input.

## Decisions

Return stalls immediately from the three provider streaming loops. Use a two-resend constant in the agent loop, sharing the budget across overflow and peer-reset recovery for the same turn. Reset the budget after a successful loop iteration. This prevents other recovery paths from multiplying stalled sends.

Discard partial assistant output before resending using the existing reset cleanup. Tools have not run on a stream error; failed cleanup closes the message and pairs tool calls with error results. Drain through the existing inbox hook only after the stream ends. Keep drained input in the history tail across a recovery compaction. At exhaustion, return a descriptive error rather than compacting merely because the stalled request is near its context window. The max-turns wrap-up retains its existing graceful fallback instead: return the earlier structured output and `TurnsExhausted` so the flow exhaustion policy still applies.

Drain pending input before the first model call of a run, including a run on an existing session. Failed-turn input that has not reached a drain stays unacknowledged in the existing inbox. Existing queue capacity and persistence-failure redelivery semantics remain unchanged.

## Risks / Trade-offs

- Partial output may already have reached a streaming preview; persisted history excludes discarded attempts, matching peer-reset recovery.
- Existing HTTP retry gates differ: some allow attempts 1 through 8 and fail attempt 9, whereas stream retry branches use a strict less-than-eight bound. Tests preserve these policies rather than normalizing them.
- Inbox storage is process-local; process replacement still relies on the sender's unacknowledged-message delivery mechanism.

Each recap, synchronous compaction and manual summarization request has an independent two-resend stall budget. It neither spends nor resets the main iteration budget and discards partial response data. Clear run-entry inbox and synthetic-tail preservation state alongside `hasUserTurn` when advancing the outer cycle, so consumed input cannot be replayed after later compaction.

Successful inner iterations retire consumed run-entry input. The latest tool/result tail remains preserved through successive threshold and overflow compactions until another iteration succeeds. Every failed wrap-up closes partial tool calls, including cancellation and mixed stall/reset failures, without changing graceful structured-output fallback.
