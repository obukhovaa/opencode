## Why

A stalled model stream can be resent repeatedly without seeing messages queued during the stalled call. Bound those resends and deliver pending input at the safe retry boundary and when a session runs again.

## What Changes

- Limit stalled-stream recovery to two resends per model turn for Anthropic, OpenAI and Gemini.
- Deliver queued input before each stall resend and the first call of a resumed session.
- Preserve existing HTTP/transient retry policies, inactivity timeout and flow step retry.

## Capabilities

### New Capabilities
- `model-stream-stall-recovery`: bounded recovery and partial-output cleanup.

### Modified Capabilities
- `interactive-step-inbox`: input delivery at stall recovery and session re-entry.

## Impact

Provider streaming loops and the agent loop; no configuration, schema, transport or storage changes.
