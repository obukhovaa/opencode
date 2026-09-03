# Design: scoped-context-files

## Context

Current state (verified against HEAD):

- **Context loading**: `getContextFromPaths()` (`prompt.go:598`) runs once per
  process under a package-level `sync.Once` (`prompt.go:593-596`). The result
  (`contextContent`) is appended to every agent's system prompt unconditionally as
  `"# Project-Specific Context\n Make sure to follow the instructions in the context
  below\n"` followed by one `"# From:<abs-path>\n<body>"` block per matched file
  (`prompt.go:469-473`, `processFile:697-703`).
- **Prompt-cache invariant**: the anthropic client ships the entire system prompt in
  ONE `TextBlockParam` with `cache_control: ephemeral` (`anthropic.go:742-747`). Any
  byte change between turns of the same session invalidates the cached prefix. This is
  the single hardest constraint on this design.
- **Deferred-tools precedent**: `DeferredWrapper` (`tools/deferred.go`) demonstrates
  per-session activation state on a long-lived, frozen toolset; `toolsearch` result
  delivery via `<system-reminder>` in a tool result establishes the injection
  mechanism the progressive-disclosure wrapper reuses.
- **Merge precedent**: `applyConfigOverrides` (`registry.go:418-516`) and
  `mergeMarkdownIntoExisting` (`registry.go:531-603`) already use `maps.Copy` for
  `Tools` and `DeferredTools` (blocks at 495-501 / 578-583). `Step` fields are
  inherited through `extends` via reflection over declared YAML keys
  (`mergeTemplates`, `include.go:259`); `nonInheritableStepKeys` (`include.go:182-187`)
  is the explicit block-list.
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
new package that declares no opencode-internal imports beyond `logging` and the
standard library (`logging` itself imports only `pubsub` — no cycle). It does NOT
import `internal/config`: the `AgentContext`, `StepContext`, and `DiscoveryConfig`
types are defined in `internal/contextfile`, and `internal/config` (`Agent.Context
*contextfile.AgentContext`, `Config.ContextDiscovery *contextfile.DiscoveryConfig`),
the agent registry (`AgentInfo.Context *contextfile.AgentContext`), and
`internal/flow` (`Step.Context *contextfile.StepContext`) all import the leaf.
Required because the progressive-disclosure injection lives in the tool layer
(`internal/llm/tools`, `internal/llm/agent/tools.go`) and must inspect path
parameters from tool calls. `internal/llm/prompt` already imports
`internal/llm/tools` (`prompt.go:17`); exporting resolution from `prompt` would force
the tools layer to import `prompt`, creating the cycle `tools → prompt → tools` —
the same class of cycle that forced the `internal/bridge` /
`internal/bridge/service` split. Both `prompt` and the tool layer import the new
leaf independently.

Alternative considered — keep resolution in `prompt.go` and export a function the
tools layer calls: rejected because it creates a direct `tools → prompt` import that
is exactly the cycle described above, and the resulting export surface would be
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
field only exists when the user has opted in to overriding the default. A layer is
declared when `context.paths` is non-nil, INCLUDING an explicitly empty list:
`paths: []` + `replace` yields an empty context block (zero context files), `paths:
[]` + `append` contributes nothing and continues downward — only an absent field is
undeclared. An **unrecognized** `mode` value is warned once per (layer, agent/step ID,
value) — the WARN names the misconfigured agent or flow step — and falls back to
**`append`** (fail-safe: a typo must never silently drop the project's root
instructions). In `append` mode the
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
directory (`.opencode` by default). `.gitignore` honoring is a deliberate non-goal
for v1 (see proposal.md Non-Goals): there is no gitignore library in `go.mod`, and
ripgrep — the only place `.gitignore` is honored today (the `ls`/`glob` primary
path) — is an external binary unsuitable for the prompt-build path; the caps below
bound over-discovery.
Caps: `maxDepth` (default 8), `maxFiles` (default 100), `maxFileBytes`
(default 32 KiB per file), `maxSessionBytes` (default 128 KiB per session total).
Files at or below `workDir` matching the basename set but NOT already matched by root
resolution are the progressive-disclosure candidates.

