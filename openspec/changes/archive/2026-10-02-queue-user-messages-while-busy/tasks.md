## 1. Per-session queue type in `internal/app/`

- [x] 1.1 Define `QueuedMessage` struct in `internal/app/app.go` (or a new file
  `internal/app/queue.go`) with fields `Text string`, `Attachments []message.Attachment`.
- [x] 1.2 Add `queues map[string][]QueuedMessage` and `queueMu sync.Mutex` to `App`;
  add `queueCancels map[string]context.CancelFunc` to track per-session drain workers.
- [x] 1.3 Implement `App.EnqueueMessage(sessionID string, msg QueuedMessage)` (goroutine-safe):
  append to `queues[sessionID]`, start drain worker if not already running.
- [x] 1.4 Implement `App.DequeueMessage(sessionID string) (QueuedMessage, bool)` (goroutine-safe):
  pop the head of the queue, return false when empty.
- [x] 1.5 Implement `App.QueueLen(sessionID string) int` (goroutine-safe): returns
  current queue length for the affordance.
- [x] 1.6 Implement `App.DiscardQueue(sessionID string)` (goroutine-safe): clear the
  queue for the given session (used by the discard key binding).

## 2. Drain worker in `internal/app/`

- [x] 2.1 Implement `App.startDrainWorker(sessionID string)` (called under `queueMu`):
  creates a `context.WithCancel` child of the app context, stores the cancel in
  `queueCancels`, and launches a goroutine.
- [x] 2.2 Drain worker loop: call `agent.Run(ctx, sessionID, msg.Text, 0, msg.Attachments...)`
  directly. `IsSessionBusy` MAY be used as a non-authoritative poll to skip a pointless
  `Run` call when the session is observably busy (e.g. sleep 50 ms when `IsSessionBusy`
  returns true), but MUST NOT be treated as the correctness gate — the authoritative
  exclusivity mechanism is `ErrSessionBusy` returned by `RunWith`'s atomic acquire.
  DO NOT introduce a check-then-act pattern where a false `IsSessionBusy` result is
  assumed to guarantee a successful acquire.
- [x] 2.3 On `ErrSessionBusy` from `Run`: re-prepend the message to the head of the
  queue (`queueMu` guarded) and apply a 100 ms back-off before the next iteration.
