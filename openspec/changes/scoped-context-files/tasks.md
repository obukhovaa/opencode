# Tasks: scoped-context-files

## 1. `internal/contextfile` package

- [x] 1.1 Create `internal/contextfile/` package — a true leaf importing only the
  stdlib and `internal/logging` (never `internal/config`); move `processContextPaths`,
  `processFile`, and `tryMarkProcessed` from `internal/llm/prompt/prompt.go`
  (L617-703) into `contextfile/resolver.go`; preserve the `# From:<abs-path>\n<body>`,
  sort-by-absolute-path, silent-skip-on-missing, and EvalSymlinks+lowercase-dedup
  behaviors exactly

- [x] 1.2 Implement `Resolve(paths []string, workDir string, mode Mode) string` with
  keyed memoization (`sync.Map` + `singleflight.Group`); key = SHA-256 of sorted
  absolute path list + mode string; add `Mode` type with `ModeReplace` / `ModeAppend`
  constants and the warn-on-unknown fallback-to-append logic

- [x] 1.3 Implement the three-layer merge in `ResolveForAgent(globalPaths []string,
  agentCtx *AgentContext, stepCtx *StepContext, workDir string) string` applying
  precedence `step > agent > global` with `replace`/`append` semantics and
  cross-layer deduplication via the existing `tryMarkProcessed` logic; define the
  `AgentContext`, `StepContext`, and `DiscoveryConfig` types HERE — `config`, the
  agent registry, and `flow` reference them as `contextfile.*`, so the import edge
  always points at the leaf

- [x] 1.4 Implement shell-free templating (`expandTokens(entry string, vars
  TemplateVars) (string, bool)`) for `${agent}`, `${flow.id}`, `${flow.step}`,
  `${env.VAR}`; skip-on-unknown-token (DEBUG log) and skip-on-empty-segment (DEBUG
  log); workDir containment check (WARN log + reject) applied after template expansion
  and `filepath.Join(workDir, entry)` + `filepath.Clean`

- [x] 1.5 Port and extend `internal/llm/prompt/prompt_test.go` context-related test
  cases into `internal/contextfile/resolver_test.go`; add new table-driven cases:
  replace vs append modes; three-layer merge with dedup; unknown-token skip; env var
  expansion; path traversal rejection; viper round-trip for `AgentContext` map key
  case-folding (per CLAUDE.md `TestConfig_HooksViperRoundTripLowercasesEventKeys`
  pattern); byte-stability / two-call pointer equality

## 2. Discovery walk and manifest

- [x] 2.1 Implement `Discover(workDir string, globalPaths []string, cfg
  DiscoveryConfig) DiscoveryResult` in `contextfile/discovery.go`; extract basename
  set from file-type (non-trailing-slash) entries of `globalPaths`; walk subtree with
  `filepath.WalkDir` using a hardcoded skip set mirroring `internal/llm/tools/ls.go`
  `shouldSkip()` (hidden-dot prefix, `.git`, `node_modules`, `vendor`,
  `dist`/`build`/`target` etc.) plus the configured data directory — `.gitignore`
  honoring is a deliberate non-goal for v1 (no gitignore library in `go.mod`; ripgrep
  is an external binary unsuitable for the prompt-build path); the caps bound
  over-discovery; apply `maxDepth`, `maxFiles`
  caps; cache result by `workDir` in a `sync.Map`; files at depth 0 (root) are
  excluded from the result set

- [x] 2.2 Implement `RenderManifest(discovered []string, workDir string, cfg
  ManifestConfig) string` in `contextfile/manifest.go`: relative-path line per file;
  label from YAML frontmatter `description` else first markdown heading (truncated ~120
  chars) else path only; total-byte overflow degrades to paths-only then trailing
  "... N more"; absent (empty string) when `len(discovered) == 0`

- [x] 2.3 Add unit tests for `Discover`: finds AGENTS.md in subdirectory but not at
  root; skips `.git` / `node_modules` / hidden-dot subtrees via the hardcoded skip
  set (`.gitignore` is not consulted in v1); respects maxDepth and maxFiles; cached
  on second call (no re-walk); `enabled: false` returns empty result

- [x] 2.4 Add unit tests for `RenderManifest`: present when files found; absent when
  none; label extraction from frontmatter vs heading; overflow degradation; byte-stable
  on repeated calls with same inputs

## 3. Config surface

