## 1. Implementation

- [x] 1.1 Return provider stalls to the agent and enforce two resends across recovery paths.
- [x] 1.2 Deliver pending input at stall resends and run entry using existing hooks.
- [x] 1.3 Verify partial-output cleanup, failure carry-over, and provider retry compatibility with regression tests.

## 2. Validation

- [x] 2.1 Run all Go tests, final make test, schema and hidden-character checks, and validate the spec.

Validation note: targeted agent/provider regression tests pass with the race detector. Full-suite and final-check runs retain the same 13 inherited failures (12 bridge provider fixtures and the composition cancellation budget). The unchanged generated LSP file retains six U+200B characters. Publication is permitted with these baseline limitations documented; the previously reproduced CLI timeout flake did not recur in the review-fix final check. No assertions or timeouts were relaxed.

## 3. Review corrections

- [x] 3.1 Restore independent bounded stall resends to all auxiliary summary entry points.
- [x] 3.2 Preserve accepted structured output and exhausted bookkeeping when the wrap-up stalls.
- [x] 3.3 Clear consumed initial input at outer-cycle re-entry and correct telemetry/compaction documentation.
- [x] 3.4 Run review regression tests and required gates, recording inherited failures explicitly.