Candidates MUST be regular files: any non-regular entry (symlink, FIFO, device) is
rejected with a WARN. Git preserves symlinks, so a committed
`docs/AGENTS.md -> ~/.ssh/id_rsa` would otherwise flow into the manifest and the
injection path with no permission prompt — the exact bypass the scoped-entry
containment (D5) closes. As defense in depth against symlinked parent directories,
each accepted candidate is `EvalSymlinks`-resolved and must land strictly inside the
`EvalSymlinks`-resolved `workDir` (same order as `containedInWorkDir`). Each accepted
file's manifest label is extracted HERE — once, behind those checks — and stored in
the cached result (`DiscoveryResult.Labels`), so prompt-build never opens candidate
files. The configured data directory reaches the walk's skip set via
`config.EffectiveContextDiscovery()`, the single accessor both discovery call sites
(manifest and wrapper state) must use.

The walk result is cached once per `workDir` (paths + labels + truncation flag).
Root-level matches remain the job of scoped resolution (D4); this set only contains
files STRICTLY below the root (depth ≥ 1).

### D7. Manifest block

When ≥1 nested file was discovered, append a compact, cache-stable section to the
system prompt listing one line per file: relative-to-workDir path + a short label
(YAML frontmatter `description`, else first markdown heading, truncated to ~120 chars;
path only if neither exists). The block carries one sentence explaining that bodies
are not loaded and arrive automatically on first directory touch. Absent entirely when
nothing was discovered — zero prompt delta for repos without nested context files.

The manifest is computed at prompt-build time purely from the process-level discovery
cache — paths AND labels; no disk reads — so byte-stability is structural: editing a
nested file mid-session cannot change the manifest, satisfying D3 without special
treatment. A per-manifest line and total-byte cap (overflow: paths-only, then trailing
"N more" line) prevents adversarially large subtrees from bloating the manifest itself.

Files the agent's/step's own scoped `context.paths` layers already inline (exact
entries and trailing-slash subtrees, with the same layer-participation rule as
resolution — a layer dropped by a higher `replace` does NOT subtract) are filtered
out of BOTH the manifest and the disclosure wrapper state via one shared,
deterministic helper (`contextfile.FilterDiscovered`), keeping the two surfaces in
agreement: a body already in the prompt is never listed as "not loaded" and never
injected a second time.

### D8. Activation trigger and owner resolution

Trigger tools: `read` / `write` / `edit` / `multiedit` → `file_path` (parent
directory); `patch` → file paths parsed from the `*** Add/Update/Delete File:`
section headers of `patch_text` (the tool has no `file_path` parameter; a single call
may touch several directories); `grep` → `path` (when set; no `path` in a grep call
resolves to `workDir` and activates nothing); `glob` → `path` + literal directory
prefix of the pattern; `ls` → `path`. Path extraction reuses and extends the existing
`tools.ExtractPathsFromCall` (`internal/llm/tools/tools.go:167`), which already parses
`patch_text` sections and the generic `file_path`/`path` params; the glob
pattern-prefix and grep-without-path rules are added on top. The `delete` tool is
deliberately excluded: removing files is not working-within-a-subtree that benefits
from its instructions. The `bash` tool is deliberately excluded (see proposal.md
Non-Goals): scanning command strings for path tokens produces too many false
positives and false negatives.

Activation criterion: the tool's resolved **directory** (parent of a file arg, or the
arg itself for directory-taking tools) must be strictly equal to or inside a nested
context file's owning directory. Owner resolution: walk from the target directory up
to (but excluding) `workDir`, collect every nested context file on the upward path,
inject not-yet-injected ones outermost-first — reproducing Claude Code's additive
layering without mutating the system prompt. Owner matching canonicalizes BOTH sides
(discovered dirs and the model-supplied target) with the resolver-dedup normalization
(`EvalSymlinks` on the deepest existing ancestor + lowercase): macOS/Windows default
filesystems are case-insensitive, so the inner tool call succeeds with a
differently-cased path while a byte-exact comparison would silently skip the
injection.

### D9. Injection mechanism: `contextDisclosureWrapper`

A `contextDisclosureWrapper` in the toolset assembly (`internal/llm/agent/tools.go`)
wraps ONLY the trigger tools and ONLY when the discovery set is non-empty — zero
allocation and zero behavior change otherwise. It mirrors the `DeferredWrapper`
pattern only in that recognition (where needed) happens by type assertion and no new
`BaseTool` interface method is added — providers never see or need the disclosure
wrapper, since it delegates `Info()`. Wrap order is an invariant: disclosure wrapping
is applied INSIDE deferral — `maybeDefer(maybeWrapDisclosure(t))` — so
`*tools.DeferredWrapper` stays outermost and the four existing type assertions
(`anthropic.go:588`, `agent.go:2235`, `agent.go:2353`, `agent/tools.go:328`) keep
working for a tool that is both deferred and a trigger. The wrapper: extracts the path
parameter from the serialized tool call, calls the inner tool, and on SUCCESS appends a
`<system-reminder>`-tagged block to the tool result content carrying the `# From:<abs-path>`
header and the file body. Literal `<system-reminder>` / `</system-reminder>` strings
inside the body are defused before wrapping (backslash after `<`, content preserved):
a repo file must not be able to close the reminder early and pass its remainder off
as genuine tool output, or forge a reminder with a fabricated `# From:` header. On
failure the tool result is returned unchanged.

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

