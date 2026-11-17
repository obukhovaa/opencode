## 1. Proposal and review

- [x] 1.1 Inspect existing flow delivery, vendor capabilities and failure modes.
- [x] 1.2 Validate and independently review proposal, design and acceptance scenarios.

## 2. Implementation

- [x] 2.1 Add presentation metadata and durable fenced per-recipient card storage with SQLite/MySQL coverage.
- [x] 2.2 Render and edit bounded native Slack, Mattermost and Telegram flow progress with platform fallbacks.
- [x] 2.3 Integrate replay-safe, paced delivery and live verbosity; preserve external relay/question behavior and fix failed-send acknowledgement.
- [x] 2.4 Document behavior, truncation, verbosity and recovery guarantees.

## 3. Verification and release

- [x] 3.1 Test native request payloads, compact/full/disabled behavior, restart/replay, multi-peer isolation and failed-send retry.
- [x] 3.2 Review the implementation independently and resolve findings.
- [x] 3.3 Run final make test, end-to-end checks and strict OpenSpec validation.
- [x] 3.4 Prepare release notes and complete source-readiness checks for patch publication.
