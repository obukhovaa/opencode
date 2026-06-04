# Message-Part SSE Events — Streaming Tool Updates

**Date**: 2026-06-02
**Status**: Implemented
**Author**: AI-assisted

## Problem

The openwork bridge (`apps/opencode-router/src/bridge.ts`) listens for `message.part.updated` SSE events to publish live `[tool] <label> <status>: <title>` messages into Telegram/Slack/Mattermost during an agent run. dax emits these incrementally (`Session.Event.PartUpdated` at status transitions `pending → running → completed|error`); our fork does not.

`internal/api/handler_event.go:streamLoop` only fans in four brokers: `Messages`, `Sessions`, `Permissions`, `Questions`. The SSE wire types it emits are `message.{created,updated,deleted}`, `session.{...}`, `permission.asked`, `question.asked`. There is no `message.part.*` frame on the wire — the spec doc that introduced the API server (`20260517T120000-server-api-and-acp.md:545`) flagged this as a known gap.

Consequence: the bridge's SSE tool handler never fires against our fork. Tool activity only becomes observable post-completion via the final `session.prompt` response shape. For tool-only assistant turns (model used a tool, returned no follow-up text), the bridge user sees nothing during a multi-second tool execution.

The provider is incidental — this is missing for every provider, openrouter just made it visible because openrouter-routed completions are more likely to end on a `tool-calls` finish reason without a wrap-up text.

## Goals

1. Emit `message.part.updated` SSE frames at the three tool-lifecycle transitions (`pending`, `running`, `completed`/`error`) that dax emits, so the openwork bridge's existing handler works against the fork unchanged.
2. Match the dax wire shape exactly — payload is `{ part: APIPart }` where `APIPart` is our existing converter output (`internal/api/types.go:74`). No new types invented.
3. **Zero overhead when no SSE client is connected** (TUI-only / CLI-only runs). The current opencode binary is overwhelmingly invoked as a TUI; it would be unacceptable to pay any non-trivial cost on every tool transition just to support a wire format nobody is listening to.

## Non-Goals

- **Text/reasoning deltas (`message.part.delta`).** The bridge does not need them, and emitting per-token deltas has different perf characteristics — separate spec when there's a consumer.
- **Persistence changes.** The full message is already persisted via `messages.Update` / `messages.Create` on the same path. Part events are emit-only; nothing reads them back.
- **A rule engine for filtering events per subscriber.** All SSE subscribers see all part events for sessions they observe.
- **Refactoring of the multi-site tool-result write paths in `agent.go`.** We instrument the existing sites; we do not consolidate them in this spec.

## Design

### Wire format

The SSE frame matches dax:

```
event: message.part.updated
data: {"type":"message.part.updated","properties":{"part":{<APIPart>}}}
```

`APIPart` is already defined (`internal/api/types.go:74`) and already produced by `convertParts` for tool calls — including the merged `state.status/input/output/error/metadata` and `callID`. We reuse the same converter for the per-event payload; the diff is just *when* we emit it (mid-stream instead of only on a full-message poll).

### Where the lifecycle transitions live

Three transitions, three publish sites:

| Status | Source event / site | Code today |
|---|---|---|
| `pending` | Provider emits `EventToolUseStart` → `assistantMsg.AddToolCall(*event.ToolCall)` | `agent.go:1143` |
| `running` | Provider emits `EventToolUseStop` → `assistantMsg.FinishToolCall(...)` | `agent.go:1155` |
| `completed` / `error` | Goroutine writes `toolResults[entry.index] = message.ToolResult{...}` after `tool.Run` returns | 14 call sites in `streamAndHandleEvents` (lines 726, 742, 830, 845, 866, 904, 918, 928, 947, 956, 998, 1006, 1023, 1045) |

For `pending` and `running`, the call site already calls `a.messages.Update(ctx, *assistantMsg)`. Adding a part publish there is a one-liner: pick out the freshly-added/finished ToolCall by ID and hand it to a publish helper.

For `completed`/`error`, the goroutines fill `toolResults` but do **not** call `messages.Update` per-tool — a single tool-result message is created at the end (`agent.go:1062`). We need to publish the per-tool transition at each site that writes a slot. Concretely: a small helper `recordToolResult(entry, toolResult, isError)` that (a) writes the slot and (b) emits the part event. The existing 14 sites are reduced to calls into the helper.

