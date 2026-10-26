## 1. Regression and gate
- [x] 1.1 Reproduce the old prefix-glob bypass using a harmless local marker fixture.
- [x] 1.2 Add a syntax-aware evaluator and apply it before all bash dispatch modes.
- [x] 1.3 Cover every ticket separator/substitution, literals, malformed syntax and verdict precedence.
- [x] 1.4 Verify foreground/background denial occurs before the allowed prefix runs.

## 2. Contract and checks
- [x] 2.1 Document scope, compatibility and parser limitations.
- [x] 2.2 Run focused race tests, final make test and deterministic hook e2e (9 checks); all pass with LC_ALL=C.
- [ ] 2.4 Finish remaining release e2e scripts after approval for provider-backed tests. The first run passed through grep, then a locale-only hook failure; hooks pass on the local rerun. Automatic approval review rejected the remaining broad batch because it can call external providers and incur costs.
- [x] 2.3 Validate the OpenSpec change and inspect the final diff.

## 3. Review and delivery
- [ ] 3.1 Open a review PR and reply in the original CD-5483 board thread.
- [ ] 3.2 Human approval, merge, engine release and downstream deployment (not automatic).
