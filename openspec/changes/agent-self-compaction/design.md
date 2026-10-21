# Design: agent-self-compaction

## The request is the tool result

The request has to survive a restart and be served exactly once. Two places could hold it: a new column on the session row, or the message log.

The message log already does everything needed. The `compact_session` result is persisted with the turn, carrying `tools.CompactSessionRequest` in its metadata (`compaction_requested`, the token count, the `keep` text). A request is pending while it sits after the session's current summary. Any compaction writes its summary after it, and `filterMessagesFromSummary` drops everything before the summary, so the compaction that serves a request also consumes it. No migration (SQLite and MySQL), no new querier methods, no new `Session` field that every `Save` would have to carry, and no state to clear by hand on success or failure. A skipped or failed call has no such metadata and is never pending.

The cost is a scan of the post-summary history for `compact_session` results: one `List` at the end of a turn, only for an agent with `selfCompact.enabled`. The pre-turn gate scans the history it has already loaded.

## When it runs

`RunWith`'s goroutine sends the terminal event, then calls `compactAfterTurn`, then closes the channel; the deferred release of the session slot comes after. So the compaction runs:

- after `processGeneration` returned and its event was published and sent: never inside the turn;
- while the slot is still held: a concurrent `Run` gets `ErrSessionBusy`, `Summarize` and `SummarizeSync` refuse, the cron sentinel cannot take the slot, so nothing interleaves with the summary and nothing compacts the same history twice;
- before the channel closes: a caller draining it (the bridge dispatcher's per-session loop, the TUI queue) sees the session free only once the compaction is done, instead of racing it into `ErrSessionBusy`.

Releasing the slot first and re-acquiring it for the compaction was considered and rejected: the turn would be just as over, but another actor (cron, an auto-resume, the next inbound message) could win the slot in between, compact or extend the history first, and leave the request to be served twice or out of order. Keeping the slot with the run that owns the request is what makes "never twice" hold.

Every agent entry point (bridge inbound, heartbeat beats, cron notification turns, background-task auto-resume, TUI, flows) goes through `RunWith`. A turn that ended in an error or was cancelled skips the post-turn path; its request stays pending.

### Callers that stop listening

Not every caller drains the channel. The flow runner and the headless prompt command read the terminal event and stop; the flow runner cancels its step ctx right away. A compaction on the run's ctx would then be skipped or cancelled mid-summarize. So the post-turn compaction runs on the run's values but not its cancellation (`context.WithoutCancel`), bounded by `postTurnCompactionTimeout`. Only the agent's own `Cancel` ends it: `RunWith` gives the slot a cancel func that also ends a second, agent-only context, and the compaction stops on that one.

A flow step's run (its ctx carries the step scope) skips the post-turn path altogether. The flow runner reuses the session as soon as it has the terminal event: a retry, a struct_output re-prompt, the next step sharing the session. Those retry `ErrSessionBusy` only for microseconds, which a summarizer call held on the slot would outlast. The request stays pending and the next turn on the session serves it at the pre-turn gate.

A compaction cancelled through its ctx is not recorded as a failure in the backoff (any trigger): it says nothing about the summarizer, and recording it would refuse the pre-turn retry.

### Background completions during the compaction

A background task's completion pair is written without the run slot (`task.EnqueueTaskCompletion`), and its auto-resume is skipped while the session is busy, on the assumption that the in-flight run picks the pair up. A run held for its post-turn compaction is past its loop, so it does not, and the summary is written after the pair, so every later reload would drop it.

Two things cover this. `processGeneration` records the synthetic message IDs and completion call IDs actually sent to the model. After a successful post-turn compaction, `runSelfCompaction` re-creates each unobserved synthetic message from the previous summary onward after the new summary (a tool call and its result as one `CreatePair`, so another completion cannot split them) and deletes the originals. A completion can arrive during the final model call or after the terminal event but before `compactAfterTurn` reads its snapshot; neither a fresh snapshot nor the final response's sequence number proves the model consumed it. The pre-turn path uses its loaded history's sequence boundary to move messages that arrive during the compaction.

`EnqueueTaskCompletion` records a pending resume per session with the completion call ID, requester and job args (flow-owned tasks never resume). It publishes that record before checking whether the session is busy. The idle path and the releasing run both take the record atomically, so the release-versus-busy-check interleaving cannot strand a completion or resume it twice. `compactAfterTurn` keeps the record through the handoff and gives `RunWith` a function that takes it after the slot is released. It resumes only if the model did not already observe that completion.

### Delivery before compaction

The bridge posts the terminal reply from its dispatcher goroutine (`handleTerminalEvent`, which waits for the per-peer sends). The events channel is buffered, so without a handshake the summarizer call could start while the reply is still being posted. `WithTurnDelivery(ctx)` returns a context carrying a channel and a func that closes it once; the dispatcher wraps each run's context with it and calls the func after handling the terminal event (and on return, as a safety net). `compactAfterTurn` waits on it, bounded by `turnDeliveryWait` (2 minutes), only when a request is pending. Callers that do not mark the context (TUI, auto-resume) do not wait.

The bridge's progress card closes when `handleInbound` returns, so it stays open during the compaction.

### Fallback

At the start of `processGeneration`, before the auto-compaction gate and before the user message is persisted, a pending request is served the same way the pre-turn gate compacts: waiting for an in-flight async `Summarize` first, keeping an auto-resume turn's synthetic tail after the summary, and reloading the history from the new summary. It runs whether or not `autoCompact` is on: the agent asked for it. The auto gate then counts the compacted history, which anchors on the summary and does not compact again.

## Counting and the floor

The tool counts with `countContextTokens`, the function every auto-compaction decision uses: the last reported usage plus a local estimate of only what was appended since. The tool runs inside a turn after at least one model call of that turn, so a report covers the history. The counter is bound to the tool when the agent resolves its tool set (`BindCounter`, like `toolsearch`'s `BindToolset`), since `NewToolSet` runs before the agent exists. The floor is checked when the tool is called, not again when the compaction runs.

## Backoff

A pending request is retried at every turn end and turn start until a compaction succeeds, so a failing summarizer must not be called on every turn. Self-compaction therefore goes through `compactionAllowed` and `autoCompact` like the automatic gates: a failure is recorded in the per-session backoff and later attempts wait. This differs from `/compact`, which a human retries deliberately and which ignores the backoff.

## Keep notes

`summarizerInputWithStats` collects the keep notes of every pending request (in order, exact duplicates dropped) and appends them to the compaction prompt before the budget is computed; callers take the prompt from the returned slice. This applies to every compaction path, so an in-loop auto-compaction later in the same turn, or a `/compact`, carries the notes it consumes.

## Observability

For the two self triggers (`self_after_turn`, `self_pre_turn`) the summarizer trace is named `<agent>/self-compaction` (`createLangfuseTraceNamed` appends `/self-compaction` to the usual agent or agent/flow/step name and keeps it whole on truncation), and its input is `self-compaction requested by agent over <n> messages` when generation input is logged for the agent. The triggers also land in `compaction.trigger` on the generation metadata. An automatic compaction's trace keeps the agent's name. On a child session the span sets no trace name, as for every trace there. `runSelfCompaction` logs INFO lines with `tokens_before` and, after success, `tokens_after` (counted on the reloaded history, anchored on the summary).

## Registration

The tool is registered in `NewToolSet` when `selfCompact.enabled` is set, the agent is a primary agent, and `IsToolEnabled` allows `compact_session` (so `tools: {"compact_session": false}` or an allowlist without it turns it off, with a warning). It is never wrapped as a deferred tool.