**Concurrency invariant.** Three of the 14 sites (lines 830, 845, 866) run inside the per-tool goroutine spawned at `agent.go:776`. Those goroutines write to a shared `toolResults` slice — safe today because each owns a unique `entry.index`. The helper preserves this invariant: callers MUST pass the index they own; double-recording or cross-index writes are forbidden. Broker.Publish itself is safe under concurrent callers (RWMutex allows concurrent readers; non-blocking sends), so no new synchronization is required. Spec note rather than code: keep the comment block at `streamAndHandleEvents` describing the per-index ownership rule.

### Service-level surface

Add to `internal/message/message.go`:

```go
// In message/content.go or a new message/part_event.go
type PartEvent struct {
    SessionID string
    MessageID string
    Part      ContentPart
    Time      int64 // unix millis
}
```

```go
// In message/message.go service
type service struct {
    *pubsub.Broker[Message]
    parts *pubsub.Broker[PartEvent]
    db    *sql.DB
    q     db.QuerierWithTx
}

func NewService(q db.QuerierWithTx, database *sql.DB) Service {
    return &service{
        Broker: pubsub.NewBroker[Message](),
        parts:  pubsub.NewBroker[PartEvent](),
        q:      q,
        db:     database,
    }
}

// Extend the Service interface
type Service interface {
    pubsub.Suscriber[Message]
    SubscribeParts(ctx context.Context) <-chan pubsub.Event[PartEvent]
    PartSubscriberCount() int
    PublishPart(sessionID, messageID string, part ContentPart)
    // ... existing methods
}

func (s *service) SubscribeParts(ctx context.Context) <-chan pubsub.Event[PartEvent] {
    return s.parts.Subscribe(ctx)
}

func (s *service) PartSubscriberCount() int {
    return s.parts.GetSubscriberCount()
}

func (s *service) PublishPart(sessionID, messageID string, part ContentPart) {
    if s.parts.GetSubscriberCount() == 0 {
        return
    }
    s.parts.Publish(pubsub.UpdatedEvent, PartEvent{
        SessionID: sessionID,
        MessageID: messageID,
        Part:      clonePart(part),
        Time:      time.Now().UnixMilli(),
    })
}
```

`clonePart` is a small helper that snapshots `ToolCall` / `ToolResult` so a subscriber reading later from its buffered channel can't observe in-flight mutation. In our fork both structs are pure value types — `ToolCall` is `{ID, Name, Input, Finished, Type string/bool}` and `ToolResult` is `{ToolCallID, Type, Name, Content, Metadata, IsError}` where `Metadata` is a plain JSON-encoded `string` (`internal/message/content.go:108`), not a map. So `clonePart` is bare struct assignment: `return part` after a type switch is sufficient. No reference fields to deep-copy.

### The performance guarantee

This is the part the request specifically calls out. Two cost models matter:

**1. CLI / TUI runs (no SSE subscribers).** This is the dominant case. Cost per call to `PublishPart`:
- One `RLock`/`RUnlock` pair inside `GetSubscriberCount()` (returns the cached `subCount` int, no map iteration).
- Compare-with-zero, return.
- **No deep copy. No allocation. No map iteration. No channel send.**

The `Broker[T]` struct already tracks `subCount` (`pubsub/broker.go:14`), so the check is O(1). Cost: tens of nanoseconds per tool transition, called O(N) times per turn where N is tool-call count (typically 1–20). Negligible.

Compare to the alternative of "always publish" — then `Broker.Publish` does an `RLock`, a `<-b.done` check, and ranges over an empty `subs` map; still cheap but pays for an event-struct allocation and the `clonePart` cost. The explicit `GetSubscriberCount() == 0` short-circuit is strictly cheaper.

**2. Server runs with one or more SSE subscribers.** We pay:
- One `clonePart` per transition: a struct copy plus, for `ToolResult`, a `make(map, len(m))` + range. Sub-microsecond.
- One non-blocking channel send per subscriber. If the subscriber's buffer (64 by default) is full, the event is silently dropped — matches existing `Broker.Publish` semantics. SSE clients can reconcile via the periodic `message.updated` frame that still fires alongside.

There is no new lock contention path: the part broker is independent of the message broker's mutex, so emitting both per turn does not serialize.

### Wiring the SSE stream

In `internal/api/handler_event.go:streamEvents`:

```go
msgCh := s.app.Messages.Subscribe(ctx)
sesCh := s.app.Sessions.Subscribe(ctx)
permCh := s.app.Permissions.Subscribe(ctx)
partCh := s.app.Messages.SubscribeParts(ctx)  // new

var questionCh <-chan pubsub.Event[question.Request]
if s.app.Questions != nil {
    questionCh = s.app.Questions.Subscribe(ctx)
}

streamLoop(ctx, w, flusher, msgCh, sesCh, permCh, questionCh, partCh)
```

