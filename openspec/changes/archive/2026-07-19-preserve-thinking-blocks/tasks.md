# Tasks — Preserve Thinking Blocks

## 1. Message model

- [x] 1.1 Extend `message.ReasoningContent` with `Signature string`, `Redacted bool`, `Data string` (all `json:...,omitempty`); keep `String()`/display semantics (redacted renders empty)
- [x] 1.2 Verify part marshal/unmarshal round-trips the new fields under the existing `reasoning` partType (`internal/message/message.go`) and add/extend a serialization unit test incl. a legacy row (no signature) case
- [x] 1.3 Add `SetReasoningParts(parts []ReasoningContent)` (or equivalent finalize helper) that replaces streamed reasoning preview parts with authoritative blocks while preserving part ordering ahead of text; keep `AppendReasoningContent` for live deltas

## 2. Provider surface

- [x] 2.1 Add `Reasoning []message.ReasoningContent` to `ProviderResponse` (`internal/llm/provider/provider.go`)
- [x] 2.2 anthropic.go: extract finalized thinking/redacted blocks (text + signature / opaque data, in order) from the accumulated message in the streaming path (`MessageStopEvent` and truncated-stream fallback) and from the non-streaming `send` path; populate `ProviderResponse.Reasoning`
- [x] 2.3 anthropic.go `convertMessages`: emit stored reasoning blocks first per assistant message — `ThinkingBlockParam{Thinking, Signature}` for signed parts, `RedactedThinkingBlockParam{Data}` for redacted parts; skip unsigned/non-redacted parts; never attach cache_control to reasoning blocks

## 3. Agent persistence

- [x] 3.0 agent.go `EventThinkingDelta` handler: append `event.Thinking` (currently appends the always-empty `event.Content` — thinking never persisted/displayed; pre-existing bug)
- [x] 3.1 agent.go: on `EventComplete` (and the send/StreamToResponse paths that finalize messages), replace the assistant message's reasoning parts with `response.Reasoning` before finish/persist
- [x] 3.2 Confirm `cleanMessages` still drops reasoning-only assistant messages (no text, no tool calls) — add regression coverage if not already covered

## 4. Tests

- [x] 4.1 Unit test: convertMessages replays a signed thinking block verbatim (text+signature byte-equal), before text/tool_use, and skips unsigned parts
- [x] 4.2 Unit test: redacted_thinking round-trip (persist → replay as RedactedThinkingBlockParam with identical data)
- [x] 4.3 Unit test: multi-block finalization — two thinking blocks stay separate parts with distinct signatures, replayed in order
- [x] 4.4 Unit test: cache_control still lands on last text/tool_use block when reasoning blocks are present
- [x] 4.5 Run `go test ./internal/message/ ./internal/llm/provider/ ./internal/llm/agent/` and fix fallout (mocks, fixtures)
