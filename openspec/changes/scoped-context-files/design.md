# Design: scoped-context-files

## Context

Current state (verified against HEAD):

- **Context loading**: `getContextFromPaths()` (`prompt.go:583`) runs once per
  process under a package-level `sync.Once` (`prompt.go:578-580`). The result
  (`contextContent`) is appended to every agent's system prompt unconditionally as
  `"# Project-Specific Context\n Make sure to follow the instructions in the context
  below\n"` followed by one `"# From:<abs-path>\n<body>"` block per matched file
  (`prompt.go:454-458`, `processFile:682-688`).
- **Prompt-cache invariant**: the anthropic client ships the entire system prompt in
  ONE `TextBlockParam` with `cache_control: ephemeral` (`anthropic.go:742-748`). Any
  byte change between turns of the same session invalidates the cached prefix. This is
  the single hardest constraint on this design.
- **Deferred-tools precedent**: `DeferredWrapper` (`tools/deferred.go`) demonstrates
  per-session activation state on a long-lived, frozen toolset; `toolsearch` result
  delivery via `<system-reminder>` in a tool result establishes the injection
  mechanism the progressive-disclosure wrapper reuses.
- **Merge precedent**: `applyConfigOverrides` and `mergeMarkdownIntoExisting`
  (`registry.go:441-557`) already use `maps.Copy` for `Tools` and `DeferredTools`.
  `Step` fields are inherited through `extends` via reflection over declared YAML keys
  (`include.go:244`); `nonInheritableStepKeys` (`include.go:182-187`) is the explicit
  block-list.
- **Pending worktree draft**: `spec/20260311T120000-git-worktree-isolation.md` wants a
  `sync.Map` keyed by project root to support per-worktree context. D2 is designed to
  subsume that: the keyed memoization map in `internal/contextfile` uses a richer key
  (path digest + mode) but can also accommodate per-root variation without conflict.

## Goals / Non-Goals

**Goals:**

- Backward-compatible: absent config ⇒ byte-identical prompts to today (guarded by
  unit tests and the e2e backward-compat scenario).
- The prompt-cache invariant is preserved end-to-end: the manifest and resolved context
  block are byte-stable within a session; nested bodies land in tool results only.
- Failure in either capability degrades silently to the global default; no agent run
  is ever blocked by a context-resolution error.

**Non-Goals** (see proposal.md for full list; design-level additions):

- No file watching or hot reload — restating explicitly because the memoization design
  would make it easy to add later (invalidate on key miss) and an implementer must not
  add it in passing.
- No changes to the `sync.Once` semantics outside the `contextfile` package — callers
  that bypass `getContextFromPaths()` (tests using `prompt_test.go` helpers) are not
  affected.
- No ancestor walk-up above `workDir`; the discovery walk is strictly downward.

## Decisions

### D1. New leaf package `internal/contextfile`

Move all context discovery, resolution, reading, templating, and memoization into a
new package that declares no opencode-internal imports beyond `config`, `logging`, and
the standard library. Required because the progressive-disclosure injection lives in
the tool layer (`internal/llm/tools`, `internal/llm/agent/tools.go`) and must inspect
path parameters from tool calls. `internal/llm/prompt` already imports the agent
registry, skills, and permission packages; adding a `prompt` → `tools` edge (or a
`tools` → `prompt` edge in the other direction) would create the same import cycle
that forced the `internal/bridge` / `internal/bridge/service` split. Both `prompt`
and the tool layer import the new leaf independently.

Alternative considered — keep resolution in `prompt.go` and export a function the
tools layer calls: rejected because it creates a direct `tools → prompt` import that
is exactly the cycle the split avoids, and the resulting export surface would be
underdefined (it mixes prompt-assembly concerns with path resolution).

### D2. Keyed memoization replaces the process-global `sync.Once`

Resolution key = SHA-256 of the sorted, absolute, resolved path list + the `mode`
string. Content cached in a `sync.Map[string, string]` for process lifetime.
Rationale: preserves today's staleness semantics (no file watching; editing a context
file still requires a restart) while allowing N distinct resolved sets per process.
First computation wins; a second goroutine requesting the same key blocks on the first
result via a `singleflight.Group` wrapper over the `sync.Map` to avoid redundant I/O.

