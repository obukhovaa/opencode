# prompt-surface (delta)

Delta spec for the `scoped-context-files` change. Restates only the requirements
that change; unchanged requirements are not repeated here. For the full specification
see `openspec/specs/prompt-surface/spec.md`.

## MODIFIED Requirements

### Requirement: Builtin base prompts are lean and within budget

The builtin base prompts (`coder`, `workhorse`, `hivemind`, `explorer`) SHALL
retain: agent identity, memory-file guidance (AGENTS.md/CLAUDE.md), output
conventions for their surface (CLI markdown, `file_path:line_number`
references), engineering conventions (minimal correct change, follow
surrounding code), action blast-radius care and git discipline, permission
denial semantics, and prompt-injection flagging. They MUST NOT contain Q/A
example dialogues, the same rule stated more than once, tool-routing lists
duplicating tool descriptions, or step-numbered task recipes.

Byte budgets enforced by unit test: `coder` ≤ 6,144; `workhorse` ≤ 4,096;
`hivemind` ≤ 4,096; `explorer` ≤ 2,048. The `explorer` prompt SHALL keep the
caller-facing thoroughness contract (`quick` / `medium` / `very thorough`).

Addendum for scoped context resolution and progressive disclosure:

The `# Project-Specific Context` section of the assembled system prompt now renders
the **scoped-resolved** context block returned by `internal/contextfile.Resolve()`
rather than the process-global string from `getContextFromPaths()`. The content and
format of the rendered block are identical when no agent or step override is in effect
(see `context-resolution` spec, "No config yields byte-identical behavior" scenario).

When `contextDiscovery.enabled` is true and ≥1 nested file was discovered, the system
prompt additionally contains a **manifest section** (see `progressive-context-disclosure`
spec, "Manifest block" requirement). The manifest section is dynamic: its content and
byte count depend on the discovery walk result and the current config, and may be
empty (zero-byte delta) for repos with no nested context files. The manifest CANNOT
cause the byte-budget test to fail: `TestBasePromptBudgets`
(`internal/llm/prompt/prompt_budget_test.go:17-36`) measures only the base prompt
constructors (`CoderPrompt()`, `WorkhorsePrompt(...)`, `HivemindPrompt(...)`,
`ExplorerPrompt(...)`), never the assembled prompt, so the manifest — like every other
appendix, including the deferred-tools `<system-reminder>` block — is exempt by
construction. No budget-test change is required; an acknowledging comment in
`prompt_budget_test.go` is optional documentation. Only NEW tests that assert on full
assembled prompts (e.g. the byte-identical backward-compat test) need to guard
discovery off when the test workspace could contain nested context files.

#### Scenario: Base prompt budgets are test-enforced

- **WHEN** `go test ./internal/llm/prompt` runs
- **THEN** a budget test asserts each builtin base prompt is within its byte budget

#### Scenario: Coder prompt keeps policy anchors

- **WHEN** `CoderPrompt()` is rendered
- **THEN** it still instructs on memory files, permission-denial handling, conversation compaction, the `!` command prefix, prompt-injection flagging, and never committing without an explicit user request

#### Scenario: Budget tests remain green with discovery enabled

- **WHEN** `go test ./internal/llm/prompt` runs in a workspace that happens to contain
  nested context files
- **THEN** the budget tests for `coder`, `workhorse`, `hivemind`, and `explorer` pass
  because `TestBasePromptBudgets` measures only the base prompt constructors, never
  the assembled prompt — the discovery-dependent manifest section is exempt by
  construction

#### Scenario: No-nested-files workspace yields byte-identical prompt

- **WHEN** a workspace has no files strictly below `workDir` matching any `contextPaths`
  basename (i.e. the discovery walk finds nothing)
- **THEN** the assembled system prompt is byte-identical to the pre-feature prompt for
  the same agent and `contextPaths` config, with no manifest section added
