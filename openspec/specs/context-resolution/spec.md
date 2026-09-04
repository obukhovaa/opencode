# context-resolution Specification

## Purpose
TBD - created by archiving change scoped-context-files. Update Purpose after archive.
## Requirements
### Requirement: Resolution precedence is step > agent > global

The system SHALL resolve the effective context path list for a given agent turn by
evaluating three ordered layers from highest to lowest priority:

1. **Step** — `context.paths` declared on the current flow step (highest).
2. **Agent** — `context.paths` declared in the agent config or markdown frontmatter.
3. **Global** — the top-level `contextPaths` list from config (lowest).

Resolution starts at the highest declared layer and works downward. A layer is
"declared" when it explicitly sets `context.paths` — INCLUDING an explicitly empty
list (`paths: []` / `"paths": []`), which is the natural way to give an agent or step
zero context files: empty + `replace` yields an empty context block, empty + `append`
contributes nothing and continues downward. Only an absent (nil) `context.paths` is
undeclared and skipped. The `mode` of the highest declared layer controls whether
layers below it contribute:

- `mode: replace` (default when `context.paths` is declared) — discard all layers
  below the declaring layer.
- `mode: append` — include the declaring layer and continue downward to the next
  declared layer, applying that layer's own `mode` by the same rule.

Because each declaring layer carries its own `mode`, evaluation is compositional: a
step with `mode: append` folds in the agent layer, then the agent's `mode` governs
whether the global layer contributes. When no layer declares `context.paths`, the
global `contextPaths` list is used unchanged — today's behavior exactly.

In `append` mode, deduplication (EvalSymlinks + lowercase canonicalization)
applies across the whole merged set so a file named at two layers appears once. An
unrecognized `mode` value SHALL be warned once (log WARN, key and value named) and
fall back to `append` — a typo MUST NOT silently discard the project's root
instructions.

The global `contextPaths` list itself is never modified by agent or step configuration.
`contextPaths` entries that are not overridden by any layer are consumed exactly as
today (trailing-slash entries recurse unconditionally; file entries silently skip
missing files; dedupe via EvalSymlinks+lowercase).

#### Scenario: Mixed modes — step `append` over agent `replace` discards global

- **WHEN** the global config has `contextPaths: ["AGENTS.md"]`, the agent declares
  `context: { paths: ["AGENTS.agent.md"], mode: replace }`, and the step declares
  `context: { paths: ["AGENTS.step.md"], mode: append }`
- **THEN** the assembled prompt contains `AGENTS.step.md` and `AGENTS.agent.md`
- **AND** `AGENTS.md` does NOT appear — the agent's `replace` discards the global
  layer, and the step's `append` only extends down to the agent layer

#### Scenario: Step `replace` excludes agent and global context

- **WHEN** the global config has `contextPaths: ["AGENTS.md"]` and a flow step
  declares `context: { paths: ["AGENTS.runtime.md"], mode: replace }`
- **THEN** the assembled system prompt contains only the content of `AGENTS.runtime.md`
  under `# Project-Specific Context`
- **AND** `AGENTS.md` content does not appear in the prompt

#### Scenario: Step `append` accumulates across layers

- **WHEN** the global config has `contextPaths: ["AGENTS.md"]`, the agent declares
  `context: { paths: ["AGENTS.agent.md"], mode: append }`, and the step declares
  `context: { paths: ["AGENTS.step.md"], mode: append }`
- **THEN** the assembled prompt contains all three files, with each `# From:<path>`
  block present and files sorted by absolute path within each layer
- **AND** a file that appears in two layers is included only once (deduplicated by
  canonical path)

#### Scenario: No config yields byte-identical behavior

- **WHEN** an agent has no `context` field in `.opencode.json` or markdown frontmatter
  and no flow step has a `context` field
- **THEN** the assembled system prompt is byte-identical to the pre-feature build for
  the same `contextPaths` config
- **AND** the `# Project-Specific Context` header, file order, `# From:<abs-path>`
  headers, and file bodies are unchanged

#### Scenario: Unrecognized mode fails safe