- [x] 3.1 Reference the `contextfile` types from `internal/config/config.go`: add
  `Agent.Context *contextfile.AgentContext` (`Paths []string`, `Mode string`,
  `Nested *bool`) and `Config.ContextDiscovery *contextfile.DiscoveryConfig`
  (`Enabled bool`, `MaxFiles int`, `MaxDepth int`, `MaxFileBytes int`,
  `MaxSessionBytes int`) — both types are defined in `internal/contextfile` (task
  1.3), so `config` imports the leaf, never the reverse; set defaults in
  `setDefaults()` (config.go:718, next to the `contextPaths` default at :720) via
  `viper.SetDefault("contextDiscovery.enabled", true)` etc.: `enabled: true`,
  `maxFiles: 100`, `maxDepth: 8`, `maxFileBytes: 32768`, `maxSessionBytes: 131072`
  — note `viper.SetDefault` makes `Config.ContextDiscovery` always non-nil after
  `Unmarshal`, which is desired

- [x] 3.2 Add `AgentInfo.Context *contextfile.AgentContext` to
  `internal/agent/registry.go` (yaml frontmatter parses automatically); add merge in
  `applyConfigOverrides` (registry.go:418-516; mirror the `DeferredTools` `maps.Copy`
  block at 495-501) and in `mergeMarkdownIntoExisting` (registry.go:531-603; mirror
  the 578-583 block)

- [x] 3.3 Add `Step.Context *contextfile.StepContext` (yaml tag
  `context,omitempty`) to `internal/flow/flow.go` — the `StepContext` struct
  (`Paths []string`, `Mode string`, `Nested *bool`) is defined in
  `internal/contextfile` (task 1.3); no entry in `nonInheritableStepKeys` — confirm this by
  reviewing `include.go:182-187` and adding a comment noting it is intentionally absent

- [x] 3.4 Declare the `context` field shape in `cmd/schema/main.go` by adding it to
  `agentSchema` `additionalProperties.properties` — that one map is shared by
  `agents.*` and `#/definitions/agent`, so one edit covers both surfaces; declare
  `contextDiscovery` at the top-level schema; regenerate `opencode-schema.json` via
  `go run cmd/schema/main.go > opencode-schema.json`

- [x] 3.5 Add viper round-trip unit test in `internal/config/` for `Agent.Context`:
  a config with an agent key containing uppercase letters (e.g., `"MyAgent"`) bearing
  a `context` object survives `viper.Unmarshal`, with the key lowercased and the
  `Context.Paths`, `Context.Mode`, and `Context.Nested` fields intact

## 4. Prompt integration

- [x] 4.1 Replace `getContextFromPaths()` + `sync.Once` (prompt.go:593-611) with a
  call to `contextfile.ResolveForAgent(cfg.ContextPaths, agentInfo.Context,
  nil /*stepCtx*/, cfg.WorkingDir)` in `getAgentPromptInternal`; remove
  `processContextPaths`, `processFile`, `tryMarkProcessed` from `prompt.go` (they now
  live in `internal/contextfile`)

- [x] 4.2 Add manifest rendering call in `getAgentPromptInternal` after the context
  block: `contextfile.RenderManifest(discovered, workDir, discoveryConfig)` appended
  to the system prompt when non-empty; guard on `info.Context.nested != false` (opt-out)

- [x] 4.3 Update `internal/llm/prompt/prompt_test.go`: update any test that assumed the
  `sync.Once` global or the old `processContextPaths` signature (they port to
  `internal/contextfile` per task 1.5); add the explicit backward-compat scenario
  ("no agent/step context config ⇒ byte-identical prompt"). No budget-test change is
  required: `TestBasePromptBudgets` (`prompt_budget_test.go:17-36`) measures only the
  base prompt constructors, so the manifest is exempt by construction — an
  acknowledging comment there is optional

## 5. Flow integration

- [x] 5.1 In `internal/flow/service.go` `runStep` (service.go:461), read `step.Context`
  and pass a `contextfile.StepContext` into agent construction (`NewAgent` call at
  service.go:513); populate the `${flow.id}` / `${flow.step}` template tokens
  explicitly from `f.ID` and `step.ID`, both in scope at the call site — NOT from
  `FlowIDContextKey` / `FlowStepIDContextKey`, which are set later (service.go:654-656)
  on the Run ctx for telemetry and are invisible to the context-free `NewAgent`
  (factory.go:207-208 discards its ctx)

- [x] 5.2 Thread `stepCtx *contextfile.StepContext` through the `NewAgent` signature
  in the agent factory and into `getAgentPromptInternal`; the flow/step template
  values ride the `StepContext` (or a `contextfile.TemplateVars`) through the
  signature because `NewAgent` discards its ctx (factory.go:207-208); use it in
  `contextfile.ResolveForAgent` for the step-layer resolution

- [x] 5.3 Add test: a flow YAML with a step declaring `context: { paths:
  ["STEP.md"], mode: replace }` resolves only `STEP.md`; a step without `context`
  receives the global default; a template with `context` is inherited by an extending
  step; an extending step's `context` overrides the template's

## 6. Progressive disclosure: wrapper and activation