Add one `case` to the fan-in select in `streamLoop`:

```go
case event, ok := <-partCh:
    if !ok {
        return
    }
    apiPart := ConvertPart(event.Payload.SessionID, event.Payload.MessageID, event.Payload.Part)
    props := map[string]any{"part": apiPart}
    if err := writeSSEEvent(w, flusher, "message.part.updated", props); err != nil {
        return
    }
```

`ConvertPart` is a thin extraction of the existing per-part switch inside `convertParts` (`internal/api/convert_message.go:84`). For `ToolCall` parts, it produces the `tool` shape with `state.status` derived from `Finished` (no result-map lookup — the merged "completed"/"error" status comes through on the subsequent emit when the result is recorded; see "Status derivation" below).

### Status derivation

The dax wire requires `state.status ∈ {pending, running, completed, error}`. Our internal parts express this implicitly:

- `ToolCall{Finished: false}` → `pending` (emitted on `EventToolUseStart`)
- `ToolCall{Finished: true}` with no result yet → `running` (emitted on `EventToolUseStop`)
- `ToolResult{IsError: false}` → `completed` (emitted on tool execution success)
- `ToolResult{IsError: true}` → `error` (emitted on tool execution failure or permission denied)

The existing `resolveToolStatus` and `convertStandaloneToolResult` (`convert_message.go:208,183`) already encode this mapping. We extract a helper `convertPartForEvent(messageID, sessionID string, part ContentPart) APIPart` that:

- For `ToolCall`, emits a tool APIPart with input parsed and status from `Finished`.
- For `ToolResult`, emits a tool APIPart with the result content + completed/error status. The bridge's dedup keys on `(callID, status)`, so the assistant-side `running` event and the result-side `completed` event reconcile into a coherent stream.

### Publish sites in `agent.go`

Three concrete edits:

**1. `processEvent` — pending and running** (`agent.go:1128`):

```go
case provider.EventToolUseStart:
    assistantMsg.AddToolCall(*event.ToolCall)
    a.messages.PublishPart(sessionID, assistantMsg.ID, *event.ToolCall) // pending
    return a.messages.Update(ctx, *assistantMsg)

case provider.EventToolUseStop:
    assistantMsg.FinishToolCall(event.ToolCall.ID)
    if tc, ok := assistantMsg.FindToolCall(event.ToolCall.ID); ok {
        a.messages.PublishPart(sessionID, assistantMsg.ID, tc) // running
    }
    return a.messages.Update(ctx, *assistantMsg)
```

`FindToolCall(id)` is a small new accessor on `Message` (lookup by `ToolCallID`). Returns the current ToolCall by value so the publish copies cleanly.

**2. Tool-result recording — completed/error.** Introduce a helper that consolidates the 14 sites:

```go
func (a *agent) recordToolResult(
    sessionID, messageID string,
    index int,
    results []message.ToolResult,
    tr message.ToolResult,
) {
    results[index] = tr
    a.messages.PublishPart(sessionID, messageID, tr)
}
```

Each of the 14 sites changes from:

```go
toolResults[entry.index] = message.ToolResult{...}
```

to:

```go
a.recordToolResult(sessionID, assistantMsg.ID, entry.index, toolResults, message.ToolResult{...})
```

The sites that write to `toolResults[i]` instead of `toolResults[entry.index]` (loop-detection at line 742, missing-tool at 726, cancellation at 904) get the same treatment with their own index variable.

**3. Final `messages.Create` for the tool-result message** (`agent.go:1062`) remains unchanged. The bridge sees the per-tool `completed`/`error` events live; the consolidated tool-result message still hits `message.created` for any consumer that wants the aggregated form.

### Buffer sizing and drop behavior

Subscribers get a buffered channel of `bufferSize = 64` (package-level const at `pubsub/broker.go:8`). One turn with 10 parallel tools × 3 transitions = 30 events. A slow SSE client could overflow if multiple turns happen rapidly. Drops are silent but recoverable: the openwork bridge's post-prompt fallback (`apps/opencode-router/src/bridge.ts`) reads tool parts off the final `session.prompt` response, so a dropped mid-stream event manifests only as missing intermediate visibility (no `pending`/`running` lines), not as a missing tool altogether.