The pending `spec/20260311T120000-git-worktree-isolation.md` draft wants per-root
variation: the keyed approach is a strict superset — a per-root key is just an
instance of the general case. The design MUST NOT conflict with that draft; the
`contextfile` package exposes `Resolve(paths []string, workDir string, mode Mode)
string` and a per-root key falls out naturally.

Alternative considered — extend the existing `sync.Once` with a `sync.Map` keyed by
agent ID: rejected because agent ID is not stable across config changes (a user can
add an agent config line that changes the effective paths for agent "coder" without
restarting) and the key must encode the resolved path set, not just the agent name.

### D3. Prompt-cache invariant is a hard constraint, enforced by test

The resolved context block MUST be byte-identical across every turn of a session, and
nested context bodies MUST NOT be injected into the system prompt.

Mechanism: `Resolve()` is called once per agent-client construction (same lifecycle as
today), and the memoization key encodes the full resolved path list so the same call
from the same agent in the same process always returns the same string. A unit test
asserts that calling `Resolve()` twice with the same arguments returns pointer-equal
strings (the map value is reused, not copied). An integration test asserts that the
assembled system prompt for an agent is byte-identical on a simulated second turn.

### D4. `mode` semantics and defaults

`mode` is an enum `replace | append`, default **`replace`** when an agent or step
explicitly declares `context.paths` — the motivating requirement is exclusion, and the
field only exists when the user has opted in to overriding the default. An
**unrecognized** `mode` value is warned once and falls back to **`append`** (fail-safe:
a typo must never silently drop the project's root instructions). In `append` mode the
layers concatenate in precedence order global → agent → step; within each layer the
existing sort-by-absolute-path ordering and `# From:<abs-path>` header format are
preserved. Dedupe (existing `EvalSymlinks` + lowercase canonicalization from
`tryMarkProcessed`) applies across the whole merged set.

Alternative considered — default `append` when any layer is declared: rejected because
it makes a step that adds one small file also drag in all root context, which is the
inverse of the motivating use case (exclusion). `replace` default plus `append` opt-in
mirrors the mental model: "I am replacing the context for this step; use `mode: append`
if you want to augment."

### D5. Shell-free templating and workDir containment

Expand `${agent}`, `${flow.id}`, `${flow.step}`, `${env.VAR}` using a simple
`strings.ReplaceAll` pass before filesystem resolution. An entry that still contains a
literal `${...}` after substitution, or in which any recognized substitution token
(`${agent}`, `${flow.id}`, `${flow.step}`, `${env.VAR}`) expands to an empty value,
is skipped with a debug log — regardless of which segment the empty value falls in.
Example: `AGENTS.${flow.id}.md` run outside a flow expands to `AGENTS..md`; because
`${flow.id}` is a recognized token with an empty value the entry is skipped and
`AGENTS..md` is never probed on disk. After joining to `workDir`, the cleaned
absolute path MUST be confirmed inside `workDir` with `strings.HasPrefix(cleaned,
workDir+string(os.PathSeparator))` (or equivalent); entries that escape are rejected
and logged as WARN.

`${env.VAR}` is kept deliberately limited: only the variable value, no glob expansion,
no recursive substitution, no shell interpretation. This makes it safe even when
env vars are attacker-controlled in CI environments. Shell markup (`` !`cmd` ``) is
explicitly absent from context path entries (unlike skill bodies where it was already
excluded for preloaded skills) — executing shell at prompt-build time with no
permission prompt is the exact hazard.

### D6. Nested discovery and caps

Once per process per `workDir`: derive the set of context *basenames* from the
file-type entries of the effective global `contextPaths` (entries with a trailing `/`
are excluded — they keep today's inline-everything semantics and their subtree content
is not a candidate for progressive disclosure). Walk the subtree using
`filepath.WalkDir` with the hardcoded skip list used by `ls`'s fallback walker
(`shouldSkip()` in `internal/llm/tools/ls.go`: hidden-file prefix, `.git`,
`node_modules`, `vendor`, and common build directories), plus the configured data
directory (`.opencode` by default). There is no `go-gitignore` dependency in the
project — `.gitignore` is honored only via ripgrep's built-in support on the
`ls`/`glob` primary path, which is unavailable to a custom `WalkDir` call. No
reusable ignore helper currently exists for direct consumption here; whether to drive
this walk via ripgrep instead (gaining `.gitignore` support) or to extend the
hardcoded list is a task-level decision.
Caps: `maxDepth` (default 8), `maxFiles` (default 100), `maxFileBytes`
(default 32 KiB per file), `maxSessionBytes` (default 128 KiB per session total).
Files at or below `workDir` matching the basename set but NOT already matched by root
resolution are the progressive-disclosure candidates.

The walk result is a `map[string]struct{}` of absolute paths, computed once and cached
by `workDir`. Root-level matches remain the job of scoped resolution (D4); this set
only contains files STRICTLY below the root (depth ≥ 1).

### D7. Manifest block

When ≥1 nested file was discovered, append a compact, cache-stable section to the
system prompt listing one line per file: relative-to-workDir path + a short label
(YAML frontmatter `description`, else first markdown heading, truncated to ~120 chars;
path only if neither exists). The block carries one sentence explaining that bodies
are not loaded and arrive automatically on first directory touch. Absent entirely when
nothing was discovered — zero prompt delta for repos without nested context files.

The manifest is computed at prompt-build time using the process-level discovery cache
and is byte-stable (same inputs ⇒ same output), so it satisfies D3 without special
treatment. A per-manifest line and total-byte cap (overflow: paths-only, then trailing
"N more" line) prevents adversarially large subtrees from bloating the manifest itself.

### D8. Activation trigger and owner resolution

Trigger tools: `read` / `write` / `edit` / `patch` → `file_path`; `grep` → `path`
(when set; no `path` in a grep call resolves to `workDir` and activates nothing); `glob`
→ `path` + literal directory prefix of the pattern; `ls` → `path`. The `bash` tool is
deliberately excluded (see proposal.md Non-Goals): scanning command strings for path
tokens produces too many false positives and false negatives.

Activation criterion: the tool's resolved **directory** (parent of a file arg, or the
arg itself for directory-taking tools) must be strictly equal to or inside a nested
context file's owning directory. Owner resolution: walk from the target directory up
to (but excluding) `workDir`, collect every nested context file on the upward path,
inject not-yet-injected ones outermost-first — reproducing Claude Code's additive
layering without mutating the system prompt.

