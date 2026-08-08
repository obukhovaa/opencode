# Tasks: lean-prompts

## 1. Tool descriptions (`internal/llm/tools/`)

- [x] 1.1 Rewrite `bash` description: keep workdir-over-cd, one-line quoting note, output-spillover contract, canonical dedicated-tools routing list, background-mode pointer, git/GitHub policy bullets; delete commit/PR walkthroughs, quoting examples, directory-verification ritual, repeated parallelism boilerplate
- [x] 1.2 Rewrite legacy-template descriptions to interface contracts: `read`, `write`, `edit`, `multiedit`, `glob`, `ls`, `delete`, `view_image`, `webfetch`, `websearch`, `sourcegraph`, `lsp`, `skill`, `patch` (light), `fetch` if present
- [x] 1.3 Verify untouched tools stay contract-compliant: `grep`, `question`, `struct_output`, `router_send`, `monitor`, `cron`, `tasklist`, `taskstop`, `todowrite`; trim only obvious duplication
- [x] 1.4 Trim `task` tool description boilerplate in `internal/llm/agent/agent-tool.go` if duplicated guidance found (keep structure — already modern)
- [x] 1.5 Add `description_budget_test.go` in package `tools` (default 2,048 bytes; `bash` 4,096) covering every builtin description

## 2. Base prompts (`internal/llm/prompt/`)

- [x] 2.1 Rewrite `coder.go` per design D3 (target ≤ 5.5 KB, budget 6,144): drop Q/A examples, tripled verbosity rule, tool-routing list; keep policy anchors (memory files, permission semantics, compaction, `!` prefix, injection flagging, git discipline, minimal-change taste)
- [x] 2.2 Rewrite `workhorse.go` (≤ 4,096): drop tool-routing list, tighten sections
- [x] 2.3 Rewrite `hivemind.go` (≤ 4,096): keep delegation + verification contract, tighten
- [x] 2.4 Tighten `explorer.go` (≤ 2,048): keep thoroughness-level contract and read-only constraints
- [x] 2.5 Tighten appendices in `prompt.go`: `backgroundTasksPrompt` (keep pinned header, `` DO NOT use `sleep N` ``, all five primitive names), `taskToolReportingPrompt`, `cronToolPrompt`, `parallelToolUsePrompt`; leave `structuredOutputPrompt` + interactive stack byte-identical
- [x] 2.6 Drop `<project>` tree from `getEnvironmentInfo()`: remove `ls.Run` call, keep `<env>` block
- [x] 2.7 Add `prompt_budget_test.go`: base-prompt budgets + assert env info has no `<project>` tag

## 3. Verification

- [x] 3.1 `go test ./internal/llm/...` green; fix any assertion still pinning removed wording
- [x] 3.2 `make test` green (fmt, vet, full suite)
- [x] 3.3 Record before/after byte counts per surface in the PR description
