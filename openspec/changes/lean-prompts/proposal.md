# Lean prompts: right-size builtin system prompts & tool descriptions for modern models

## Why

Our builtin prompt surface was written for 2024-era models and inherited upstream
opencode's verbose house style. Measured today, a default `coder` session carries
**~75 KB (~19k tokens) of fixed overhead before the first user message**:

- ~30 KB of builtin tool descriptions (`internal/llm/tools/`), most following the
  legacy `WHEN TO USE / HOW TO USE / FEATURES / LIMITATIONS / TIPS` template. The
  `bash` description alone is ~9.7 KB and embeds step-by-step git-commit and
  PR-creation tutorials.
- 13.7 KB `coder` base prompt with nine Q/A verbosity examples and the "fewer than
  4 lines" rule stated three times (workhorse 5.4 KB, hivemind 4.9 KB largely
  mirror it).
- Up to ~30 KB of recursive project file tree (1000-file cap) injected into every
  primary-agent prompt via `getEnvironmentInfo()` — ~30 KB on this 749-file repo,
  re-sent on every request and multiplied across flow steps.

Anthropic's Claude 5 context-engineering guidance is explicit: they removed over
80% of Claude Code's system prompt for Claude 5-generation models with no measured
loss; examples in prompts now *constrain* rather than help; rigid rules should
become judgment-based guidance; tool usage instruction belongs in short tool
descriptions, not repeated in the system prompt. The reference sst/opencode
(`opencode-dax`) has already converged there: its live tool descriptions run
68–412 words (glob 89, write 108, edit 231) versus our 200–1,750, and its per-model
system prompts are ~100 lines. All models we route to (Claude Sonnet 4.5+/Opus,
Gemini 3.x via VertexAI/LiteLLM) are of that generation: the verbosity is pure
token cost, dilutes attention over the instructions that DO matter, and in places
actively misguides (e.g. tutorials drift out of sync with tool behavior).

## What Changes

- **Builtin tool descriptions** are rewritten as concise interface contracts:
  what the tool does, non-obvious parameter semantics, hard constraints/failure
  modes, and output-shape notes (truncation, temp-file spillover). Removed:
  workflow tutorials (git commit / PR rituals in `bash`), section-template
  boilerplate, quoting lessons, capability restatements of what the schema
  already encodes, and repeated cross-tool routing advice (stated once, in the
  `bash` description, as the canonical "prefer dedicated tools" note).
- **Builtin agent base prompts** (`coder`, `workhorse`, `hivemind`, `explorer`)
  are slimmed to identity, environment-specific conventions (CLI/markdown
  rendering, `file_path:line_number` references), engineering taste (minimal
  correct change, follow surrounding conventions), and safety policy (git
  discipline, action blast-radius, permission-denial semantics, prompt-injection
  flagging). Removed: Q/A example blocks, tripled verbosity rules, tool-routing
  lists duplicating tool descriptions, step-numbered task recipes.
- **Environment info** keeps `<env>` (cwd, git-repo flag, platform, date) and
  **drops the `<project>` recursive file-tree dump** — the model explores with
  `ls`/`glob` on demand (progressive disclosure).
- **Behavioral contract prompts are preserved**: `struct_output` gating prompt,
  the interactive chat-bridge prompt (+ reviewer details), and the
  background-tasks no-poll contract keep their semantics and test-pinned key
  phrases; the no-poll and cron/task-id appendices are only tightened in wording.
- **A size-budget regression test** pins every builtin tool description and base
  prompt under an explicit byte budget so the surface cannot silently re-bloat.

Not breaking: no `.opencode.json` surface, no runtime behavior change other than
prompt text; custom agent prompts (`info.Prompt`, markdown agents) are untouched.

## Capabilities

### New Capabilities

- `prompt-surface`: requirements for the assembled prompt surface — tool
  descriptions as bounded interface contracts, base prompts within budget,
  environment info without the project tree, preservation of the behavioral
  contract sections, and the anti-regression size guard.

### Modified Capabilities

None. `background-tasks`' "no-poll guidance delivered independent of the agent's
system prompt" requirement is untouched (the guidance stays, only tighter);
`structured-output` and `chat-bridge*` prompt requirements are preserved verbatim.

## Impact

**`github.com/obukhovaa/opencode`**

- `internal/llm/tools/*.go` — description constants rewritten (`bash`, `edit`,
  `read`, `write`, `glob`, `ls`, `grep` (light), `multiedit`, `patch`, `delete`,
  `view_image`, `webfetch`, `websearch`, `sourcegraph`, `lsp`, `skill`, `fetch`
  where applicable); parameter descriptions kept/trimmed in place.
- `internal/llm/prompt/coder.go`, `workhorse.go`, `hivemind.go`, `explorer.go` —
  base prompts rewritten; `summarizer.go`, `descriptor.go` unchanged.
- `internal/llm/prompt/prompt.go` — `getEnvironmentInfo()` loses the `ls` call +
  `<project>` block; `backgroundTasksPrompt` / `taskToolReportingPrompt` /
  `cronToolPrompt` / `parallelToolUsePrompt` tightened, gating logic untouched.
- `internal/llm/prompt/*_test.go` — assertions updated where pinned wording
  changed; new `prompt_budget_test.go` (or `tools/description_budget_test.go`)
  enforcing size budgets.
- Docs: none required (no config surface change; CLAUDE.md build/test flow
  unchanged). `opencode-schema.json`: untouched.
