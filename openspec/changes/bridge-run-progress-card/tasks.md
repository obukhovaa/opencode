# Tasks: bridge-run-progress-card

## 1. Editable messages

- [x] 1.1 `internal/bridge/bridge.go`: add `EditableMessageToken` and the `MessageEditor`
  interface (`SendEditable` / `EditMessage`) beside `QueuedAcknowledger`
- [x] 1.2 Slack, Telegram, Mattermost `adapter.go`: implement `MessageEditor`; rebase
  `SendQueuedAck` / `UpdateQueuedAck` on it, keeping token formats unchanged
- [x] 1.3 Confirmed the `external` relay adapter stays text-only (no `MessageEditor`)

## 2. Progress tracker

- [x] 2.1 New `internal/bridge/service/progress.go`: `runProgress` with counters, in-flight
  set, last failure, terminal status, per-peer tokens, `wake` (cap 1) and `done`;
  `snapshot()` producing card text plus a one-shot failure text; `run(ctx, deliver)`
  worker paced by `progressMinInterval`
- [x] 2.2 `dispatch.go`: `progressStart` after `agent.Run` accepts, gated on
  `ToolUpdatesEnabled && mode == compact`; `progressFinish(status)` in the deferred tail
  after the parts grace window, waiting on `done` with a short bound; the terminal
  `AgentEventTypeError` selects the failed state
- [x] 2.3 `handlePartEvent`: at any mode feed the card when one exists (call start → in
  flight, result → count/failure); emit per-call cards only at `full`; keep recording
  and consuming the start-time map so it cannot grow when cards are suppressed; failures
  without a card still post a fresh `✗` line (`shouldEmitToolCallCard` /
  `shouldEmitToolResultCard`)
- [x] 2.4 `deliverProgress`: resolve bindings per flush; editors get post-then-edit with
  fresh-post recovery; text-only peers get only an undelivered failure line
- [x] 2.5 Rewrite the `handlePartEvent` doc block and the `toolErrorPreviewRunes` comment
  for the new default

## 3. Verbosity values

- [x] 3.1 `internal/bridge/config.go`: `NormalizeToolUpdateVerbosity` maps `verbose` and
  `debug` to `full`; rewrite the `ToolUpdateVerbosity` field doc for the card
- [x] 3.2 `internal/bridge/service/service.go`: `SetToolVerbosity` error text names the
  aliases
- [x] 3.3 `internal/bridge/service/commands.go`: `/verbosity` descriptions describe the
  card vs per-call cards

## 4. Tests

- [x] 4.1 `internal/bridge/config_test.go`: `verbose` / `debug` / `" Debug "` → full;
  `chatty` → compact, not ok
- [x] 4.2 `internal/bridge/service/progress_test.go`: header text at each state
  (thinking, in flight, counts, failure, terminal ok/error); failure line and one-shot
  text fallback; updates after finish ignored; worker orders and coalesces a burst under
  a short interval; gate tables; dispatcher end-to-end at compact (one post, edits only,
  no per-call sends, relay gets the failure line once), at full (per-call sends, no
  card) and with tool updates off (failure line only)
- [x] 4.3 `internal/bridge/service/commands_verbosity_test.go`: `/verbosity` lists two
  modes and marks the live one, accepts `verbose` and `debug` reporting `full`, rejects
  `chatty`
- [x] 4.4 Slack / Telegram / Mattermost `editable_test.go`: one post then edits of the
  same message; malformed token rejected
- [x] 4.5 `go test ./internal/bridge/...` and `make test` green

## 5. Docs

- [x] 5.1 `docs/bridge.md`: `toolUpdateVerbosity` row and `/verbosity` rows describe the
  card, the aliases and the text-only-adapter failure behaviour
- [x] 5.2 Schema: confirmed the `router` block is not declared in `cmd/schema/main.go`;
  nothing to regenerate
