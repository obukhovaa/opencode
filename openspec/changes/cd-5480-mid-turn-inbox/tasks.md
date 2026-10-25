# Tasks

## 1. Inbox package

- [x] 1.1 Add `internal/inbox` with per-session FIFO `Push` (atomic for a batch), `PopFirst`, `DrainAll`, `Clear`, the cap-16 drop-oldest overflow callback and an `OnDelivered` hook; verify with table-driven unit tests for ordering, atomic batches, overflow and concurrent push/drain under `-race`.
- [x] 1.2 Move `QuestionRouter.buffered` onto the inbox (BufferInbound → Push, popBufferedLocked → PopFirst, ClearSession → Clear) with no behaviour change yet; verify `go test ./internal/bridge/...` stays green, including `interactive_buffer_test.go`.

## 2. Wire shape

- [x] 2.1 Add `Messages []InboundMessage{ID, Text}` to `bridge.Inbound`; `/router/inbound` validates `messages` XOR `text`/attachments and non-empty ids/texts (400 otherwise); verify with `http_inbound_test.go` cases for valid batch, mixed body, empty id.
- [x] 2.2 `dispatchInbound` pushes a batch as one unit for interactive sessions; verify a test that a batch is never interleaved with a concurrent single.

## 3. Questions and repeats

- [x] 3.1 Add `Questions.Unanswered(requestID, note)` (tool result per design D3); verify a question-tool test that the result text is returned and no answer is recorded.
- [x] 3.2 `TryHandleQuestionReply` / `handleNewRequest`: id-carrying input closes the question as unanswered and is drained after it; plain text answers as today; id-less buffered input keeps the auto-answer with the remainder left in the inbox; verify tests for each path, including the head-only regression now draining M2.
- [x] 3.3 Per-run delivered-id set (design D8): repeats are skipped and re-acknowledged, a repeat-only batch never closes a question; verify unit tests across two steps of one run.
- [x] 3.4 Step end replies "not delivered" to each id-less leftover's peer before unbinding (design D9); verify a Slack-adapter test with a fake sender.

## 4. Agent loop

- [x] 4.1 Drain after tool results, before the next model call: one `message.User` per item, appended to `msgHistory`; verify with an agent test using a scripted provider that the second request's tail is `[tool_result, user M1, user M2]`.
- [x] 4.2 Hold at the natural end turn and at accepted `struct_output` when the inbox is non-empty; verify tests for both (earlier `struct_output` survives when the model ends with text).
- [x] 4.3 No drain on the max-turns wrap-up turn; a hold cycle counts against `max_turns`; verify with a test that a reviewer cannot extend a run beyond `max_turns`.

## 5. Acknowledgement

- [x] 5.1 Fire `OnDelivered` after each drain (stored items with an id) and for repeats skipped by the per-run set; bridge fans out to bindings through a new optional `DeliveryAcker` adapter capability; verify unit tests that cleared items are never acknowledged.
- [x] 5.2 `external` adapter: relay frame `kind: "delivered"`, `delivered.messageIds`, no text; non-202 recorded as a failed send; verify with a relay test asserting the exact frame.

## 6. Docs and checks

- [x] 6.1 Update `docs/bridge.md` (interactive inbox, batched inbound, delivered frame) and `docs/flows.md` (mid-turn reviewer input on interactive steps); verify the docs describe the drain points and the budget rule.
- [x] 6.2 Add an e2e script under `scripts/test/` driving an interactive step: two inbound messages during a tool call reach the next request as consecutive user messages and produce one `delivered` frame; verify `make test-e2e`.
- [x] 6.3 `make test` green (schema unchanged: no `Config` field added).

## 7. Review regressions

- [x] 7.1 Resolve review regressions: reserve drained input through persistence and delivery bookkeeping, scope pending-id deduplication per peer, and verify retries and failed writes under the race detector.

## Workflow follow-up

- Push to the `fork` remote; the GitHub PR to the upstream repo waits for the product owner's OK on this proposal. (OK given 2026-10-24.)
- The PR description names the behaviour change for Slack and Mattermost reviewers on interactive steps (mid-turn delivery, "not delivered" reply at step end, no flag), the unanswered-close of questions for id-carrying input, and that no prompt change is included.
- After merge and release, c2-agent bumps its opencode pin (CD-5481).

## Implementation notes

- 1.1: overflow is reported by `Push` returning the evicted items rather than through a callback; the delivery hook is registered per session on `Push` and dropped by `Clear`.
- 3.4: the overflow notice is now sent only for evicted items without an id, like the step-end notice; an evicted id is never acknowledged, so its sender re-sends it.
- 4.2: after a reopen, only a `struct_output` emitted in the current turn ends the run early; a plain tool call no longer finishes it on the earlier output.
- 2.2: a `messages` batch for a session that no interactive step owns (outside pool mode) is joined into one prompt for the dispatcher and acknowledged when queued.
- 6.2: like `struct_output.sh`, the e2e gate wraps `go test` on the real subsystems, since the agent loop has no exported seam for a fake provider.
- 6.3: `make test` is green apart from two failures unrelated to this change and seen on the base commit or under load: `TestGenerationInputGateFromLoadedConfig` needs `LANGFUSE_PUBLIC_KEY` in the environment, and `TestExec_TimeoutKillsProcessGroup` is timing-sensitive and passes on rerun.
- 7.1: reproduced both review failures before the fixes. The inbox, bridge and agent regression tests pass under `-race`, including reservation cleanup and retries after failed writes. The full `LC_ALL=C GOFLAGS=-p=2 make test` run and `make schema-check` pass on 2026-10-25.