The activation read itself is bounded and re-verified — the discovery cache is
process-lifetime and the filesystem can change under it. Per candidate: `Lstat` first
(rejects a post-discovery symlink swap, FIFO, or device WITHOUT opening — opening a
FIFO read-only blocks forever); re-check symlink-resolved workDir containment; then
`Open` + `Stat` on the opened descriptor (no TOCTOU on size/type) and read through
`io.LimitReader(f, maxFileBytes+1)` so an under-reporting `Stat` can never smuggle an
unbounded read past the cap (a link to `/dev/zero` stats as size 0). All of this runs
under the shared disclosure mutex, so bounding the read also bounds how long
concurrent trigger tools can be stalled.

### D11. Per-session activation state

ONE shared per-toolset disclosure-state object (mutex + `sessionID → set[absPath]` +
`sessionID → injectedBytes`), created once in `NewToolSet` and passed by pointer to
every trigger-tool wrapper — mirroring how all `DeferredWrapper`s of one toolset share
a single `deferSeq` counter (`WrapDeferred`, `deferred.go:33`). Per-wrapper state
would break cross-tool dedup (a `read` fires the injection; a `grep` on the same
directory must not re-inject) and double-count the byte budget. Sessions must not
observe each other's activations.
Subagent sessions get their OWN activation set and do NOT inherit the parent's — a
subagent that never touches a directory must not pay for its context. After a process
restart, duplicate injection is accepted (same tradeoff the deferred-tools change
accepted: self-correcting, bounded cost, negligible versus the complexity of persisting
activation sets).

### D12. Config surface and naming

New `AgentContext` type — defined in `internal/contextfile`, referenced as
`config.Agent.Context` and `AgentInfo.Context` (yaml frontmatter):
```
context:
  paths: ["AGENTS.runtime.md"]
  mode: replace              # replace | append
  nested: false              # bool, default true
```
New top-level `Config.ContextDiscovery *contextfile.DiscoveryConfig`:
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
discovery walk finds). The budget test — `TestBasePromptBudgets` in
`internal/llm/prompt/prompt_budget_test.go:17-36` — measures ONLY the base prompt
constructors (`CoderPrompt()`, `WorkhorsePrompt(...)`, `HivemindPrompt(...)`,
`ExplorerPrompt(...)`), never the assembled prompt, so the manifest is exempt by
construction — the same way every other appendix (including the deferred-tools
`<system-reminder>` block) already is. No budget-test change is required for
budget-safety; an acknowledging comment in `prompt_budget_test.go` is optional. Only
NEW tests that assert on full assembled prompts (e.g. the byte-identical
backward-compat test) must guard discovery off when the test workspace could contain
nested context files.

### D14. Flow-step `context` field is inheritable

Adding `Context *contextfile.StepContext` to `Step` makes it inheritable through
`include`/`extends` for free: the merge is reflection-driven over declared raw-YAML
keys (`mergeTemplates`, `include.go:259`); a field added to `Step` needs no code
change in `mergeTemplates`. `Context` MUST NOT be
added to `nonInheritableStepKeys` — per-flow context is exactly the kind of thing
template files should be able to supply, and the orchestrator never reads the `context`
field (it reads `id`, `interactive`, `interaction`, `resume_after`). Resolution reads
`step.Context` in `runStep` (`service.go:461`) and passes a `contextfile.StepContext`
into agent construction (`NewAgent` call at `service.go:513`). The `${flow.id}` and
`${flow.step}` template tokens are populated explicitly from `f.ID` and `step.ID` —
both in scope at the call site — NOT from `FlowIDContextKey`/`FlowStepIDContextKey`:
those ctx values are set later (`service.go:654-656`) on the Run context for
telemetry only, and `NewAgent` is context-free (`factory.go:207-208` discards its
ctx), so they are invisible at prompt-build time.

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
