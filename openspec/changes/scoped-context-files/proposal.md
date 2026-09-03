## Why

Context files (`AGENTS.md`, `CLAUDE.md`, everything in `contextPaths`) are loaded
exactly once per process by a package-level `sync.Once` (`prompt.go:578-594`) and
appended verbatim to the end of **every** agent's system prompt. There is no
per-agent, per-flow-step, per-session, or environment variation. This has two
consequences:

1. **One workspace, two audiences, one file.** The canonical motivating case:
   `piano/agents/scenario-builder` runs opencode for two disjoint activities —
   *authoring* the workspace (designing flows, skills, agent types; "circuit 2") and
   *executing* the authored flows at runtime ("circuit 3"). Its root `AGENTS.md` is
   ~6 k tokens and ~85-90 % circuit-2 authoring material (three-circuits model, agent
   roster, build-vs-run wiring rules, GitLab identity conventions). That content is
   dead weight for a runtime `scenario-runner` flow step — the agent already carries
   its runtime context in its own 272-line type definition. The only workaround is
   prose ("if you are running a flow, ignore sections 1-3"), which burns the tokens
   anyway and is unreliable. A `scenario-runner` step today inherits ~5.5 k tokens of
   irrelevant authoring instructions on every turn.

2. **No progressive disclosure for nested context files.** `contextPaths` entries are
   joined to `workDir` only; trailing-slash directory entries recurse and inline
   everything unconditionally. A repo that wants per-subsystem instructions has two bad
   options: merge everything into the root file (context bloat for every agent) or put
   them in subdirectories where no agent discovers them. Claude Code's two-phase model
   — ancestor/root files at launch, subtree files injected only on first directory
   touch — has no equivalent here.

## What Changes

- **Scoped context resolution.** Context path resolution becomes a function of (global
  config, agent, flow step), with precedence `step > agent > global default`. Agents
  and flow steps declare an optional `context: { paths, mode, nested }` object.
  `mode: replace` (default when declared) substitutes; `mode: append` concatenates.
  A limited, shell-free template syntax (`${agent}`, `${flow.id}`, `${flow.step}`,
  `${env.VAR}`) is supported in path entries. Resolved content is memoized per
  resolution key (not globally), so it is byte-identical within a session.

- **Progressive disclosure of nested context files.** Discover context files strictly
  below `workDir` in one walk per process, inject only a compact manifest (one line
  per file) into the system prompt, and inject a file's body as a `<system-reminder>`
  appended to the first tool result that touches its owning directory — never by
  mutating the system prompt. Per-session activation state; subagent sessions are
  isolated from the parent.

- **New leaf package `internal/contextfile`.** Context discovery, resolution, reading,
  memoization, and templating move out of `internal/llm/prompt` into a new package
  that both `prompt` and `tools` can import without creating a cycle.

- **`contextDiscovery` top-level config block.** `enabled` (default true), `maxFiles`,
  `maxDepth`, `maxFileBytes`, `maxSessionBytes`. Agent/step `context.nested: false`
  opts a single agent or step out of discovery injection.

- **`contextPaths` is untouched.** Backward-compatible: agents without a `context`
  declaration and a config without `contextDiscovery` produce a byte-identical prompt.

## Capabilities

### New Capabilities

- `context-resolution`: scoped context path resolution at agent and flow-step
  granularity — config surface (`context: { paths, mode, nested }` on agents and
  steps), shell-free templating with workDir containment, keyed memoization,
  `replace`/`append` semantics and precedence, and backward-compatibility guarantees.

- `progressive-context-disclosure`: per-process nested-file discovery walk, manifest
  block in the system prompt, `contextDisclosureWrapper` tool-result injection, trigger
  tool set, per-session activation state and byte budget, caps, failure isolation, and
  opt-out.

### Modified Capabilities

- `prompt-surface`: the `# Project-Specific Context` block now renders the resolved
  set (from `internal/contextfile`) rather than a process-global string; the new
  manifest section and its exemption from static prompt-budget assertions are stated.

- `flow-api`: the `context` step field is added to the step schema, is inheritable via
  `extends`, and is explicitly absent from `nonInheritableStepKeys`.

## Impact

**`github.com/opencode-ai/opencode`**

- **NEW** `internal/contextfile/`: resolver, keyed memoization (`sync.Map` over
  resolution-key digest), shell-free templating + containment check, path dedupe
  (reusing `EvalSymlinks`+lowercase canon from `processContextPaths`), discovery walk,
  manifest rendering. Absorbs `processContextPaths` / `processFile` / `tryMarkProcessed`
  from `internal/llm/prompt/prompt.go` (L602-688).
- `internal/config/config.go`: `Agent.Context *AgentContext`, new top-level
  `ContextDiscovery *ContextDiscoveryConfig`; `cmd/schema/main.go` + regenerated
  `opencode-schema.json`.
- `internal/agent/registry.go`: `AgentInfo.Context` (yaml frontmatter), merge in
  `applyConfigOverrides` (L441-483 pattern) and `mergeMarkdownIntoExisting`
  (L499-557 pattern).
- `internal/flow/flow.go`: `Step.Context *StepContext` (yaml-tagged, inheritable).
- `internal/flow/service.go`: read `step.Context` in `runStep` (L396-440 area) and
  pass into `NewAgent`; resolution uses the flow-context values already set at
  L558-561 (`FlowIDContextKey`, `FlowStepIDContextKey`).
- `internal/llm/prompt/prompt.go`: `getContextFromPaths()` + `sync.Once` (L578-594)
  replaced by a call to `contextfile.Resolve(agentID, stepContext)`;
  `processContextPaths`/`processFile` removed (moved to `internal/contextfile`);
  manifest rendering added; deferred-tools `<system-reminder>` budget tests updated.
- `internal/llm/agent/tools.go`: `contextDisclosureWrapper` wrapping trigger tools
  when discovery found ≥1 nested file; per-session activation map.
- `internal/llm/tools/tools.go`: trigger-tool parameter extraction helpers (per-tool
  path derivation for `read`, `write`, `edit`, `patch`, `grep`, `glob`, `ls`).
- `internal/llm/prompt/prompt_test.go`: new backward-compat scenario; budget tests
  updated to acknowledge the dynamic manifest block.
- Docs: `CLAUDE.md` agent-fields entry for `context`, `docs/context.md` (new),
  `docs/flows.md` step-field entry; `flow-creator` skill reference.
- E2E: `scripts/test/scoped_context.sh` asserting manifest presence, first-touch
  body injection, second-touch deduplication, and byte-identical output for agents
  without `context` config.
