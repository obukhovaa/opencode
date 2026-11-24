## 1. Config surface

- [x] 1.1 Add `ListingScopeConfig{Skills, Subagents []string}` and the top-level `ListingScope *ListingScopeConfig` field (`json:"listingScope,omitempty"`) to `internal/config/config.go`.
- [x] 1.2 Add a viper round-trip test: `${args.team}/skills/**` and `.agents/skills/**` load unchanged through `config.Load`, including environment-reference expansion.
- [x] 1.3 Declare `listingScope` in `cmd/schema/main.go`, regenerate `opencode-schema.json`, and confirm `make schema-check` and `go test ./cmd/schema/...` pass.

## 2. Scope resolution and matching (`internal/llm/tools`)

- [x] 2.1 Add the `ListingScope` value type with `WithListingScope` / `ListingScopeFrom` context helpers and a canonical `Key()` string. A nil list means the list does not apply.
- [x] 2.2 Add the location matcher: relative to `config.WorkingDirectory()`, forward slashes, `..` never matches, `doublestar.Match`. Unit-test in-tree paths, out-of-tree paths, `**`, and an empty location.
- [x] 2.3 Add `ResolveListingScope(cfg, resolve func(string) (string, bool))`. Per list: escape glob metacharacters in each scalar value, substitute, and mark the whole list inactive on any unresolved, empty, non-scalar or non-`args` token. Unit-test the scenarios in spec requirement 2, including a literal `*` value.

## 3. Flow wiring (`internal/flow/service.go`)

- [x] 3.1 Resolve the scope from the run's live args with the same substitution as step `model:`, then set `tools.WithListingScope` next to `tools.WithDeniedTools`, before `NewAgent`. Log a misconfigured token once per pattern.
- [x] 3.2 Flow test: a run with `team` gets a scoped step agent, a run without `team` an unscoped one, and a step after a `team`-setting step is scoped.

## 4. Agent factory and toolset (`internal/llm/agent`)

- [x] 4.1 Add the scope's `Key()` to `stepCacheKey`, and pass the scope from `NewAgent` through `newAgent` to `NewToolSet`. Leave the pinned-prefix path (`primaryFlowPrefix.toolsFor`) untouched.
- [x] 4.2 In `NewToolSet`, pass the skills list to `NewSkillTool` and the subagents list to the `task` tool constructor.
- [x] 4.3 Copy the scope onto the detached async task context in `agent-tool-async.go`, the same way as `DeniedTools`.
- [x] 4.4 Test: a re-entered step with a different scope gets a new agent; sync and async subagents of a scoped step carry the same scope; subagents of an unscoped agent carry none.

## 5. Two-tier skill listing (`internal/llm/tools/skill.go`)

- [x] 5.1 Split permitted skills into full and names-only by the scope. Render `<other_skills count="N">` with its note after the full entries, and only when the names-only set is non-empty.
- [x] 5.2 Reserve the names-only element and note in the `maxListingChars` budget before fitting full entries. Keep the existing drop, disclose and keep-one rules, and never truncate the names.
- [x] 5.3 Draw the parameter-description examples from in-scope skills. Keep the not-found suggestion list covering every permitted skill.
- [x] 5.4 Log once per scoped tool (`sync.Once`): agent, `skills`, full and names-only counts.
- [x] 5.5 Tests: byte-identity with no scope; the two-tier split; a denied skill absent from both tiers; a tight budget keeps every name; nothing out of scope equals unscoped; a names-only skill loads by name.

## 6. Two-tier subagent listing (`internal/llm/agent/agent-tool.go`)

- [x] 6.1 Split subagent types by the scope. Agents without a location are always full. Append the sorted names-only line with its note, only when non-empty.
- [x] 6.2 Log once per scoped tool, as in 5.4.
- [x] 6.3 Tests: byte-identity with no scope; the split, with an `.opencode.json` agent kept full; `task` with a names-only `subagent_type` runs; the unknown-type error lists every subagent.

## 7. Docs and verification

- [x] 7.1 Document `listingScope` in `docs/skills.md`: semantics, fail-open, permissions interplay, budget, and a Piano-style example. Cross-reference it from `docs/flows.md` (per-run scoping, subagent inheritance) and `docs/configuration.md`.
- [x] 7.2 Run `go build ./...`, `go vet ./...` and `go test ./...`, and `openspec validate run-scoped-listings --strict`.
- [x] 7.3 Measure with a local fixture of the Piano workspace layout (shared `.agents/skills` and several `<team>/skills`, plus types): record the description sizes of both tools, scoped and unscoped, in the PR description.
