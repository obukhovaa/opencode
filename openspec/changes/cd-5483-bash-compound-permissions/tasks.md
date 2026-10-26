## 1. Regression and gate
- [x] 1.1 Reproduce the old prefix-glob bypass using a harmless local marker fixture.
- [x] 1.2 Add a syntax-aware evaluator and apply it before all bash dispatch modes.
- [x] 1.3 Cover every ticket separator/substitution, literals, malformed syntax and verdict precedence.
- [x] 1.4 Verify foreground/background denial occurs before the allowed prefix runs.

## 2. Contract and checks
- [x] 2.1 Document scope, compatibility and parser limitations.
- [x] 2.2 Run focused race tests, final make test and deterministic hook e2e (9 checks); all pass with LC_ALL=C.
- [x] 2.4 ~~Provider-backed release e2e~~ not applicable: no provider or agent-loop code changes (review, 2026-10-26).
- [x] 2.3 Validate the OpenSpec change and inspect the final diff.

## 3. Review follow-up (2026-10-26)
- [x] 3.3 Blocking: refuse hidden execution (arithmetic/subscript/indirection over a variable, `@P`, assigning expansions) under prefix rules; the four reviewed exploit lines are tests and fire under bash 5.3 without the fix.
- [x] 3.4 CD-5487: safe list runs no other command (wrappers removed, `env` bare only, program-running options refused); `env bash -c` denied under `"*": deny`.
- [x] 3.5 Pin the reviewed here-string, env-prefix, `eval` and `bash -c` forms as tests.
- [x] 3.6 Document parse-failure denial under allow-all, redirects, and bash-only soundness of allow rules (zsh).

## 4. Review and delivery
- [x] 4.1 Open a review PR and reply in the original CD-5483 board thread.
- [x] 4.2 Merge and engine release; downstream deployment left to the c2-agent owner.
