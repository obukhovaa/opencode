# Tasks: bridge-queue-visibility-and-loss-paths

## 1. Fix `ErrSessionBusy` — re-queue instead of discard

- [x] 1.1 In `internal/bridge/service/dispatch.go`'s `handleInbound`, replace the
  discard-and-reply path for `ErrSessionBusy` with a bounded retry loop (budget: 5 minutes,
  backoff: 100 ms per cycle, matching `internal/app/queue.go`'s `busyBackoff`). On each
  retry, re-call `ag.Run` with the original `in` value. When the budget expires WITHOUT a
  successful `ag.Run`, signal `run()` to re-queue `in` via the overflow slice (task 1.6)
  rather than discarding. The `in` value MUST never be lost due to a busy session.

- [x] 1.2 Remove the `ErrSessionBusy` arm from `runFailureMessage`
  (`dispatch.go:258-269`). The arm is now dead code. If `runFailureMessage` is still
  needed for non-busy errors, retain the generic branch only; delete the specific busy text.

- [x] 1.3 Update `TestRunFailureMessage_BusyDoesNotAdviseAbort`
  (`internal/bridge/service/dispatch_test.go:60-74`). This test currently pins the lossy
  wording ("resend"). After 1.1-1.2 the `ErrSessionBusy` case no longer reaches
  `runFailureMessage`. Update the test to assert the re-queue / retry behavior: verify that
  when `agent.Run` returns `ErrSessionBusy` once and then succeeds, the inbound content
  is delivered (not discarded). Do NOT simply delete the test — convert it to cover the
  retry invariant.

- [x] 1.4 Add a new test `TestHandleInbound_BusyRetryPreservesContent` in
  `dispatch_test.go` or a new `dispatch_busy_retry_test.go`: given a mock agent that
  returns `ErrSessionBusy` N times then succeeds, assert (a) `agent.Run` is called N+1
  times with the same content, (b) the run-failure reply path is NOT taken,
  (c) the inbound text is intact when the final Run call succeeds.

- [x] 1.6 Implement non-blocking push with per-session overflow to fix cross-session
  starvation:

  a. Add `overflow []bridge.Inbound` slice and protecting mutex to `sessionDispatch`
     (`dispatch.go`).

  b. In `dispatchInbound` (`inbound.go`), replace the blocking `disp.pushInbound(ctx, in)`
     call with a non-blocking attempt: try a non-blocking send to `d.inbound`; if the
     channel is full, append to `d.overflow` instead. `runInboundLoop` MUST NOT block
     waiting for any per-session slot.

  c. In `run()` (`dispatch.go:124-138`), after `d.handleInbound` returns, drain
     `d.overflow` into `d.inbound` (FIFO) before the next iteration reads a new message
     from `d.inbound`. Overflow items are served before new channel reads.

  d. In `sessionDispatch.close()` (`dispatch.go:897-902`), drain both `d.overflow` and
     the unread items in `d.inbound` for shutdown-loss logging (task 4.1) BEFORE calling
     `close(d.inbound)`.

  e. Add a test `TestDispatch_NonBlockingPush_NoStarvation`: construct two session
     dispatchers; fill session A's `d.inbound` to cap; push one message for session A and
     one for session B via the push path; assert session B's message is accepted
     immediately (no block) and that session A's overflow slice holds the message.

- [x] 1.7 Add a test `TestSessionSerializationInvariant` that verifies the dispatcher
  processes a second inbound message only after the first `agent.Run` returns: use a
  slow mock that returns after a controlled delay; assert delivery order is strictly FIFO
  and no concurrent Runs are observed. This pins the serialization behavior currently
  untested.

## 2. No-active-agent drop: make it audible

- [x] 2.1 In `handleInbound` (`dispatch.go:193-196`), after logging the warn, call
  `d.svc.replyToPeer` (passing `in.Peer`) with a brief message such as "bridge: this
  session has no active agent — your message could not be processed. Please try again
  once the agent is available." Do NOT retry in this branch; the condition requires
  operator intervention.

- [x] 2.2 Add a test `TestHandleInbound_NilAgentReplies`: a dispatcher with a nil
  `ActiveAgent()` receives an inbound; assert a reply is sent to the sender and no panic
  occurs.

## 3. Interactive-flow buffer overflow: make it audible

- [x] 3.1 In `QuestionRouter.BufferInbound` (`question.go:472-481`): when the buffer is
  full and the oldest message is evicted, capture `evicted.Peer` and call
  `r.svc.replyToPeer` with a message such as "bridge: your earlier message was lost
  because too many messages were buffered while the interactive step had no pending
  question. Please resend it once the current step completes."

- [x] 3.2 Add a test `TestBufferInbound_DropNotifiesEvictedPeer`: fill the buffer to cap,
  push one more; assert the evicted peer receives a reply and the buffer still has cap
  elements with the newest message at the tail.

## 4. Shutdown loss: log at WARN

- [x] 4.1 In `sessionDispatch.close()` (`dispatch.go:897-902`), BEFORE calling
  `close(d.inbound)`, drain the channel into a local slice. Also drain `d.overflow`.
  For each item collected, log at WARN:
  `"bridge: shutdown lost queued inbound", "session", d.sessionID, "peer", item.Peer.PeerID`.
  Emit a per-session summary: `"bridge: shutdown dropped N queued messages", "session", d.sessionID`.
  Draining must be protected against concurrent `run()` reads — acquire the overflow mutex
  before draining, and only drain `d.inbound` items that remain after `d.stop.Store(true)`
  (i.e., items the `run()` goroutine can no longer read).

- [x] 4.2 Add a test `TestShutdown_WarnsOnQueuedMessages`: construct a dispatcher with N
  messages in its inbound channel; call `close()`; assert N WARN log entries containing
  the session ID and peer ID are emitted. No assertion on in-chat replies (they are not
  attempted).

## 5. POST /router/inbound 429 enrichment

- [x] 5.1 In `internal/bridge/service/http_inbound.go`'s `handleInbound` HTTP handler,
  replace the bare `writeAPIError(w, http.StatusTooManyRequests, "inbound dispatcher full; retry")`
  with:
  ```go
  w.Header().Set("Retry-After", "1")
  writeJSON(w, http.StatusTooManyRequests, map[string]any{
      "error":               "inbound dispatcher full",
      "retryAfterSeconds":   1,
      "dispatcherSaturated": true,
  })
  ```

- [x] 5.2 Update the existing 429 test in `internal/bridge/service/http_inbound_test.go`
  to assert: (a) `Retry-After: 1` header is present, (b) the response body contains
  `dispatcherSaturated: true`, (c) the HTTP status is still 429.

## 6. Config field: `QueueAcknowledgementsEnabled`

- [x] 6.1 Add `QueueAcknowledgementsEnabled bool` to the `RouterConfig` struct (or
  whatever the existing bridge router config struct is named) in
  `internal/config/config.go`. Add a field comment explaining the behavior and noting
  that `false` (the default) disables all queued acknowledgements.

- [x] 6.2 Update `cmd/schema/main.go` to declare the new field with `type: boolean`,
  description, and `default: false`. Regenerate `opencode-schema.json` via
  `go run cmd/schema/main.go > opencode-schema.json` and commit the result. (**CLAUDE.md
  schema obligation — non-negotiable.**)

- [x] 6.3 Update `docs/bridge.md` with a `router.queueAcknowledgementsEnabled` entry in
  the config reference section, explaining what it does and its default. (**CLAUDE.md
  docs obligation.**)

- [x] 6.4 Add a Viper round-trip unit test in `internal/config/` (e.g.
  `config_router_test.go`): unmarshal `.opencode.json` with `router.queueAcknowledgementsEnabled: true`
  via `viper.Unmarshal`, assert the field is `true` in the resulting struct. Pure
  `json.Unmarshal` is insufficient — Viper case-folds map keys and the issue manifests
  only through the real loader path. (**CLAUDE.md Viper round-trip obligation.**)

## 7. `QueuedAcknowledger` interface and adapter implementations

- [x] 7.1 Define the `QueuedAcknowledger` interface in `internal/bridge/bridge.go` (or
  the existing package-level types file):
  ```go
  type QueueAckToken = string

  type QueuedAcknowledger interface {
      SendQueuedAck(ctx context.Context, peer PeerRef, position int) (QueueAckToken, error)
      UpdateQueuedAck(ctx context.Context, peer PeerRef, token QueueAckToken, position int) error
  }
  ```
  `position` is 1-based. Assert the interface via compile-time check on each adapter.

- [x] 7.2 Implement `SendQueuedAck` and `UpdateQueuedAck` for the Telegram adapter
  (`internal/bridge/telegram/adapter.go`): `SendQueuedAck` calls `bot.SendMessage` with
  the queued-ack text; `UpdateQueuedAck` calls `bot.EditMessageText` with the token
  (message ID cast to int). Message IDs on Telegram are ints; the token is the
  string-encoded ID.

- [x] 7.3 Implement for the Slack adapter (`internal/bridge/slack/adapter.go`):
  `SendQueuedAck` calls `api.PostMessageContext`; `UpdateQueuedAck` calls
  `api.UpdateMessageContext`. The token is the message `ts` string.

- [x] 7.4 Implement for the Mattermost adapter (`internal/bridge/mattermost/adapter.go`):
  `SendQueuedAck` calls `client.CreatePost`; `UpdateQueuedAck` calls
  `client.UpdatePost`. The token is the post ID.

- [x] 7.5 Add unit tests for each adapter's `SendQueuedAck` / `UpdateQueuedAck`: mock
  the platform API client, call the methods, assert the correct API call was made with the
  expected message text and the token returned / consumed correctly.

## 8. Queued-ack lifecycle in `handleInbound`

- [x] 8.1 In `handleInbound` (`dispatch.go:184`), after the active-agent check and
  before the parts subscription, introduce the short-wait threshold logic (Decision 3):
  arm a 2-second timer. If the initial `ag.Run` call does NOT return `ErrSessionBusy`
  (i.e. the session was idle), skip all ack logic. If it does return `ErrSessionBusy`,
  record that an ack is pending.

- [x] 8.2 When the 2-second timer fires (i.e. the retry loop is still in progress), check
  `cfg.Router.QueueAcknowledgementsEnabled`. If true, cast the adapter to
  `QueuedAcknowledger` and call `SendQueuedAck(ctx, in.Peer, currentPosition)`. Store the
  returned token. `currentPosition` is determined by reading `len(d.inbound)` at the time
  of enqueueing — the count of messages already queued ahead.

- [x] 8.3 On each retry cycle (after each 100 ms backoff), if a token exists, call
  `UpdateQueuedAck` with the updated position. Position decreases by 1 for each message
  the dispatcher processes ahead of this one (monotonically decreasing). Position 1 means
  "you're next". Failures to update are logged at debug level; do not abort the retry.

- [x] 8.4 When the retry loop exits (either `agent.Run` succeeds or returns a non-busy
  error), if a token exists: call `UpdateQueuedAck` with a "running" state text (e.g.
  "▶ Processing your message now…") or call a `DeleteQueuedAck` / pass a sentinel position
  (0 or -1) that the adapter interprets as "resolve". Choose whichever is cleaner for the
  adapter interface; document the convention in the interface definition.

- [x] 8.5 Add a test `TestHandleInbound_QueuedAckLifecycle`: mock adapter implements
  `QueuedAcknowledger`; mock agent returns `ErrSessionBusy` 3 times then succeeds;
  assert (a) `SendQueuedAck` called once after the threshold, (b) `UpdateQueuedAck` called
  3 times with decreasing position, (c) `UpdateQueuedAck` called once more with the
  "resolved" sentinel on success. Run with `go test -race`.

## 9. Test coverage gaps (serialization and busy path)

The following tests close gaps identified in the dossier — the serialization invariant and
`ErrSessionBusy`-from-competing-actor path are currently unpinned:

- [x] 9.1 `TestDispatch_SerializationInvariant` (covered by task 1.7 above) — ensure
  exactly one Run in flight at any time.

- [x] 9.2 `TestBufferInbound_NoDrainWithoutQuestion`: verify that when an interactive
  session has pending buffered messages and a question arrives, `BufferInbound` is drained
  in FIFO order into the next Ask call.

- [x] 9.3 Ensure `go test -race ./internal/bridge/service/ ./internal/config/` is green
  after all above changes.

## 10. Verification

- [ ] 10.1 End-to-end: start a flow step against a session bound to a chat peer; send a
  chat message while the step owns the session slot; confirm (a) the message is delivered
  to the agent after the step completes (not discarded), (b) the queued-ack appears and
  is resolved when the step finishes.

- [ ] 10.2 Saturate `POST /router/inbound`: fill `inboundCh` to cap 64 and POST; assert
  response is 429 with `Retry-After: 1` header and `dispatcherSaturated: true` body.

- [x] 10.3 Confirm `go build ./...` clean and `go vet ./...` clean after all changes.
