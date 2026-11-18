## 1. Implementation

- [x] 1.1 Return provider stalls to the agent and enforce two resends across recovery paths.
- [x] 1.2 Deliver pending input at stall resends and run entry using existing hooks.
- [x] 1.3 Verify partial-output cleanup, failure carry-over, and provider retry compatibility with regression tests.

## 2. Validation

- [ ] 2.1 Run all Go tests, final make test, schema and hidden-character checks, and validate the spec.

Validation note: targeted agent/provider regression tests pass with the race detector. Full-suite and final-check runs were attempted; publication remains gated on unrelated baseline test failures and the existing generated LSP hidden-character finding. No assertions or timeouts were relaxed.
