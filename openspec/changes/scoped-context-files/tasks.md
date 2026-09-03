# Tasks: scoped-context-files

## 1. `internal/contextfile` package

- [ ] 1.1 Create `internal/contextfile/` package; move `processContextPaths`,
  `processFile`, and `tryMarkProcessed` from `internal/llm/prompt/prompt.go`
  (L602-688) into `contextfile/resolver.go`; preserve the `# From:<abs-path>\n<body>`,
  sort-by-absolute-path, silent-skip-on-missing, and EvalSymlinks+lowercase-dedup
  behaviors exactly

- [ ] 1.2 Implement `Resolve(paths []string, workDir string, mode Mode) string` with
  keyed memoization (`sync.Map` + `singleflight.Group`); key = SHA-256 of sorted
  absolute path list + mode string; add `Mode` type with `ModeReplace` / `ModeAppend`
  constants and the warn-on-unknown fallback-to-append logic

- [ ] 1.3 Implement the three-layer merge in `ResolveForAgent(globalPaths []string,
  agentCtx *AgentContext, stepCtx *StepContext, workDir string) string` applying
  precedence `step > agent > global` with `replace`/`append` semantics and
  cross-layer deduplication via the existing `tryMarkProcessed` logic

- [ ] 1.4 Implement shell-free templating (`expandTokens(entry string, vars
  TemplateVars) (string, bool)`) for `${agent}`, `${flow.id}`, `${flow.step}`,
  `${env.VAR}`; skip-on-unknown-token (DEBUG log) and skip-on-empty-segment (DEBUG
  log); workDir containment check (WARN log + reject) applied after template expansion
  and `filepath.Join(workDir, entry)` + `filepath.Clean`

- [ ] 1.5 Port and extend `internal/llm/prompt/prompt_test.go` context-related test
  cases into `internal/contextfile/resolver_test.go`; add new table-driven cases:
  replace vs append modes; three-layer merge with dedup; unknown-token skip; env var
  expansion; path traversal rejection; viper round-trip for `AgentContext` map key
  case-folding (per CLAUDE.md `TestConfig_HooksViperRoundTripLowercasesEventKeys`
  pattern); byte-stability / two-call pointer equality

## 2. Discovery walk and manifest

- [ ] 2.1 Implement `Discover(workDir string, globalPaths []string, cfg
  DiscoveryConfig) DiscoveryResult` in `contextfile/discovery.go`; extract basename
  set from file-type (non-trailing-slash) entries of `globalPaths`; walk subtree with
  `filepath.WalkDir` honoring `.gitignore` via existing ignore library; hard-skip
  `.git`, `node_modules`, `vendor`, data directory; apply `maxDepth`, `maxFiles`
  caps; cache result by `workDir` in a `sync.Map`; files at depth 0 (root) are
  excluded from the result set

- [ ] 2.2 Implement `RenderManifest(discovered []string, workDir string, cfg
  ManifestConfig) string` in `contextfile/manifest.go`: relative-path line per file;
  label from YAML frontmatter `description` else first markdown heading (truncated ~120
  chars) else path only; total-byte overflow degrades to paths-only then trailing
  "... N more"; absent (empty string) when `len(discovered) == 0`

- [ ] 2.3 Add unit tests for `Discover`: finds AGENTS.md in subdirectory but not at
  root; ignores .git subtree; respects maxDepth and maxFiles; cached on second call
  (no re-walk); `enabled: false` returns empty result

- [ ] 2.4 Add unit tests for `RenderManifest`: present when files found; absent when
  none; label extraction from frontmatter vs heading; overflow degradation; byte-stable
  on repeated calls with same inputs

## 3. Config surface

- [ ] 3.1 Add `AgentContext` struct (`Paths []string`, `Mode string`, `Nested *bool`)
  and `Agent.Context *AgentContext` to `internal/config/config.go`; add
  `ContextDiscoveryConfig` struct (`Enabled bool`, `MaxFiles int`, `MaxDepth int`,
  `MaxFileBytes int`, `MaxSessionBytes int`) and `Config.ContextDiscovery
  *ContextDiscoveryConfig`; set defaults in `setDefaults()` (L594): `enabled: true`,
  `maxFiles: 100`, `maxDepth: 8`, `maxFileBytes: 32768`, `maxSessionBytes: 131072`

- [ ] 3.2 Add `AgentInfo.Context *contextfile.AgentContext` to
  `internal/agent/registry.go` (yaml frontmatter parses automatically); add merge in
  `applyConfigOverrides` (mirror `DeferredTools` `maps.Copy` block, L463-468) and in
  `mergeMarkdownIntoExisting` (mirror L533-538 block)

- [ ] 3.3 Add `StepContext` struct (`Paths []string`, `Mode string`, `Nested *bool`)
  and `Step.Context *StepContext yaml:"context,omitempty"` to
  `internal/flow/flow.go`; no entry in `nonInheritableStepKeys` — confirm this by
  reviewing `include.go:182-187` and adding a comment noting it is intentionally absent

