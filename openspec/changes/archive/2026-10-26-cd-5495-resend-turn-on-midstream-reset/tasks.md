## 1. Agent loop
- [x] 1.1 Export `provider.IsPeerStreamReset` over the existing retryable `RST_STREAM` check.
- [x] 1.2 In `processGeneration`, resend a turn once on a peer reset model-call error: pause, delete the partial message, and send the same history again. Skip it after the overflow recovery, on a cancel or deadline, and for every other error.

## 2. Tests and docs
- [x] 2.1 Unit tests: resend after partial output with the partial message deleted; only once; other errors not resent; not after the overflow recovery; a cancel during the pause ends as cancelled.
- [x] 2.2 Document the resend in `docs/compaction.md` next to the overflow retry.
- [x] 2.3 `go vet` plus the agent and provider package tests.
- [x] 2.4 TUI: the chat list handles a message `DeletedEvent`, so the deleted partial message leaves the screen.

## 3. Rollout
- [ ] 3.1 After release and the downstream pin bump, confirm in daemon logs that a mid-stream reset logs the resend and the turn completes.