- [ ] 6.1 Implement `contextDisclosureWrapper` in `internal/llm/agent/tools.go`:
  wraps `read`, `write`, `edit`, `multiedit`, `patch`, `grep`, `glob`, `ls` tools
  only when `discoveryResult` is non-empty (`delete` is deliberately not a trigger:
  removing files is not working-within-a-subtree that benefits from its
  instructions); recognizable by type assertion (mirrors `DeferredWrapper`); all
  wrappers of one toolset share ONE disclosure-state object (mutex + `sessionID →
  set[absPath]` + `sessionID → injectedBytes`) created in `NewToolSet` and passed by
  pointer — mirroring the shared `deferSeq` counter (`WrapDeferred`, deferred.go:33);
  delegates all `BaseTool` interface methods to the inner tool

- [ ] 6.2 Extend the existing `tools.ExtractPathsFromCall`
  (`internal/llm/tools/tools.go:167`) rather than writing greenfield helpers — it
  already parses `patch` paths from `patch_text` section headers (via
  `diff.IdentifyFilesNeeded`/`IdentifyFilesAdded`) and the generic `file_path`/`path`
  params; add on top: `grep` with no `path` arg returns nothing (activates nothing);
  `glob` combines `path` and the literal directory prefix of the pattern; file-taking
  tools (`read`/`write`/`edit`/`multiedit`) derive the parent directory of `file_path`

- [ ] 6.3 Implement owner resolution in `contextfile/discovery.go`:
  `OwnersForPath(dir string, discovered []string, workDir string) []string` — walks
  upward from `dir` to (excluding) `workDir`, returns nested files whose owning
  directory is on the path, outermost-first

- [ ] 6.4 Wire the wrapper into `NewToolSet` (`internal/llm/agent/tools.go`): after
  the discovery walk result is available (cached call), wrap the trigger tools
  INSIDE deferral — `maybeDefer(maybeWrapDisclosure(t))` — so `*tools.DeferredWrapper`
  stays outermost and the four existing type assertions (anthropic.go:588,
  agent.go:2235, agent.go:2353, agent/tools.go:328) keep working; install zero
  wrappers when discovery result is empty; the shared disclosure-state object is
  created once per toolset in `NewToolSet` (per-toolset, not per-wrapper, not a
  global)

- [ ] 6.5 Add unit tests for the wrapper: first read into `services/auth/` triggers
  injection of `services/auth/AGENTS.md`; second read does not re-inject; cross-tool
  dedup — a `read` fires the injection and a `grep` on the same directory does not
  re-inject (exercises the shared per-toolset state); outermost-
  first when both `services/AGENTS.md` and `services/auth/AGENTS.md` are discovered;
  whole-tree grep with no `path` activates nothing; failed tool call does not trigger
  injection; unreadable file is skipped (WARN logged, tool result unchanged); budget
  exhaustion logs INFO and stops injecting; two sessions on the same agent instance
  are isolated (session A's activations not visible to session B); a tool that is
  both deferred and a trigger keeps `*tools.DeferredWrapper` outermost and both
  behaviors work

## 7. Docs and e2e

- [ ] 7.1 Update `CLAUDE.md` agent-fields table: add `context` entry with the
  `paths`, `mode`, and `nested` sub-fields and a cross-reference to `docs/context.md`

- [ ] 7.2 Create `docs/context.md`: full user-facing documentation of scoped context
  resolution (field reference, `replace` vs `append` semantics, template tokens,
  backward compatibility note) and progressive context disclosure (how the manifest
  works, which tools trigger injection, opt-out with `nested: false`, cap configuration)

- [ ] 7.3 Update `docs/flows.md`: add step-field entry for `context` in the step
  schema table, noting it is inheritable via `extends` and is absent from
  `nonInheritableStepKeys`

- [ ] 7.4 Update the `flow-creator` skill (`.agents/skills/flow-creator/SKILL.md`)
  reference to include the `context` step field in the step schema summary

- [ ] 7.5 Write `scripts/test/scoped_context.sh`: e2e script (self-contained,
  `mktemp` sandbox, no external services) that:
  - asserts a no-`context` agent produces a byte-identical system prompt (A/B against
    a reference capture)
  - creates a workspace with `services/auth/AGENTS.md` and asserts the manifest
    appears in the logged system prompt
  - runs a session where the first `read` into `services/auth/` injects the body
    (verify via log or prompt-dump)
  - runs a second `read` into the same directory and confirms no duplicate injection

- [ ] 7.6 Run `make test` and `make test-e2e` green; for NEW tests that assert on
  full assembled prompts (e.g. the byte-identical backward-compat test), guard
  discovery off by setting `contextDiscovery.enabled = false` in test configs where
  the walk would find unexpected files (`TestBasePromptBudgets` needs no guard — it
  measures base prompt constructors only)