**Important — `NewBrokerWithOptions` is currently dead.** The signature accepts `channelBufferSize` and `maxEvents`, but `Subscribe` ignores the constructor argument and always allocates `make(chan Event[T], bufferSize)` using the package-level const (`pubsub/broker.go:63`). `maxEvents` is stored on the struct and never read. So a recommendation to "bump the buffer to 256" has no effect today. If we want a larger buffer for the parts broker only, the prerequisite is fixing `Broker` to store the buffer size as a struct field and consult it in `Subscribe`. Scope this as a small precursor change inside `pubsub/broker.go` (1-line struct field + 1-line read in `Subscribe`), not part of the parts-broker work itself. The fix is trivial; calling it out so a future reader doesn't ship the dead-code form thinking it works.

Recommend the precursor fix + 256-byte buffer on the parts broker — the cost is bounded by subscriber count, which in practice is 1 (the openwork bridge).

## Implementation Plan

- [ ] Add `PartEvent` type to `internal/message/content.go`.
- [ ] Add `clonePart(ContentPart) ContentPart` helper in `internal/message/content.go`.
- [ ] Add `parts *pubsub.Broker[PartEvent]` field to `message.service`, initialise in `NewService`.
- [ ] Extend `message.Service` interface with `SubscribeParts`, `PartSubscriberCount`, `PublishPart`.
- [ ] Add `Message.FindToolCall(id string) (ToolCall, bool)` accessor.
- [ ] Extract `ConvertPart(messageID, sessionID, part) APIPart` from `convertParts` in `internal/api/convert_message.go`.
- [ ] Wire `partCh` into `streamEvents` and `streamLoop` (`internal/api/handler_event.go`).
- [ ] Emit `pending` event in `processEvent` `EventToolUseStart` branch.
- [ ] Emit `running` event in `processEvent` `EventToolUseStop` branch.
- [ ] Introduce `recordToolResult` helper, refactor the 14 result-write sites to use it.
- [ ] **Precursor**: fix `pubsub.Broker` to honor the constructor's `channelBufferSize` argument (store as struct field, read in `Subscribe`). Currently dead.
- [ ] Construct the parts broker with `NewBrokerWithOptions[PartEvent](256, 1000)` once the above lands.
- [ ] Add shutdown wiring. There is no existing message-broker shutdown today — `app.Shutdown()` (`internal/app/app.go:259`) only stops `CronScheduler` and `LspService`; the `Messages` broker is leaked at process exit. Add `app.Messages.Shutdown()` (which shuts down both the message broker and the new parts broker) to the same place. Pre-existing leak; this is the right time to fix it.

## Testing

- Unit test in `internal/message`: subscribe, publish a tool call, assert the part event arrives with deep-copied metadata (mutating the original after publish doesn't affect the subscriber's view).
- Unit test for the `GetSubscriberCount() == 0` fast path: subscriber count is zero, `PublishPart` returns without invoking `clonePart`. Use a `clonePart` wrapper with an atomic counter in the test, assert it stays at zero.
- Integration test (`test/server/`): start the API server, open `/event` SSE stream, run a session that invokes the `bash` tool, assert the SSE stream contains `message.part.updated` frames with `state.status` transitioning `pending → running → completed`.
- Existing CLI / TUI tests should not see any behavioural difference. Benchmark `PublishPart` with zero subscribers to confirm <100ns/op on the dev machine.

## Risks

| Risk | Mitigation |
|---|---|
| Hidden mutation aliasing — subscriber reads partially-mutated `ToolCall` from buffered channel | `clonePart` before publish; subscribers see frozen snapshots. |
| Per-turn allocation overhead on busy servers | Gate on `subCount == 0` (CLI cost: ~zero) and accept the allocation when subscribers exist; subscribers are typically just the bridge. |
| New goroutine leak if `parts.Shutdown()` is missed | Wire shutdown in the same place the message broker shuts down today. |
| Wire-format drift from dax | We reuse `APIPart`/`APIToolState` which are already aligned (see `20260601T152855-api-openrouter-compat-and-parity.md`). The frame envelope `{type, properties:{part}}` matches the existing `writeSSEEvent` shape. |
| Slow SSE clients drop events | Bump buffer to 256; the openwork bridge has a documented post-prompt fallback that backfills tool parts the SSE stream missed. Drops degrade visibility, not correctness. |

## Out-of-band consideration — the openwork side

The bridge fix landed in `apps/opencode-router/src/bridge.ts` (post-prompt iteration over tool parts with `seenToolStates` dedup) is **complementary** to this spec, not redundant:

- Before this change: the bridge's only path to see tool activity is the post-prompt response. User waits the full turn duration before seeing any `[tool]` line.
- After this change: the bridge's SSE path lights up. `pending`/`running`/`completed` lines stream live. The post-prompt fallback becomes a safety net for dropped events.

Keep both. The bridge dedupes by `(callID, status)` so duplicate emission from both paths is a no-op.