- [ ] 3.4 Declare `context` field shape in `cmd/schema/main.go` for both the
  `agents.*` agent def and the standalone agent schema block; declare
  `contextDiscovery` at the top-level schema; regenerate `opencode-schema.json` via
  `go run cmd/schema/main.go > opencode-schema.json`

- [ ] 3.5 Add viper round-trip unit test in `internal/config/` for `Agent.Context`:
  a config with an agent key containing uppercase letters (e.g., `"MyAgent"`) bearing
  a `context` object survives `viper.Unmarshal`, with the key lowercased and the
  `Context.Paths`, `Context.Mode`, and `Context.Nested` fields intact

## 4. Prompt integration

- [ ] 4.1 Replace `getContextFromPaths()` + `sync.Once` (prompt.go:578-594) with a
  call to `contextfile.ResolveForAgent(cfg.ContextPaths, agentInfo.Context,
  nil /*stepCtx*/, cfg.WorkingDir)` in `getAgentPromptInternal`; remove
  `processContextPaths`, `processFile`, `tryMarkProcessed` from `prompt.go` (they now
  live in `internal/contextfile`)

- [ ] 4.2 Add manifest rendering call in `getAgentPromptInternal` after the context
  block: `contextfile.RenderManifest(discovered, workDir, discoveryConfig)` appended
  to the system prompt when non-empty; guard on `info.Context.nested != false` (opt-out)

- [ ] 4.3 Update `internal/llm/prompt/prompt_test.go`: update any test that assumed the
  `sync.Once` global or the old `processContextPaths` signature; add a comment in the
  base-prompt budget test noting the manifest section is exempt; add the explicit
  backward-compat scenario ("no agent/step context config ⇒ byte-identical prompt")

## 5. Flow integration

- [ ] 5.1 In `internal/flow/service.go` `runStep` (L396-440 area), read `step.Context`
  and pass a `contextfile.StepContext` into agent construction; the flow context values
  `FlowIDContextKey` / `FlowStepIDContextKey` (already set at L558-561) provide
  `${flow.id}` and `${flow.step}` template tokens

- [ ] 5.2 Thread `stepCtx *contextfile.StepContext` through `NewAgent` signature in
  the agent factory and into `getAgentPromptInternal`; use it in
  `contextfile.ResolveForAgent` for the step-layer resolution

- [ ] 5.3 Add test: a flow YAML with a step declaring `context: { paths:
  ["STEP.md"], mode: replace }` resolves only `STEP.md`; a step without `context`
  receives the global default; a template with `context` is inherited by an extending
  step; an extending step's `context` overrides the template's

## 6. Progressive disclosure: wrapper and activation

- [ ] 6.1 Implement `contextDisclosureWrapper` in `internal/llm/agent/tools.go`:
  wraps `read`, `write`, `edit`, `patch`, `grep`, `glob`, `ls` tools only when
  `discoveryResult` is non-empty; recognizable by type assertion (mirrors
  `DeferredWrapper`); holds per-session activation map (`sessionID → set[absPath]`)
  and per-session byte-count map; delegates all `BaseTool` interface methods to the
  inner tool

- [ ] 6.2 Implement path-extraction helpers in `internal/llm/tools/tools.go` (or a
  new `internal/llm/tools/context_trigger.go`): one function per trigger tool taking
  the serialized JSON params and returning the resolved target directory; `grep` with
  no `path` arg returns empty string (activates nothing); `glob` combines `path` and
  directory prefix of the pattern; all others derive parent directory of `file_path`

- [ ] 6.3 Implement owner resolution in `contextfile/discovery.go`:
  `OwnersForPath(dir string, discovered []string, workDir string) []string` — walks
  upward from `dir` to (excluding) `workDir`, returns nested files whose owning
  directory is on the path, outermost-first

- [ ] 6.4 Wire the wrapper into `NewToolSet` (`internal/llm/agent/tools.go`): after
  the discovery walk result is available (cached call), wrap the trigger tools via
  `maybeWrapDisclosure(t, discoveryResult, perSessionState)`; install zero wrappers
  when discovery result is empty; per-session state is a value on the wrapper, not a
  global

- [ ] 6.5 Add unit tests for the wrapper: first read into `services/auth/` triggers
  injection of `services/auth/AGENTS.md`; second read does not re-inject; outermost-
  first when both `services/AGENTS.md` and `services/auth/AGENTS.md` are discovered;
  whole-tree grep with no `path` activates nothing; failed tool call does not trigger
  injection; unreadable file is skipped (WARN logged, tool result unchanged); budget
  exhaustion logs INFO and stops injecting; two sessions on the same agent instance
  are isolated (session A's activations not visible to session B)

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

- [ ] 7.6 Run `make test` and `make test-e2e` green; correct any budget test failures
  caused by the manifest section in test workspaces (guard discovery off in unit tests
  by setting `contextDiscovery.enabled = false` in test configs where the walk would
  find unexpected files)
