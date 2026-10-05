## Why

The bridge already queues inbound messages — `sessionDispatch.inbound` is a buffered channel
(cap 16) that back-pressures adapters rather than dropping — but from a user's perspective
the queue is completely invisible: no acknowledgement is sent when a message lands behind an
in-flight run. Worse, the current `ErrSessionBusy` handler in `handleInbound` is both
incorrect and lossy: when a competing actor (flow step, cron sentinel, task auto-resume)
holds the session slot, `agent.Run` returns `ErrSessionBusy`, the inbound message is silently
discarded, and the user is told to "please resend". The `chat-bridge` spec's justification
for asserting `ErrSessionBusy` can never surface — "all reviewer fan-in goes through this
single dispatcher" — is factually wrong; it only rules out bridge-vs-bridge collisions, not
cross-actor holders, which `session-run-exclusivity/spec.md:12-14` explicitly calls out by
name. Three additional loss paths produce no user-facing signal at all: the interactive-flow
buffer's drop-oldest truncation, the early-exit when no active agent is available, and the
bare `429` from `POST /router/inbound`.

## What Changes

- **`chat-bridge` requirement R7 is amended (spec correction).** The "MUST NEVER return
  `ErrSessionBusy`" assertion is removed; the requirement now specifies the correct
  re-queue-and-retry semantics for cross-actor busy responses. The associated test
  `TestRunFailureMessage_BusyDoesNotAdviseAbort` currently pins the lossy wording and must
  be deliberately updated as part of this change.

- **`ErrSessionBusy` from cross-actor holders is handled correctly.** When `agent.Run`
  returns `ErrSessionBusy` because a flow-step agent, a cron sentinel lock, or a
  task-auto-resume holds the session slot, the bridge MUST retain and retry the inbound
  with a bounded per-attempt budget (≤ 5 min); on budget exhaustion the message is
  re-queued via the overflow mechanism for a subsequent attempt. Human message content
  MUST NEVER be discarded on `ErrSessionBusy`, regardless of how long the competing run
  lasts. The TUI drain worker's unbounded retry precedent does NOT directly apply — the
  bridge funnels all sessions through a single shared dispatch loop whose blocking
  semantics require the non-blocking push fix below.

- **Non-blocking push with per-session overflow (fixes cross-session starvation).**
  `runInboundLoop` (`service.go:426`) is a single shared goroutine; `dispatchInbound`
  currently calls `pushInbound` which blocks when `d.inbound` is full, stalling ALL other
  sessions. This change makes `dispatchInbound`'s push to `d.inbound` non-blocking:
  overflow messages go to a per-session in-memory slice drained by `run()` after each
  `handleInbound`, preserving FIFO. A single stalled session MUST NOT prevent dispatch to
  any other session.

- **Queued-acknowledgement visibility (new capability, new config field).** When an inbound
  message queues behind a run already in flight, the sender SHALL receive an acknowledgement
  naming its queue position. The acknowledgement is updated in-place (edit, not post) as the
  queue drains and resolved when the message begins its run. Config-gated under
  `router.queueAcknowledgementsEnabled` following the `router.toolUpdatesEnabled` precedent.
  This is a new `Router.*` config field and therefore triggers all four schema-update
  obligations from CLAUDE.md.

- **Interactive-flow buffer drops are audible.** When `QuestionRouter.BufferInbound` evicts
  an oldest message (cap 16), the evicted sender SHALL be notified via a reply rather than
  losing their message silently.

- **No-active-agent drop is audible.** When `handleInbound` exits early because
  `ActiveAgent()` is nil, the inbound sender SHALL be informed rather than seeing nothing.

- **`POST /router/inbound` 429 gains `Retry-After` and depth.** The bare "retry" text
  response is enriched with a `Retry-After` header and a machine-readable dispatcher
  saturation body so the orchestrator's retry is informed rather than blind.

- **Shutdown loss is log-audible.** Messages queued in a dispatcher at `Service.Stop` time
  are drained for logging (WARN per message, per-session count summary) before the channel
  is closed. In-chat notification at teardown is not attempted: `Service.Stop()` cancels
  the service context before `tearDownDispatchers()`; any `replyToPeer` call using that
  cancelled context returns immediately without reaching the platform. Durable delivery
  across restarts is an explicit non-goal.

