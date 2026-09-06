## Why

While the agent is running, pressing Enter in the TUI discards the submission with a
transient warning toast — the typed text is preserved in the textarea, but the user must
notice when the run finishes and press Enter again manually. Claude Code queues the input
instead and delivers it after the current response completes. Users expect the same.

## What Changes

- **Per-session in-memory queue on `app.App`.** When `editorCmp.send()` fires while the
  session is busy, the typed text and attachments are enqueued rather than rejected. The
  textarea is reset (text moves into the queue), and no warning toast is shown.
- **Queue drain worker.** A goroutine per session watches for the session slot to free,
  then drains the queue one message at a time — each as a separate `agent.Run` call so
  user intent is preserved and ordering is FIFO. `ErrSessionBusy` from `Run` is treated
  as a retryable condition, not a surfaced error.
- **TUI affordance.** The chat list (or an inline banner) shows "N message(s) queued"
  while the queue is non-empty, with a dedicated key binding to discard the entire queue.
- **Explicit non-modification of `session-run-exclusivity`.** The queue never attempts a
  concurrent `Run`; it only starts the next run after the slot is genuinely free. The
  one-Run-per-session invariant is preserved by construction: the drain worker simply
  waits for the slot to open before calling `Run`, and receives `ErrSessionBusy` as a
  retryable signal rather than an error. No requirement in the `session-run-exclusivity`
  spec changes.
- **OpenEditor and custom commands remain gated.** The Ctrl+E (open external `$EDITOR`)
  path and `dialog.CommandRunCustomMsg` keep their busy-reject guard. External editor
  sessions and slash commands mutate session state in ways that do not compose safely
  with queued chat messages; queuing them is a separate, later decision.

### Non-goals

- Mid-run injection between tool calls is explicitly out of scope. Queued messages are
  delivered only after the current run completes — not between tool calls — because the
  direct Anthropic API silently combines consecutive same-role turns while Bedrock has
  historically rejected them outright; VertexAI behavior is unverified. Each outcome
  is a distinct correctness risk that requires dedicated investigation before mid-run
  injection can be attempted safely. This non-goal must be stated in release notes so
  no user or reviewer expects intra-run delivery.
- Flow-owned sessions (`NonInteractive = true`) are out of scope. The queue MUST NOT
  enqueue against sessions owned by the flow engine.

## Capabilities

### New Capabilities

- `chat-message-queue`: the per-session in-memory queue, its drain lifecycle (startup,
  drain ordering, cancel semantics, overflow policy, session-switch and shutdown
  behavior), the TUI affordance and discard key, the scope exclusions (OpenEditor,
  commands, flow sessions), and the testability contract for the drain worker.

### Modified Capabilities

<!-- session-run-exclusivity is NOT modified: the queue never runs two concurrent Runs;
     it only starts the next Run after the slot is free. The one-Run-per-session
     invariant is preserved by construction and no requirement in that spec changes. -->

## Impact

**`github.com/opencode-ai/opencode`**

- `internal/tui/components/chat/editor.go:140-159` (`send`): replace the reject-with-toast
  with an enqueue call to `app.App`; reset the textarea on successful enqueue (text moves
  into the queue, so the textarea is cleared unlike today).
- `internal/tui/page/chat.go:160-167` (`Update` / `chat.SendMsg` handler): route through
  the queue; `ErrSessionBusy` from the agent MUST NOT surface as a red error toast.
- `internal/tui/page/chat.go:442-457` (`sendMessage`): called by the drain worker and by
  the immediate path when the session is idle; `ErrSessionBusy` here becomes retryable
  via the drain, not a fatal error.
- `internal/app/app.go`: per-session queue (`[]QueuedMessage`) and drain worker goroutine,
  keyed by session id; goroutine-safe map; lifecycle managed so workers do not leak across
  session switches or on shutdown.
- `internal/tui/components/chat/list.go` (or a new small inline component): render "N
  queued" affordance and expose a key binding to discard the queue; rendered distinctly
  from persisted chat messages because queued messages are never persisted until dequeued.
- `internal/tui/page/chat.go:265-267` (Esc / cancel path): Esc cancels the in-flight run
  but queued messages survive by default; the discard key is the explicit mechanism to
  clear the queue.
