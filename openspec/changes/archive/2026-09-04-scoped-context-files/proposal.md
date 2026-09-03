## Why

Context files (`AGENTS.md`, `CLAUDE.md`, everything in `contextPaths`) are loaded
exactly once per process by a package-level `sync.Once` (`prompt.go:593-611`) and
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

## Non-Goals

- **`bash` is not a disclosure trigger.** Scanning command strings for path tokens
  produces too many false positives and false negatives; the explicit path-taking
  tools cover the working-in-a-subtree signal.
- **No shell markup in context files.** `` !`cmd` `` expansion is explicitly absent
  from context path entries and context file bodies — executing shell at prompt-build
  time with no permission prompt is the exact hazard to avoid.
- **No environment-variable export.** `${env.VAR}` reads a value into a path entry;
  nothing is exported into the agent's environment and no shell interpretation occurs.
- **No ancestor walk-up above `workDir`.** Discovery is strictly downward; parent
  directories are never probed.
- **No file watching or hot reload.** Editing a context file still requires a restart,
  exactly as today.
- **No `.gitignore` honoring in the discovery walk (v1).** There is no gitignore
  library in `go.mod` and ripgrep is an external binary unsuitable for the
  prompt-build path; the walk uses the hardcoded skip set (see design.md D6) and the
  caps bound over-discovery.

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
  from `internal/llm/prompt/prompt.go` (L617-703).
- `internal/config/config.go`: `Agent.Context *contextfile.AgentContext`, new
  top-level `ContextDiscovery *contextfile.DiscoveryConfig` (both types defined in
  `internal/contextfile`; `config` imports the leaf, never the reverse);
  `cmd/schema/main.go` + regenerated `opencode-schema.json`.
- `internal/agent/registry.go`: `AgentInfo.Context *contextfile.AgentContext` (yaml
  frontmatter), merge in `applyConfigOverrides` (registry.go:418-516; `DeferredTools`
  block at 495-501) and `mergeMarkdownIntoExisting` (registry.go:531-603;
  `DeferredTools` block at 578-583).
- `internal/flow/flow.go`: `Step.Context *contextfile.StepContext` (yaml-tagged,
  inheritable).
- `internal/flow/service.go`: read `step.Context` in `runStep` (service.go:461) and
  pass into `NewAgent` (call at service.go:513); `${flow.id}` / `${flow.step}` are
  populated explicitly from `f.ID` and `step.ID` at that call site — `NewAgent` is
  context-free (factory.go:207-208 discards its ctx), so the `FlowIDContextKey` /
  `FlowStepIDContextKey` ctx values (set later at service.go:654-656 for Run-time
  telemetry) are not the token source.
- `internal/llm/prompt/prompt.go`: `getContextFromPaths()` + `sync.Once` (L593-611)
  replaced by a call to `contextfile.Resolve(agentID, stepContext)`;
  `processContextPaths`/`processFile` removed (moved to `internal/contextfile`);
  manifest rendering added.
- `internal/llm/agent/tools.go`: `contextDisclosureWrapper` wrapping trigger tools
  when discovery found ≥1 nested file; one shared per-toolset disclosure-state object
  (per-session activation + byte budget), mirroring the shared `deferSeq` pattern.
- `internal/llm/tools/tools.go`: extend the existing `ExtractPathsFromCall`
  (tools.go:167) for trigger-tool path derivation (`read`, `write`, `edit`,
  `multiedit`, `patch`, `grep`, `glob`, `ls`); `patch` paths are parsed from
  `patch_text` section headers, `glob` adds the pattern's literal directory prefix,
  and `grep` without `path` activates nothing.
- `internal/llm/prompt/prompt_test.go`: new backward-compat scenario. No budget-test
  change: `TestBasePromptBudgets` (`prompt_budget_test.go`) measures only the base
  prompt constructors, so the manifest is exempt by construction.
- Docs: `CLAUDE.md` agent-fields entry for `context`, `docs/context.md` (new),
  `docs/flows.md` step-field entry; `flow-creator` skill reference.
- E2E: `scripts/test/scoped_context.sh` asserting manifest presence, first-touch
  body injection, second-touch deduplication, and byte-identical output for agents
  without `context` config.
