# Validation

- Focused iteration serialization/lifecycle tests passed.
- Full `internal/api` package passed with race and coverage in the `make test` run.
- Generation, formatting and vet passed after placing the installed Go 1.25.8
  toolchain and mockgen on PATH. No generated artifact changed.
- Strict OpenSpec validation and `git diff --check` passed.
- No root CHANGELOG is maintained in this repository.

## Full gate is not green

The full `make test` run reported failures outside this change. Representative
failures reproduced in a detached, untouched worktree at base
`7c588c3a1323ccca322e703c3a4489d1263f3e33`:

- `internal/bridge/service`: full package fails provider validation in HTTP tests
  (`no valid provider available`). The single selected HTTP test passes alone.
- `internal/clitool`: `TestExec_TimeoutKillsProcessGroup` reports
  `context deadline exceeded` instead of the expected formatted output.
- `internal/flow`: `TestShellMarkup_CommandOutputIsNotSubstituted` times out after
  120 seconds. The full run also failed the adjacent template-marker test.

After these reproductions, the remaining slow full-suite processes were stopped.
This is not a complete or green full-suite result. Logs and baseline commands are
retained under `~/.c3-cos/codex-loop/cd5719-opencode-*.log` for the CoS handoff.

The repository hidden-character check also flags the unchanged generated
`internal/lsp/protocol/tsprotocol.go`; the changed Go files contain none.

Publication of this prerequisite is authorized by Arne via CoS message
`20261116T090430Z` on 2026-11-16. It does not implement Preview delivery.