### D9. Injection mechanism: `contextDisclosureWrapper`

A `contextDisclosureWrapper` in the toolset assembly (`internal/llm/agent/tools.go`)
wraps ONLY the trigger tools and ONLY when the discovery set is non-empty — zero
allocation and zero behavior change otherwise. It mirrors the `DeferredWrapper`
type-assertion pattern: providers and `toolsearch` recognize the wrapper by type
assertion without a new `BaseTool` interface method. The wrapper: extracts the path
parameter from the serialized tool call, calls the inner tool, and on SUCCESS appends a
`<system-reminder>`-tagged block to the tool result content carrying the `# From:<abs-path>`
header and the file body. On failure the tool result is returned unchanged.

Precedent for `<system-reminder>`-wrapped tool results: `toolsearch` (`tools/toolsearch.go`).
The injection MUST fire only on a successful inner call — injecting directory context
because a `read` of a nonexistent file failed is noise and risks confusing the model.

### D10. Failure and error isolation

Discovery walk errors are skipped file-by-file (log WARN) and never surface to the
model. An unreadable or oversized nested file at activation time is skipped with a log;
the tool result is returned as-is. Session byte budget exhaustion at activation time:
log INFO once per session and stop injecting further bodies (the model may still touch
those directories; only the body injection stops). None of these paths return an error
to the caller.

### D11. Per-session activation state

`sessionID → set[absPath]` stored on the `contextDisclosureWrapper` (not the toolset
slice, which is frozen and shared). Sessions must not observe each other's activations.
Subagent sessions get their OWN activation set and do NOT inherit the parent's — a
subagent that never touches a directory must not pay for its context. After a process
restart, duplicate injection is accepted (same tradeoff the deferred-tools change
accepted: self-correcting, bounded cost, negligible versus the complexity of persisting
activation sets).

### D12. Config surface and naming