- **WHEN** an agent declares `context: { paths: ["foo.md"], mode: "xyzzy" }`
- **THEN** a WARN log entry names the agent and the unrecognized value
- **AND** resolution falls back to `append` — the agent receives `foo.md` content
  concatenated after the global context, not no context at all
- **AND** the once-only WARN dedupe is keyed on (layer, agent-or-step ID, value), so
  a second agent with the same typo still produces its own WARN naming that agent

#### Scenario: Explicitly empty declared layer

- **WHEN** an agent declares `context: { paths: [], mode: replace }`
- **THEN** the agent's `# Project-Specific Context` block is empty (absent) — the
  empty list is a declaration, not a fall-through to the global `contextPaths`
- **AND** with `mode: append` instead, the layer contributes nothing and resolution
  continues downward to the global layer unchanged

### Requirement: Agent-level `context` is accepted in `.opencode.json` and markdown frontmatter

The `context` object (`paths: []string`, `mode: string`, `nested: bool`) SHALL be
accepted in the `agents.<name>` map in `.opencode.json` (as `Agent.Context`) and in
markdown-agent YAML frontmatter (as `AgentInfo.Context`, parsed automatically by the
existing frontmatter decoder). Merge precedence follows the existing two-site merge
pattern: `applyConfigOverrides` applies the `.opencode.json` value, and
`mergeMarkdownIntoExisting` applies the markdown value, both mirroring the `Tools` /
`DeferredTools` `maps.Copy` pattern. The merged value is declared in the generated
JSON schema (`cmd/schema/main.go`, regenerated `opencode-schema.json`).

Because `Config.Agents` is a `map[AgentName]Agent` and viper case-folds map keys on
JSON load, a config with a mixed-case agent name key (e.g. `"MyAgent"`) will have its
key lowercased to `"myagent"` by viper — the same hazard documented and tested for
`DeferredTools`. The `Context` struct values (`paths`, `mode`, `nested`) are not map
keys and are not affected by key folding.

#### Scenario: Markdown-agent frontmatter sets scoped paths

- **WHEN** `.opencode/agents/runtime.md` declares `context: { paths: ["RUNTIME.md"],
  mode: replace }` in its YAML frontmatter
- **THEN** the `runtime` agent resolves only `RUNTIME.md` as its context, with no
  contribution from the global `contextPaths`

#### Scenario: `.opencode.json` `context` merges over built-in defaults

- **WHEN** `.opencode.json` sets `agents.coder.context.paths = ["CODER.md"]` and
  `agents.coder.context.mode = "replace"`
- **THEN** the `coder` agent resolves only `CODER.md`, not the default `contextPaths`

#### Scenario: Viper round-trip preserves `context` semantics despite key case-folding

- **WHEN** `.opencode.json` declares an agent key with mixed case (e.g. `"SomeAgent"`)
  bearing a `context` object, and the config is loaded through viper
- **THEN** a `viper.Unmarshal` round-trip unit test confirms the `Context.Paths`,
  `Context.Mode`, and `Context.Nested` fields survive, while the agent-map key is
  folded to lowercase — the folded lowercase key is what `applyConfigOverrides`
  sees, so the override lands as expected for lowercase agent IDs (all built-ins);
  a mixed-case markdown agent ID does not merge with a JSON override — a
  pre-existing limitation of every per-agent JSON field, not new to `context`

### Requirement: Shell-free templating with workDir containment

Path entries in `context.paths` SHALL support exactly four substitution tokens, expanded
before filesystem resolution: `${agent}` (agent ID), `${flow.id}` (flow ID or empty
string), `${flow.step}` (step ID or empty string), `${env.VAR}` (value of environment
variable `VAR`, or empty string). No shell execution, no glob expansion, no recursive
substitution, no other template syntax.

An entry that still contains a literal `${...}` after substitution (due to an unknown
token name), or in which any recognized substitution token (`${agent}`, `${flow.id}`,
`${flow.step}`, `${env.VAR}`) expands to an empty value, SHALL be skipped with a
DEBUG log — regardless of which path segment the empty value falls in. An entry that
passes both checks is then joined to
`workDir` and cleaned with `filepath.Clean`. The cleaned absolute path MUST reside
strictly inside `workDir` (i.e. `strings.HasPrefix(abs, workDir+"/")` after symlink
resolution); entries that escape are rejected with a WARN log naming the entry and the
resolved path. No such entry ever reaches the filesystem.

