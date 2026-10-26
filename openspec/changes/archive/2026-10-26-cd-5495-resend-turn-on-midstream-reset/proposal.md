## Why

CD-5495: a peer HTTP/2 `RST_STREAM` (`stream error: stream ID 1; INTERNAL_ERROR;
received from peer`) that arrives after the first streamed event fails the whole
agent turn. The provider stream loops replay a reset only while nothing has reached
the consumer (GENAI-92, CD-5305). With extended thinking, a thinking delta arrives
within seconds, so a reset a few seconds into the call surfaces as the run's error:
`failed to process events: stream error: ...`. Long-lived daemon sessions lose a whole
scheduled turn this way, and flow steps lose a step. The same request sent again
succeeds. Production logs show 15 of these resets in a week, and every one of them was
in the "after partial output" branch.

## What Changes

- When a turn's model call fails with a retryable peer `RST_STREAM`
  (`provider.IsPeerStreamReset`, the same codes the stream loops replay), the agent loop
  deletes the partial assistant message, waits one second, and sends the same history
  again, once per turn.
- This is safe for the same reason the overflow retry is: a model call error comes from
  the response stream before any tool ran, so nothing executes twice. The deleted
  partial message keeps the history free of a second assistant message for the turn.
- The resend does not run when the turn already went through the overflow recovery,
  when the run is cancelled or past its deadline, or for any other error. A cancel
  during the wait ends the run as cancelled. If the resend fails, the run fails with the
  resend's error.
- A reset the provider stopped replaying before any output (its replay budget ran out)
  reaches the agent loop as the same error and is resent the same way.
- The TUI's message list drops a deleted message, so the discarded partial message does
  not stay on screen half-written.
- The provider stream loops are unchanged.

## Capabilities

### New Capabilities
- `model-stream-reset-recovery`: a turn whose model stream a peer reset after
  partial output is resent once.

### Modified Capabilities
None.
