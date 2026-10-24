# Tasks

## 1. Inbox package

- [x] 1.1 Add `internal/inbox` with per-session FIFO `Push` (atomic for a batch), `PopFirst`, `DrainAll`, `Clear`, the cap-16 drop-oldest overflow callback and an `OnDelivered` hook; verify with table-driven unit tests for ordering, atomic batches, overflow and concurrent push/drain under `-race`.
- [ ] 1.2 Move `QuestionRouter.buffered` onto the inbox (BufferInbound → Push, popBufferedLocked → PopFirst, ClearSession → Clear) with no behaviour change yet; verify `go test ./internal/bridge/...` stays green, including `interactive_buffer_test.go`.

## 2. Wire shape

- [ ] 2.1 Add `Messages []InboundMessage{ID, Text}` to `bridge.Inbound`; `/router/inbound` validates `messages` XOR `text`/attachments and non-empty ids/texts (400 otherwise); verify with `http_inbound_test.go` cases for valid batch, mixed body, empty id.
- [ ] 2.2 `dispatchInbound` pushes a batch as one unit for interactive sessions; verify a test that a batch is never interleaved with a concurrent single.

## 3. Questions and repeats

- [ ] 3.1 Add `Questions.Unanswered(requestID, note)` (tool result per design D3); verify a question-tool test that the result text is returned and no answer is recorded.
- [ ] 3.2 `TryHandleQuestionReply` / `handleNewRequest`: id-carrying input closes the question as unanswered and is drained after it; plain text answers as today; id-less buffered input keeps the auto-answer with the remainder left in the inbox; verify tests for each path, including the head-only regression now draining M2.
- [ ] 3.3 Per-run delivered-id set (design D8): repeats are skipped and re-acknowledged, a repeat-only batch never closes a question; verify unit tests across two steps of one run.
- [ ] 3.4 Step end replies "not delivered" to each id-less leftover's peer before unbinding (design D9); verify a Slack-adapter test with a fake sender.

## 4. Agent loop

- [ ] 4.1 Drain after tool results, before the next model call: one `message.User` per item, appended to `msgHistory`; verify with an agent test using a scripted provider that the second request's tail is `[tool_result, user M1, user M2]`.
- [ ] 4.2 Hold at the natural end turn and at accepted `struct_output` when the inbox is non-empty; verify tests for both (earlier `struct_output` survives when the model ends with text).
- [ ] 4.3 No drain on the max-turns wrap-up turn; a hold cycle counts against `max_turns`; verify with a test that a reviewer cannot extend a run beyond `max_turns`.

## 5. Acknowledgement

- [ ] 5.1 Fire `OnDelivered` after each drain (stored items with an id) and for repeats skipped by the per-run set; bridge fans out to bindings through a new optional `DeliveryAcker` adapter capability; verify unit tests that cleared items are never acknowledged.
- [ ] 5.2 `external` adapter: relay frame `kind: "delivered"`, `delivered.messageIds`, no text; non-202 recorded as a failed send; verify with a relay test asserting the exact frame.

## 6. Docs and checks

- [ ] 6.1 Update `docs/bridge.md` (interactive inbox, batched inbound, delivered frame) and `docs/flows.md` (mid-turn reviewer input on interactive steps); verify the docs describe the drain points and the budget rule.
- [ ] 6.2 Add an e2e script under `scripts/test/` driving an interactive step: two inbound messages during a tool call reach the next request as consecutive user messages and produce one `delivered` frame; verify `make test-e2e`.
- [ ] 6.3 `make test` green (schema unchanged: no `Config` field added).

## Workflow follow-up

- Push to the `fork` remote; the GitHub PR to the upstream repo waits for the product owner's OK on this proposal.
- The PR description names the behaviour change for Slack and Mattermost reviewers on interactive steps (mid-turn delivery, "not delivered" reply at step end, no flag), the unanswered-close of questions for id-carrying input, and that no prompt change is included.
- After merge and release, c2-agent bumps its opencode pin (CD-5481).
