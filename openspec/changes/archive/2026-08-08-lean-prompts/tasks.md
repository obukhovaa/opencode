# Tasks: lean-prompts

## 1. Tool descriptions (`internal/llm/tools/`)

- [x] 1.1 Rewrite `bash` description: keep workdir-over-cd, one-line quoting note, output-spillover contract, canonical dedicated-tools routing list, background-mode pointer, git/GitHub policy bullets; delete commit/PR walkthroughs, quoting examples, directory-verification ritual, repeated parallelism boilerplate
- [x] 1.2 Rewrite legacy-template descriptions to interface contracts: `read`, `write`, `edit`, `multiedit`, `glob`, `ls`, `delete`, `view_image`, `webfetch`, `sourcegraph`, `patch` (light), `struct_output` (light) — `grep`/`websearch`/`lsp`/`skill` inspected and left as-is (already lean or dynamic)
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

## 4. Review fixes (max-effort review round on PR #24)

- [x] 4.1 Fix contract drift: edit empty-new_string semantics, multiedit no-create carve-out, bash spillover guidance vs read's 250KB cap
- [x] 4.2 Restore over-trimmed policy: amend protocol + never-force-push-main/master (bash), webfetch-over-curl routing entry (bash), URL anti-hallucination guard + summarize-tool-output note (coder), colon-before-tool-call rule (hivemind)
- [x] 4.3 Remove coder tool-routing bullets duplicating tool descriptions; spec now scopes the routing-list single-home rule and allows per-tool shell-equivalent preferences
- [x] 4.4 Scope byte budgets to statically defined descriptions (spec + design); add five-primitives pin test; make budget test config-self-sufficient; fix test import grouping
- [x] 4.5 Correct design.md read-before-edit statement and touched/left-alone inventory; align proposal.md Impact and task 1.2 with the shipped diff

## 5. Review round 2 fixes (high-effort re-review)

- [x] 5.1 Restore hook-modified-files amend exception and the warn-on-explicit-secrets-commit clause (bash policy)
- [x] 5.2 Scope edit's read-first precondition to exclude the create-new-file path
- [x] 5.3 Restore "likely to succeed" guard in parallelToolUsePrompt; restore explorer's no-emoji rule
- [x] 5.4 Keep batching advice only in the gated parallelToolUsePrompt appendix (dropped from read/glob/bash descriptions)
- [x] 5.5 Interpolate read-cap/preview constants into the bash description via the existing replacer (no hardcoded 250KB/500)
- [x] 5.6 Move package config-loading init to testsetup_test.go; drop the dead guard; cross-link the budget-test inventory with NewToolSet's createTool switch
- [x] 5.7 Fix garbled design.md sentence