### Non-goals

- **No queue for the non-bridge HTTP message API.** `POST /session/{id}/message` and
  `POST /session/{id}/prompt_async` (`internal/api/handler_message.go`) return 409 Conflict
  today; queueing a synchronous HTTP request means holding the connection open for an
  unbounded wait, which is a different contract question, explicitly out of scope.
- **No unification of the three inbound queues** (bridge `sessionDispatch.inbound`,
  `internal/app/queue.go`, any future HTTP one). Routing bridge inbound through the app
  queue would place two queues in series for the same session, making depth accounting and
  ordering ambiguous. Explicitly deferred.
- **Flow-owned / non-interactive runs unchanged.** The orchestrator drives opencode via
  `POST /flow` with `NonInteractive: true`; the flow engine owns the prompt sequence there,
  so a human-style queue is inapplicable.
- **No emoji-reaction primitives.** No adapter gains a reaction API in this change; the
  queued-ack uses editable text notes on all three platforms.
- **No ACP changes.** ACP is serial by construction over stdio.

## Capabilities

### New Capabilities

- `bridge-queue-visibility`: the queued-acknowledgement contract — when to send it (position
  ≥ 1), in-place-edit semantics, position reporting, the short-wait threshold before
  announcing, config gating, group/DM routing (sender only), and lifecycle (resolved when the
  message begins its run; never lingered after the run starts).

### Modified Capabilities

- `chat-bridge`: amend the per-session dispatcher requirement to replace the incorrect
  "MUST NEVER return `ErrSessionBusy`" assertion with the correct re-queue-and-retry
  contract; add requirements for the three silent-drop paths (interactive-flow buffer
  overflow, no-active-agent, shutdown loss).
- `bridge-http-api`: add `Retry-After` header and machine-readable saturation body to
  `POST /router/inbound` 429 responses.

## Impact

**`github.com/opencode-ai/opencode`**

- `internal/bridge/service/dispatch.go:193-196` (`handleInbound`, no-agent branch): reply
  to sender instead of silently returning.
- `internal/bridge/service/dispatch.go:218-223` (`handleInbound`, `agent.Run` error path):
  on `ErrSessionBusy`, retry with bounded budget; on exhaustion signal `run()` to re-queue
  via overflow rather than discarding. `ErrSessionBusy` no longer reaches `runFailureMessage`.
- `internal/bridge/service/dispatch.go:258-269` (`runFailureMessage`): the `ErrSessionBusy`
  arm becomes dead code and is removed.
- `internal/bridge/service/inbound.go:dispatchInbound` + `internal/bridge/service/dispatch.go`
  (`sessionDispatch`, `pushInbound`): replace blocking `pushInbound` call from the shared
  loop with a non-blocking push; add per-session `overflow []bridge.Inbound` slice; add
  `run()` overflow-drain step after each `handleInbound`.
- `internal/bridge/service/dispatch_test.go:60-74` (`TestRunFailureMessage_BusyDoesNotAdviseAbort`):
  deliberately updated — the test pins the old lossy wording and must change.
- `internal/bridge/service/question.go:472-481` (`BufferInbound`): notify the dropped
  sender when cap is hit.
- `internal/bridge/service/http_inbound.go:50-58` (`handleInbound` HTTP handler, 429 path):
  add `Retry-After` header and structured saturation body.
- `internal/config/config.go`: new `QueueAcknowledgementsEnabled bool` field under
  `RouterConfig` (or equivalent; naming follows existing `ToolUpdatesEnabled`).
- `cmd/schema/main.go` + `opencode-schema.json`: schema update and regeneration for the new
  field (CLAUDE.md obligation: non-negotiable).
- `docs/bridge.md`: document the new field and queue-acknowledgement behavior.
- `internal/config/config_test.go` (or new `config_router_test.go`): Viper round-trip unit
  test for the new `RouterConfig` field (CLAUDE.md obligation).
- All three platform adapters (`telegram/`, `slack/`, `mattermost/`): new
  `SendQueuedAck(ctx, peer, pos) (editToken, error)` and
  `UpdateQueuedAck(ctx, peer, editToken, pos)` plumbing, or equivalent interface.
