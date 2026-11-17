# Validation

## Final candidate — 2026-11-17

- Merged upstream main at `837cab1c32c2d215814ec0b567682728560db48e` without rewriting the original producer commits.
- Focused iteration tests passed with the race detector. Added real HTTP `/flow/status?runID=` coverage for repeated reads, completed/failed/postponed terminal retention, and a newer run reusing the same step session while the retained run keeps its original iteration.
- Full isolated `make test` passed: formatting, vet, all packages with race detection and 70.9% coverage. The test environment uses a private HOME and dummy provider keys, with the real Go build cache; no live model calls are needed.
- Independent producer review approved the additive field/copy and recovery tests.
- Strict OpenSpec validation and `git diff --check` passed. No configuration/schema generation change is required.
- The original baseline failures recorded on `7c588c3a` were reproduced before this revision, but are superseded by this successful full gate on current main. The unchanged generated LSP protocol's hidden-character warning is outside this change.

This is the OpenCode producer prerequisite paired with c2-agent MR !262. It does not implement Preview delivery, a globally unique execution ID or a complete parallel-step snapshot. CD-5719 must not be closed solely from publishing these prerequisites.
