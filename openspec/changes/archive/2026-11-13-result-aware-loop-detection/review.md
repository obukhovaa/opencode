# PR #120 review

Reviewed against main after PRs #119 and #121. The original result-aware design is retained; two dispatch/provenance issues were fixed before merge.

- A successful `question` response is not always human input: unattended defaults and unanswered notifications also return success. The question tool now carries runtime-only provenance for actual answers. Only actual answers reset repetition; automatic defaults and notifications remain protected. Actual answers are delivered even with a repeat threshold of one.
- Preflight admission could queue every repeated sequential call before the threshold was observed, or discard a blocked call before an earlier answer reset history. Sequential calls now check immediately before execution. Already-running parallel calls complete, and forced terminal tools remain exempt.

Validation: real question-tool regressions reproduced the automatic/default, failed-delivery and threshold-one bugs before the fix. Sequential-batch regressions, existing loop/inbox/permission/forced-output tests and focused race tests pass. Full `make test` passes on the updated main baseline using an isolated HOME, LC_ALL=C and synthetic provider credentials. No real provider or chat calls were used. The inbox/session-flow E2E gates cover the newly merged flow input path as well.

Tool response provenance is not serialized or advertised in model tool schemas. The separate permission-denial and max-turn budgets are unchanged.
