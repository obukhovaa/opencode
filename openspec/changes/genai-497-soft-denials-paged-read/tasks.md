## 1. Soft rule denials

- [x] 1.1 `internal/permission/denied.go`: `RuleDeniedError` (`Is` matches `ErrorPermissionDenied`), `DeniedByRule`, `AsRuleDenied`.
- [x] 1.2 Return `DeniedByRule(tool, input)` from every `case permission.ActionDeny:` branch (bash, write, edit ×3, multiedit, patch, delete ×2, webfetch, websearch, monitor, taskstop, CLI tools, MCP tools, `authorizeRead`); leave every rejected-prompt return as it is. Read-category tools match the denial with `errors.Is` and keep returning their text result.
- [x] 1.3 Agent loop, parallel and sequential paths: a rule denial records `ruleDeniedMessage`, does not cancel siblings, keeps the turn going; a rejected prompt is unchanged.
- [x] 1.4 Breaker on `callTracker`: 3 consecutive turns without progress (loop-detected / not-found calls are not progress) or 10 denied calls per run finish the message with `permission_denied`.
- [x] 1.5 Tests: soft deny continues the run, rejected prompt still ends it, parallel sibling survives, both breaker bounds, identical repeated denied call, `observeTurn` table, the CLI tool's agent deny is a rule denial.
- [x] 1.6 Docs: `docs/tool-permissions.md` (When a call is denied), `docs/cli-tools.md`.

## 2. Paged reads

- [x] 2.1 Apply `MaxReadSize` only to a whole-file read; the refusal names offset/limit and grep.
- [x] 2.2 Stream the window through `bufio.Reader`, cutting long lines as they arrive; cap returned text at `MaxReadSize`; report a past-EOF offset.
- [x] 2.3 Skip LSP notify/diagnostics for files over the ceiling.
- [x] 2.4 Correct the spill header (`tempdir.go`), the bash description and `README.md`.
- [x] 2.5 Flip the webfetch e2e: a whole read of the oversized spill is declined, a paged read works, the header names offset/limit.
- [x] 2.6 Tests: whole read declined, offset/limit/offset-only/oversized-limit pages, byte cap, 1MB single line, past-EOF offset, read-before-edit record, small file unchanged.

## 3. Overflow recovery

- [x] 3.1 Extract the in-loop compaction into `compactInLoop`.
- [x] 3.2 `isContextOverflowError` + `contextOverflowCause` (provider marker, or near the window).
- [x] 3.3 One compaction + one inline retry per run, gated on a provider stream error (`modelCallError`), `autoCompact`, the backoff and non-cancellation; original error on any failure.
- [x] 3.4 Tests: success after one compaction, overflow after a tool turn keeps the pair, overflow again, failed compaction, auto-compaction off, backoff, non-overflow error, an error after the tools ran is not retried, classifier table.
- [x] 3.5 Docs: `README.md` Auto Compact.

## 4. Title generation

- [x] 4.1 `titleWanted`: skip unattended sessions unless interactive.
- [x] 4.2 Tests: `titleWanted` table (attended, unattended, interactive step, linked subagent, no service) and a processGeneration run with no descriptor call.
- [x] 4.3 Docs: `README.md` built-in agents table, `docs/telemetry.md`.

## 5. Verification

- [x] 5.1 `go test ./internal/llm/agent/... ./internal/llm/tools/... ./internal/permission/... ./internal/config/...`, `go vet`, `gofmt -l`.
- [x] 5.2 `scripts/test/webfetch_output_limit.sh` (the e2e touched by 2.5).
- [ ] 5.3 `make test` and the full `make test-e2e` before merge.
