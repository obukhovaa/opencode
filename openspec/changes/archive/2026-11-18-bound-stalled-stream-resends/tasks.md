## 1. Implementation

- [x] 1.1 Return provider stalls to the agent and enforce two resends across recovery paths.
- [x] 1.2 Deliver pending input at stall resends and run entry using existing hooks.
- [x] 1.3 Verify partial-output cleanup, failure carry-over, and provider retry compatibility with regression tests.

## 2. Validation

- [x] 2.1 Run all Go tests, final make test, schema and hidden-character checks, and validate the spec.

Validation note: release preparation replaces leaking bridge fixtures and timing-dependent composition cancellation with isolated, coordinated tests, removes invisible generated-comment characters, and fixes CLI child cleanup. The final release gate must pass in full; no baseline failures are waived. Assertions and timeout budgets are not weakened.

## 3. Review corrections

- [x] 3.1 Restore independent bounded stall resends to all auxiliary summary entry points.
- [x] 3.2 Preserve accepted structured output and exhausted bookkeeping when the wrap-up stalls.
- [x] 3.3 Clear consumed initial input at outer-cycle re-entry and correct telemetry/compaction documentation.
- [x] 3.4 Run review regression tests and required gates, recording inherited failures explicitly.

## 4. Final release review corrections

- [x] 4.1 Preserve current tool results across repeated compactions and prevent replay of consumed entry input.
- [x] 4.2 Pair partial wrap-up tool calls for cancellation and non-stall errors, preserving accepted output and exhaustion state.