New `AgentContext` struct on both `config.Agent` and `AgentInfo` (yaml frontmatter):
```
context:
  paths: ["AGENTS.runtime.md"]
  mode: replace              # replace | append
  nested: false              # bool, default true
```
New top-level `ContextDiscovery *ContextDiscoveryConfig`:
```
contextDiscovery:
  enabled: true              # default true
  maxFiles: 100
  maxDepth: 8
  maxFileBytes: 32768
  maxSessionBytes: 131072
```
Deliberately NOT named `context` at top level, to avoid colliding with the
agent/step-level `context` object which has entirely different fields.
`contextDiscovery.enabled` defaults to **true**: the manifest is ~1 line per file, is
strictly cheaper than inline-everything, and repos with no nested context files see a
byte-identical prompt. Agents and steps may override with `context.nested: false`.

Viper case-folding hazard: `Config.Agents` is `map[AgentName]Agent`, keyed on
user-supplied agent names. Viper case-folds map keys on `.opencode.json` load — the
same hazard documented for `DeferredTools`. A viper round-trip unit test for
`Agent.Context` (mapping a key with mixed-case agent name through viper and back) is
required per CLAUDE.md.

### D13. Interaction with `prompt-surface` lean-prompts budgets

The manifest block is config-gated and dynamic (size depends on how many files the
discovery walk finds), so it is exempt from the static prompt-budget assertions in
`prompt_test.go` — identically to how the deferred-tools `<system-reminder>` block is
treated. The budget tests must be updated to acknowledge the dynamic manifest block
(comment noting it may add bytes when `contextDiscovery.enabled = true` and nested
files exist) rather than silently starting to fail the first time a nested context file
appears in the test's working directory.

### D14. Flow-step `context` field is inheritable

Adding `Context *StepContext` to `Step` makes it inheritable through `include`/`extends`
for free: the merge is reflection-driven over declared raw-YAML keys (`include.go:244`);
a field added to `Step` needs no code change in `mergeTemplates`. `Context` MUST NOT be
added to `nonInheritableStepKeys` — per-flow context is exactly the kind of thing
template files should be able to supply, and the orchestrator never reads the `context`
field (it reads `id`, `interactive`, `interaction`, `resume_after`). Resolution reads
`step.Context` in `runStep` where `FlowIDContextKey`/`FlowStepIDContextKey` are
already set (service.go:558-561), and passes a `contextfile.StepContext` into agent
construction.

## Risks / Trade-offs

- [Manifest byte cost for repos with many nested context files] → capped at ~1 line
  per file + a total manifest byte cap (overflow degrades to paths-only then "N more");
  a repo with 100 files in the cap uses at most ~120*100 + overhead ≈ 12 KB of manifest
  body, well below typical model context windows.
- [Viper case-folding drops agent `context` keys] → mitigated by the same viper
  round-trip test the `DeferredTools` field mandated; the `AgentContext` struct values
  (not keys) are strings, so folding only applies to the agent-name map key, not the
  `paths` list or `mode` field.
- [Wrong `grep` path resolution: a grep with no `path` arg should activate nothing but
  may still be called with an implicit `workDir`] → owner resolution is strictly
  between the resolved directory and `workDir` exclusive, so `workDir` itself matches
  nothing in the nested set (which is STRICTLY below); the scenario is covered by a
  unit test.
- [Process restart drops activation state → duplicate injection on next touch] →
  accepted; identical to the deferred-tools restart tradeoff. The injected bodies are
  idempotent and the model handles repeated context gracefully.
- [Import cycle `tools → prompt`] → prevented structurally by D1 (new leaf package).
  CI will catch a cycle immediately.
- [Template file with `${env.VAR}` path escaping workDir via symlink] → containment
  check uses `filepath.Clean` + `strings.HasPrefix` on the resolved (symlink-expanded)
  absolute path; cannot be bypassed by symlink chains.

## Migration Plan

Pure opt-in: no config ⇒ no resolver change, no manifest, byte-identical prompts
(guarded by the backward-compat e2e scenario). Rollout: add `context: { paths:
["AGENTS.runtime.md"], mode: replace }` to one step in a flow, validate the manifest
appears in the logged system prompt, verify the body is injected on the first `read`
call into the agent's working directory, and confirm the root instructions are excluded.
Rollback: remove the `context` key. No schema migration required.

## Open Questions

- Default cap values (`maxFiles: 100`, `maxDepth: 8`, etc.) are informed estimates;
  real-corpus data from a few large monorepos would sharpen them. They can be adjusted
  after the initial ship without spec changes — the caps are config, not behavior.
