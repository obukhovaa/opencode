# Design: lean prompts

## Context

Two prompt surfaces reach the model on every request:

1. **System prompt** — assembled in `internal/llm/prompt/prompt.go`
   (`getAgentPromptInternal`): base agent prompt + conditional appendices
   (struct_output / interactive / parallel / background-tasks / task-id / cron /
   preloaded skills) + environment info (primary agents) + LSP note + context
   files. The `coder` base prompt is 13.7 KB; `getEnvironmentInfo()` embeds a
   recursive `ls` tree (1000-file cap, ~30 KB on this repo).
2. **Tool descriptions** — const strings in `internal/llm/tools/*.go` (plus the
   `task` tool in `internal/llm/agent/agent-tool.go`), passed verbatim to
   providers (`provider/anthropic.go:329` et al.). ~30 KB total for a
   full-toolset agent; the majority follow the legacy upstream
   `WHEN TO USE / HOW TO USE / FEATURES / LIMITATIONS / TIPS` template.

Constraints discovered during investigation:

- Tests pin phrases of the *appendix* prompts, not the base prompts:
  `prompt_background_tasks_test.go` (header `# Background tasks` + `` DO NOT use `sleep N` ``),
  `struct_output_gating_test.go` (sentinel `You MUST use the struct_output tool`),
  `prompt_interactive_peers_test.go` (reviewer-details wording).
- OpenSpec capabilities pin *semantics* of some tool descriptions:
  `tasklist-taskstop-tools` (must reference background primitives, must not
  recommend polling), `background-tasks` (no-poll guidance present for every
  tool-bearing agent), `cron-tool`, `monitor-tool`, `chat-bridge-agent-tool`
  (router_send description is rebuilt per registration from live config).
- The `task` tool description was already modernized (mirrors current
  Claude Code style) and `grep`/`patch`/`struct_output` are already lean.

## Goals / Non-Goals

**Goals:**

- Cut the fixed prompt overhead of a default coder session by well over half
  without dropping any *policy* (git discipline, blast-radius care, permission
  semantics, injection flagging) or any *behavioral contract* (struct_output
  gating, interactive bridge, no-poll).
- Make tool descriptions honest interface contracts for what OUR runtime
  actually does. Our `edit`/`multiedit`/`write` DO enforce read-before-edit
  (`getLastReadTime` checks in edit.go/multiedit.go/write.go), so the
  descriptions state it as a hard precondition; conversely `multiedit`
  rejects empty `old_string`, so it must not advertise `edit`'s create-file
  special case. Do not copy contract text from dax that our code doesn't
  enforce, and do not drop contract text it does enforce.
- Pin the result with a size-budget test so bloat cannot silently return.

**Non-Goals:**

- Per-provider / per-model-generation prompt variants (the `models.ModelProvider`
  parameter stays in the signatures, still unused).
- Changing tool schemas, parameters, permission gating, or any runtime behavior.
- Touching custom-agent prompts (`info.Prompt`, markdown agents), skills, or
  flow-step prompts.
- Rewriting the recently-engineered interactive/bridge prompt stack.

## Decisions

### D1. One lean prompt set for all models — no generation switch

All models this fork routes to (Claude Sonnet 4.5+/Opus 4.x+, Gemini 3.x, Kimi)
are "modern" per Anthropic's guidance. A per-generation switch (dax keys per
model *family*, not per generation) would double the maintenance surface to
serve models we no longer run. Alternative considered: keep verbose prompts for
non-Claude providers — rejected; Gemini 3.x-era models have the same property,
and dax's family-specific prompts differ in emphasis, not verbosity.

### D2. Tool descriptions become interface contracts; routing advice lives once

Format per tool: 1 sentence of purpose, then only the non-obvious operational
contract — parameter interactions, hard failure modes, output shape
(truncation → temp file + how to read more), concurrency/permission notes that
change agent behavior. No section headers, no restating the JSON schema, no
tutorials. Targets (from dax live equivalents): most tools 300–900 bytes;
`bash` ≤ ~3.5 KB (it keeps: workdir-not-cd, quoting-in-one-line, output
spillover contract, dedicated-tools routing list (the single canonical copy),
background mode pointer, compact git/PR policy — policy bullets, not the
step-by-step ceremony).

The git-commit/PR *tutorials* in `bash` (verification rituals, staged-file
analysis steps, PR body heredoc example) are deleted, kept as policy bullets:
no config edits, no destructive/skip-hooks flags, never force-push
main/master (warn if asked), no commit/push unless asked, the compact amend
protocol (never after a failed/hook-rejected commit, never on pushed or
foreign commits — fix and create a NEW commit), no `-i` flags, `gh` for
GitHub. Rationale: Claude 5-gen and Gemini 3 models perform these workflows
natively; step recipes drift and actively constrain (per Anthropic blog).
The policy bullets survive because they are *policy* — the model cannot
infer house rules it was never told.

