## 1. Regression and gate
- [x] 1.1 Reproduce the old prefix-glob bypass using a harmless local marker fixture.
- [x] 1.2 Add a syntax-aware evaluator and apply it before all bash dispatch modes.
- [x] 1.3 Cover every ticket separator/substitution, literals, malformed syntax and verdict precedence.
- [x] 1.4 Verify foreground/background denial occurs before the allowed prefix runs.

## 2. Contract and checks
- [x] 2.1 Document scope, compatibility and parser limitations.
- [x] 2.2 Run focused race tests, final make test and deterministic hook e2e (9 checks); all pass with LC_ALL=C.
- [x] 2.4 Provider-backed release e2e: not applicable to this change per Arne's review (no provider/agent-loop code changed); use focused local gate tests instead. The previously rejected external-provider batch was not run.
- [x] 2.3 Validate the OpenSpec change and inspect the final diff.

## 3. Review and delivery
- [x] 3.1 Open draft PR #110 and reply in the original CD-5483 board thread.
- [ ] 3.2 Human approval, merge, engine release and downstream deployment (not automatic).

## 4. Review follow-up
- [x] 4.1 Reproduce the four same-statement parameter/arithmetic bypasses with policy-only fixtures.
- [x] 4.2 Refuse uninspectable re-evaluation, add both-dispatch-mode regressions, and retain literal positive cases.
- [x] 4.3 Add non-Bash foreground fallback, explicit parse/redirect limits and the extra requested negative cases.
- [x] 4.4 Run final local checks (full make test plus permission/tool/shell race suites).
- [ ] 4.5 Arne re-reviews and approves the corrected head.
