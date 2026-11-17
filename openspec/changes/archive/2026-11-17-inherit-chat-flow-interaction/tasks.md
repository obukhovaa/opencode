## 1. Implementation

- [x] 1.1 Add conversation-derived defaults during named admission and snapshot persistence without changing request idempotency.
- [x] 1.2 Restore missing targets from recorded origin after legacy step state restoration, preserving authorization and standalone behavior.
- [x] 1.3 Document named interactive chat invocation and recovery semantics.

## 2. Verification

- [x] 2.1 Cover native/external peer shapes, explicit targets, required schemas, recovery and thread isolation with regression tests.
- [x] 2.2 Exercise a real named interactive question/answer flow through OpenCode and the C2 external bridge harness.
- [x] 2.3 Complete independent review, fix findings and pass repository checks.

## 3. Release preparation

- [x] 3.1 Record verification evidence and the downstream runtime upgrade plan, then sync and archive the completed specification.