- [x] 2.4 On any non-`ErrSessionBusy` error from `Run`: surface the error through the
  TUI error-reporting path with attribution (e.g. prefix the message with "Queued message
  failed: " or equivalent so the user knows this was not an interactive submission); halt
  the drain worker (exit the goroutine); preserve the remaining queue — do NOT discard it.
  The worker's `queueCancels` entry MUST be removed on halt so a subsequent enqueue
  starts a fresh worker.
- [x] 2.5 Worker exits when `DequeueMessage` returns false (empty queue); remove the
  cancel from `queueCancels` so a subsequent enqueue starts a fresh worker.
- [x] 2.6 Worker exits on context cancellation (app shutdown or explicit stop).
- [x] 2.7 In app shutdown (`App.Close` or equivalent): cancel all live drain workers via
  `queueCancels`; wait for goroutines to exit (use a `sync.WaitGroup` on workers).

## 3. Wire `editorCmp.send()` to enqueue

- [x] 3.1 In `internal/tui/components/chat/editor.go:140` (`send()`): replace the
  `if m.app.ActiveAgent().IsSessionBusy(m.session.ID) { return util.ReportWarn(...) }`
  guard with routing on `(QueueLen > 0 || IsSessionBusy)`: if either condition is true,
  call `m.app.EnqueueMessage(m.session.ID, QueuedMessage{...})`, reset the textarea,
  clear attachments, return nil. If both are false (queue empty AND session idle), fall
  through to the existing direct dispatch path unchanged.
- [x] 3.2 Ensure `m.textarea.Reset()` and `m.attachments = nil` execute before the
  enqueue returns to the caller — text has moved into the queue, not lost.
- [x] 3.3 Verify that an empty-value submit while busy or queue non-empty is a no-op
  (the empty-string guard at `editor.go:150` must run before or within the routing check
  so an empty submit is always discarded regardless of queue state).
- [x] 3.4 Verify that when the queue is empty AND the session is idle, `send()` takes
  the existing direct dispatch path with no `EnqueueMessage` call — idle-path latency
  and error surfacing are unchanged.

## 4. Suppress `ErrSessionBusy` toast in `sendMessage`

- [x] 4.1 In `internal/tui/page/chat.go:442` (`sendMessage`): when `activeAgent.Run`
  returns `ErrSessionBusy`, return nil (no error toast) instead of
  `util.ReportError(err)`. Add an `errors.Is(err, agent.ErrSessionBusy)` guard.
- [x] 4.2 Verify the change does not suppress genuinely unexpected errors — only
  `ErrSessionBusy` is silenced; all other errors still route to `util.ReportError`.

## 5. TUI queue affordance

- [x] 5.1 Add a `QueueCountMsg` (or equivalent) Bubble Tea message that the drain worker
  and `EnqueueMessage` emit to update the TUI's queue count for a session.
  (Implemented as `app.DrainEvent`, delivered via `SetDrainNotifier` → `program.Send`.)
- [x] 5.2 In `internal/tui/components/chat/list.go` (or a new
  `internal/tui/components/chat/queue_banner.go`): render an inline banner "N message(s)
  queued — press <key> to discard" when `QueueLen(sessionID) > 0`. The banner MUST be
  styled distinctly from chat messages (e.g., a muted/info color, not a bubble).
- [x] 5.3 Add a `DiscardQueue` key binding for the discard action. `ctrl+d` MUST NOT be
  used — it is bound to `KeyMap.InsertNewline` in the bubbles v2 textarea default KeyMap
  and would collide since unhandled keys fall through to `m.textarea.Update`. Before
  adopting any candidate key, verify it is absent from all three sources: `editorMaps`
  (Send, OpenEditor), `DeleteKeyMaps` (AttachmentDeleteMode, Escape, DeleteAllAttachments)
  — both defined in `editor.go` — and the textarea's default `KeyMap` from bubbles v2
  (`charm.land/bubbles/v2/textarea`). Wire the chosen binding in `internal/tui/page/chat.go`
  to call `p.app.DiscardQueue(p.session.ID)` and emit a `QueueCountMsg{Count: 0}`.
  (Chosen key: `ctrl+x`. Verified absent from editorMaps, DeleteKeyMaps, messageKeys,
  and the bubbles v2 textarea default KeyMap.)
- [x] 5.4 Show the discard key in the editor's help/status bar while the queue is
  non-empty (update `internal/tui/components/chat/editor.go` `View()` or the chat page's
  help bar). (Shown in `queueBanner()` in list.go: "N messages queued — press ctrl+x to discard".)
- [x] 5.5 Hide the banner when `QueueLen == 0` (drain completed or discard pressed).

## 6. Preserve OpenEditor and command guards

- [x] 6.1 Confirm that `internal/tui/components/chat/editor.go:384-387` (Ctrl+E /
  OpenEditor path) retains its `IsSessionBusy` warn-and-return with no change.
- [x] 6.2 Confirm that `internal/tui/page/chat.go:169-171` (`dialog.CommandRunCustomMsg`
  handler) retains its `IsBusy()` warn-and-return with no change.
- [x] 6.3 Add a comment at each guard site noting that queueing these paths is a future
  decision and must not be folded into this change.

## 7. Tests

- [x] 7.1 `internal/app/queue_test.go` (new): unit test `EnqueueMessage` + `DequeueMessage`
  FIFO order; `DiscardQueue` empties the queue; `QueueLen` tracks correctly; concurrent
  enqueue/dequeue under `go test -race` — no data race.
- [x] 7.2 `internal/app/drain_test.go` (new): drain worker with a fake `agent.Service`
  mock (use `go generate ./...` mocks or inline stub):
  - A second `EnqueueMessage` while the fake is "busy" does not call `Run` until the
    fake marks itself idle.
  - FIFO: two queued messages result in two sequential `Run` calls in enqueue order.
  - `ErrSessionBusy` from `Run` causes retry (second `Run` call on the same message)
    without surfacing an error; the message is not lost.
  - A non-`ErrSessionBusy` error from `Run` causes the worker to surface an attributed
    error notification, halt (worker goroutine exits, `queueCancels` entry removed), and
    leave the remaining queue intact; a fresh `EnqueueMessage` starts a new worker.
  - Worker goroutine terminates after the queue drains (use
    `goleak.VerifyNone` or a `WaitGroup` assertion).
  - Context cancellation (app shutdown) terminates the worker even when the queue is
    non-empty.
- [x] 7.3 `internal/tui/components/chat/editor_busy_test.go` (new): construct a minimal
  `editorCmp` backed by a stub `App`; assert that `send()` while busy enqueues, resets
  the textarea, and returns nil (no warning `tea.Cmd`).
- [x] 7.6 `internal/app/drain_test.go` additions: test that a submission arriving while
  the queue is non-empty but the slot is momentarily idle is enqueued (not dispatched
  directly) and delivered after the already-queued message — FIFO is preserved across
  the idle window between drain deliveries.
- [x] 7.7 `internal/tui/components/chat/editor_busy_test.go` additions:
  - `send()` while queue is empty AND session idle calls `agent.Run` directly (no
    `EnqueueMessage`), matching the pre-change behavior.
  - `send()` while queue is non-empty but session is idle enqueues (does NOT dispatch
    directly), preserving FIFO.
  - A non-`ErrSessionBusy` error from `agent.Run` on the idle path surfaces to the TUI
    (returns a non-nil `tea.Cmd` / `util.ReportError`).
- [x] 7.8 `internal/llm/agent/session_locks_test.go`: run unchanged; confirm green with
  `go test -race ./internal/llm/agent/`.
- [x] 7.9 Run `go test -race ./internal/app/ ./internal/tui/... ./internal/llm/agent/`
  and confirm all pass.
- [x] 7.10 Run `make test` per `CLAUDE.md` final-check requirement.