#### Scenario: Agent ID substitution resolves a per-agent file

- **WHEN** an agent has `context.paths = ["AGENTS.${agent}.md"]` and the agent ID is
  `workhorse`
- **THEN** the resolver attempts to read `AGENTS.workhorse.md` relative to `workDir`
- **AND** if that file does not exist, it is silently skipped (existing behavior for
  missing context files)

#### Scenario: Unknown token skips the entry

- **WHEN** a step declares `context.paths = ["AGENTS.${unknown}.md"]`
- **THEN** a DEBUG log records the unresolved entry and it is not probed on disk

#### Scenario: Recognized token with empty value skips the entry

- **WHEN** an agent runs outside a flow (so `${flow.id}` expands to an empty string)
  and `context.paths = ["AGENTS.${flow.id}.md"]`
- **THEN** a DEBUG log records the skipped entry and `AGENTS..md` is NOT probed on disk
- **AND** the `${flow.id}` token is recognized (not a literal `${...}` residue), so
  the skip is triggered by the empty-value rule, not the unknown-token rule

#### Scenario: Path traversal via env var is blocked

- **WHEN** `${env.EVIL}` expands to `"../../../etc/passwd"` and the joined path
  escapes `workDir`
- **THEN** a WARN log names the entry and resolved path, and the file is not read

### Requirement: Keyed memoization provides byte-stability per session

The resolved context string for a given (path list, mode, workDir) combination SHALL
be computed at most once per process and reused thereafter. The memoization key SHALL be
derived from the sorted, absolute, resolved path list and the `mode` value (a stable
digest sufficient to distinguish different resolution configurations). A
`singleflight.Group` over the cache prevents redundant concurrent computation for the
same key. Within a session, the same agent always produces the same context block.

The resolved string is never modified after first computation. Two distinct agents with
different resolved path lists get separate cached values; two turns of the same agent
return the same cached string (pointer-reuse acceptable). After a process restart the
cache is empty and re-computed on first use.

#### Scenario: Two agents with different resolved sets in one process

- **WHEN** agent A resolves `["AGENTS.md"]` and agent B resolves `["AGENTS.runtime.md"]`
  in the same process
- **THEN** each agent's system prompt contains only its own resolved content
- **AND** the two cached strings are independent (a change to A's mock file content
  does not affect B's cached value)

#### Scenario: Repeated turns produce identical context block

- **WHEN** an agent's `getContextFromPaths`-equivalent is called on turn 1 and turn 2
  of the same session
- **THEN** the returned string is the same object (or byte-identical), confirming
  the cache is hit on the second call

### Requirement: Preserved observable formats

The following behaviors of the existing resolution system SHALL be preserved exactly
when scoped resolution falls through to the global `contextPaths` (i.e. no agent or
step override):

- Each matched file is prefixed with `# From:<absolute-path>\n` followed by its raw
  content, with no trailing newline added or removed.
- Multiple files are joined with a single `\n` between entries, sorted by absolute path
  ascending.
- Missing files are silently skipped (no error, no placeholder in the output).
- Trailing-slash directory entries in `contextPaths` recurse unconditionally via
  `filepath.WalkDir`, including every non-directory file found.
- Symlinks and relative paths pointing to the same canonical file are deduplicated via
  `EvalSymlinks` + lowercase normalization; the file appears once.

These formats apply within each layer when `mode: append` is active; the merged output
is the concatenation of layer outputs in global → agent → step order.

#### Scenario: `# From:` header format is stable

- **WHEN** the resolver reads `/workspace/AGENTS.md`
- **THEN** the entry in the assembled block is exactly `# From:/workspace/AGENTS.md\n`
  followed by the raw file content — no change to header or footer format

#### Scenario: Symlink and relative-path deduplication still applies

- **WHEN** `contextPaths` contains both `"AGENTS.md"` and `"./AGENTS.md"` and they
  resolve to the same canonical path
- **THEN** the file appears exactly once in the assembled block

