# Tasks

## 1. Repetition detection
- [x] 1.1 Track completed results and enforce repeated-call protection; verify changed-result, error-status and forced-tool tests.
- [x] 1.2 Reset repetition history at both successful inbox delivery boundaries; verify repeated questions, chat exchanges and failed-drain tests.
- [x] 1.3 Document the result-aware threshold and delivery reset in configuration and permission docs; check against the implemented behavior.

## 2. Integration checks
- [x] 2.1 Run agent package tests, make test, hidden-character check and strict OpenSpec validation; record outcomes in the PR.

## 3. Review fixes
- [x] 3.1 Exclude execution metadata from result identity; verify changing-timestamp regression.
- [x] 3.2 Treat successful question answers as delivered input; verify identical-answer regression and retained dismissal protection.

## Workflow follow-up
- CoS arranges review and release; archive after merge.