Light rewrites keeping their format contracts: `patch` (drops the redundant
second Move-to example and the read/ls ritual) and `struct_output` (drops
the WHEN/HOW section headers).

Left alone (already lean, dynamic, or spec-pinned semantics): `grep`,
`question`, `router_send` (dynamic), `monitor`, `cron`, `tasklist`,
`taskstop`, `todowrite`, `websearch` (dynamic), `skill` (dynamic), `lsp`,
`bash` background-param texts, `task` (agent-tool.go — already modern).

### D3. Base prompts: keep policy, drop pedagogy

`coder` (13.7 KB → target ≤ 5.5 KB): keeps identity, memory-file pointer
(AGENTS.md/CLAUDE.md), CLI/markdown output conventions + `file:line` refs,
concision guidance stated once and judgment-based (short default, expand on
request), the URL anti-hallucination guard, the summarize-tool-output note,
conventions/minimal-change taste, action blast-radius + git discipline, task
flow essentials (verify with tests/lint), system notes (permission-denial
semantics, compaction, `!` prefix, injection flagging). Drops: the nine Q/A
examples (blog: examples constrain Claude 5-gen models), tool-routing
bullets (the routing list lives only in the `bash` description), duplicated
commit warnings (bash description owns git policy), step-numbered recipes.

`workhorse` (5.4 KB → ~3 KB): same structure minus TUI/interactivity; drops its
9-line tool-routing list. `hivemind` (4.9 KB → ~3 KB): delegation contract and
verification duty kept, wording tightened. `explorer` (2.0 KB → ~1.4 KB): keeps
thoroughness-level contract (quick/medium/very thorough — callers reference
it) and read-only constraints. `summarizer`/`descriptor`: unchanged.

Appendices in `prompt.go`: `backgroundTasksPrompt` tightened ~40% keeping the
test-pinned header and `` DO NOT use `sleep N` `` phrase and every tool name it
teaches; `taskToolReportingPrompt`, `cronToolPrompt`, `parallelToolUsePrompt`
tightened in place. `structuredOutputPrompt` and the interactive stack:
byte-for-byte untouched.

### D4. Drop the `<project>` tree from environment info

`getEnvironmentInfo()` keeps the `<env>` block (cwd, git flag, platform, date)
and stops calling the `ls` tool / emitting `<project>`. Rationale: up to ~30 KB
per prompt; neither Claude Code nor dax injects a tree; any file change breaks
provider prompt-caching for the whole system prompt; the synchronous `ls.Run`
at prompt-assembly adds latency; the model orients itself with one `ls` call
when it actually needs to. Alternative considered — top-level-only listing
(~1 KB): rejected as still cache-hostile and near-zero informational value.

### D5. Size budgets enforced in-package

`internal/llm/tools/description_budget_test.go` (package `tools`, so unexported
consts/funcs are reachable) asserts each statically defined builtin
description ≤ its budget: default 2,048 bytes, `bash` 4,096. Dynamic
descriptions (`skill`, `websearch`, `router_send`, `task`) are excluded —
their size scales with user config, not this repo's surface.
`internal/llm/prompt/prompt_budget_test.go` asserts `CoderPrompt()` ≤ 6,144,
`WorkhorsePrompt`/`HivemindPrompt` ≤ 4,096, `ExplorerPrompt` ≤ 2,048, that
`getEnvironmentInfo()` contains no `<project>` tag, and that the
background-tasks appendix keeps its header, the no-sleep instruction, and all
five background-primitive references. Budgets are ~15–25% above targets to
allow drift without churn.

## Risks / Trade-offs

- [Model behavior shifts on niche flows (e.g. commit-message style, PR body
  format) because recipes are gone] → policy bullets retain the guardrails;
  e2e scripts (`make test-e2e`) and existing unit tests cover runtime
  contracts; anything style-level is recoverable per-project via AGENTS.md.
- [Weaker models configured by users underperform with lean prompts] → the fork
  only registers modern models; users with custom needs can override any agent
  prompt via `info.Prompt`/markdown agents (unchanged mechanism).
- [Losing the project tree costs the model one orientation `ls` call] → that
  call is cheaper than paying the tree on every request, and restores prompt
  cacheability.
- [Some removed sentence was load-bearing for an existing test] → run full
  `make test`; the three prompt test files' pinned phrases are explicitly kept
  or updated in the same commit.

## Open Questions

None blocking. Follow-up candidate (out of scope): make `getEnvironmentInfo`
results cacheable per-session rather than recomputed per prompt build.
