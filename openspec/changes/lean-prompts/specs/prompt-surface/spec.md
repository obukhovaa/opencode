# prompt-surface

Governs the fixed prompt payload OpenCode ships to the model on every request:
builtin agent base prompts, the conditional prompt appendices, environment
info, and builtin tool descriptions. The intent is a lean surface tuned for
modern (Claude 4.5+/5-generation, Gemini 3.x) models: interface contracts and
policy, not tutorials, examples, or restated schemas.

## ADDED Requirements

### Requirement: Builtin tool descriptions are bounded interface contracts

Every builtin tool description registered via `ToolInfo.Description` SHALL
state the tool's purpose and only the non-obvious operational contract
(parameter interactions, hard failure modes, output truncation/spillover
behavior, concurrency or permission notes that change agent behavior). Tool
descriptions MUST NOT contain multi-step workflow tutorials, Q/A or
good/bad-example blocks, section-template boilerplate (`WHEN TO USE`,
`FEATURES`, `LIMITATIONS`, `TIPS`), or restatements of what the parameter
schema already encodes. Descriptions MUST describe only behavior the runtime
actually implements.

Each builtin tool description SHALL fit within an explicit byte budget
enforced by a unit test: 4,096 bytes for `bash`, 2,048 bytes for every other
builtin tool. The cross-tool routing guidance ("prefer dedicated tools over
shell equivalents") SHALL appear in exactly one place: the `bash` tool
description.

#### Scenario: Description budgets are test-enforced

- **WHEN** `go test ./internal/llm/tools` runs
- **THEN** a budget test iterates every builtin tool description constant
- **AND** fails naming the offending tool if any description exceeds its byte budget

#### Scenario: Bash description carries policy, not ceremony

- **WHEN** the `bash` tool description is rendered
- **THEN** it contains the output-spillover contract (truncated output persisted to a temp file readable via `read`/`grep`), the `workdir`-over-`cd` instruction, the background-mode pointer, the dedicated-tools routing list, and git/GitHub policy bullets (no commits/pushes unless asked, no destructive or hook-skipping flags, no `-i` flags, `gh` for GitHub operations)
- **AND** it does not contain step-numbered commit or pull-request walkthroughs

#### Scenario: Descriptions match runtime behavior

- **WHEN** a builtin tool description documents a precondition or failure mode
- **THEN** the described behavior is one the tool's `Run` implementation actually enforces

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

#### Scenario: Base prompt budgets are test-enforced

- **WHEN** `go test ./internal/llm/prompt` runs
- **THEN** a budget test asserts each builtin base prompt is within its byte budget

#### Scenario: Coder prompt keeps policy anchors

- **WHEN** `CoderPrompt()` is rendered
- **THEN** it still instructs on memory files, permission-denial handling, conversation compaction, the `!` command prefix, prompt-injection flagging, and never committing without an explicit user request

### Requirement: Environment info excludes the project file tree

`getEnvironmentInfo()` SHALL emit the `<env>` block (working directory,
git-repo flag, platform, date) and SHALL NOT execute the `ls` tool or embed a
`<project>` file-tree listing. Agents obtain project structure on demand via
`ls`/`glob`.

#### Scenario: No project tree in assembled prompts

- **WHEN** a system prompt is assembled for a primary (`mode=agent`) agent
- **THEN** it contains the `<env>` block
- **AND** it contains no `<project>` tag and no recursive file listing

### Requirement: Behavioral contract prompts survive slimming

The prompt appendices that encode runtime contracts SHALL be preserved through
any prompt-surface reduction: the structured-output gating prompt
(`struct_output` sentinel), the interactive chat-bridge prompt including
reviewer details, and the background-tasks no-poll contract (its `# Background
tasks` header, the `` DO NOT use `sleep N` `` instruction, and mentions of
`run_in_background`, `task` async mode, `monitor`, `tasklist`, `taskstop`).
Appendix gating logic (which agents receive which appendix) MUST NOT change.

#### Scenario: No-poll contract intact after slimming

- **WHEN** the system prompt is assembled for any agent with tool access
- **THEN** the background-tasks appendix is present with its header, the no-sleep instruction, and all five background-primitive tool references

#### Scenario: Interactive and struct_output prompts unchanged

- **WHEN** an interactive flow step or a schema-bearing step assembles its prompt
- **THEN** the interactive prompt (with reviewer details) and the structured-output gating prompt are appended exactly as before the reduction
