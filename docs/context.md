# Context Files

OpenCode feeds project instructions (memory files such as `AGENTS.md` or
`CLAUDE.md`) into every agent's system prompt. This document covers the two
mechanisms that control **which** files land there and **when**:

1. **Scoped context resolution** — per-agent and per-flow-step overrides of the
   global `contextPaths` list.
2. **Progressive context disclosure** — context files in *subdirectories* are
   listed in a compact manifest and their bodies delivered on demand, in tool
   results, the first time the agent works inside their directory.

Both are pure opt-in. With no `context` configured anywhere and no nested
context files on disk, the assembled system prompt is **byte-identical** to the
behavior before these features existed.

## Global `contextPaths`

The top-level `contextPaths` list in `.opencode.json` names the candidate
context files, relative to the working directory:

```json
{
  "contextPaths": ["AGENTS.md", "CLAUDE.md", ".cursor/rules/"]
}
```

Resolution semantics (unchanged, and preserved exactly by everything below):

- Each matched file is rendered as `# From:<absolute-path>` followed by its raw
  content; multiple files are joined with a single newline, sorted by absolute
  path.
- Missing files are silently skipped.
- An entry with a trailing `/` recurses into the directory and inlines every
  file found.
- Symlinks and relative spellings of the same file are deduplicated (canonical
  path, case-insensitive).

The rendered block appears in the system prompt under
`# Project-Specific Context`. Content is resolved once per process and cached —
editing a context file requires a restart to pick up.

## Scoped context resolution

### The `context` object

Agents (in `.opencode.json` or markdown frontmatter) and flow steps accept the
same shape:

```json
{
  "agents": {
    "coder": {
      "context": {
        "paths": ["AGENTS.runtime.md"],
        "mode": "replace",
        "nested": true
      }
    }
  }
}
```

```yaml
# flow step
- id: implement
  agent: coder
  context:
    paths: ["AGENTS.${flow.step}.md"]
    mode: append
  prompt: ...
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `paths` | array of strings | — | Context files, relative to the working directory. Same entry semantics as `contextPaths` (trailing `/` recurses, missing files skip silently). Supports the template tokens below. |
| `mode` | string | `replace` | How this layer combines with the layers below it: `replace` discards them, `append` concatenates after them. An unrecognized value logs a WARN once and falls back to `append` (fail-safe: a typo must never silently drop the project's root instructions). |
| `nested` | bool | `true` | Set `false` to opt this agent/step out of the nested-context manifest and tool-result injection (see [Progressive context disclosure](#progressive-context-disclosure)). |

The flow-step `context` field is inheritable through `include`/`extends` step
templates — a template may declare it, and an extending step's own `context`
replaces the template's wholly (shallow merge, like every other step key). See
[Flows](flows.md#shared-step-templates-include--extends).

### Precedence: step > agent > global

Three layers are evaluated from highest to lowest priority:

1. **Step** — `context.paths` on the current flow step.
2. **Agent** — `context.paths` in the agent's config or frontmatter.
3. **Global** — the top-level `contextPaths` list.

A layer is *declared* when it explicitly sets `context.paths`; undeclared
layers are skipped. Evaluation starts at the highest declared layer and its
`mode` decides whether the layers below contribute — and the rule is
**compositional**: each declaring layer carries its own `mode`, so a step with
`mode: append` folds in the agent layer, and then the *agent's* `mode` governs
whether the global layer contributes.

Examples (global `contextPaths: ["AGENTS.md"]` throughout):

| Agent declares | Step declares | Effective files |
|----------------|---------------|-----------------|
| — | — | `AGENTS.md` (today's behavior, byte-identical) |
| — | `paths: [S.md], mode: replace` | `S.md` |
| `paths: [A.md], mode: append` | — | `AGENTS.md` + `A.md` |
| `paths: [A.md], mode: replace` | `paths: [S.md], mode: append` | `A.md` + `S.md` (the agent's `replace` discards the global layer) |
| `paths: [A.md], mode: append` | `paths: [S.md], mode: append` | `AGENTS.md` + `A.md` + `S.md` |

In `append` mode the layers concatenate in global → agent → step order; within
each layer files sort by absolute path, and deduplication applies across the
whole merged set (a file named at two layers appears once). The global
`contextPaths` list itself is never modified by agent or step configuration.

### Template tokens

`context.paths` entries support exactly four substitution tokens, expanded
before filesystem resolution:

| Token | Value |
|-------|-------|
| `${agent}` | Agent ID (e.g. `coder`) |
| `${flow.id}` | Flow ID, or empty outside a flow |
| `${flow.step}` | Step ID, or empty outside a flow |
| `${env.VAR}` | Environment variable `VAR`, or empty |

No shell execution, no globbing, no recursive substitution. An entry that
still contains a literal `${...}` after expansion (unknown token), or in which
a recognized token expanded to an empty value (e.g. `AGENTS.${flow.id}.md` run
outside a flow), is skipped with a DEBUG log — it is never probed on disk.

**Containment**: after expansion, the joined and cleaned absolute path must
stay inside the working directory. Entries that escape (e.g. an `${env.VAR}`
expanding to `../../etc/passwd`) are rejected with a WARN log and never reach
the filesystem.

### Backward compatibility

When no agent and no step declares `context.paths`, the global `contextPaths`
resolve exactly as before this feature existed — the `# Project-Specific
Context` header, `# From:` blocks, ordering, and joining are byte-identical.
The resolved block is memoized per (path set, mode, workDir) and never changes
within a session, preserving the provider prompt-cache prefix.

## Progressive context disclosure

Monorepos carry context files in subdirectories (`services/auth/AGENTS.md`).
Inlining all of them into every prompt is wasteful; ignoring them loses the
instructions. Progressive disclosure does neither:

1. A **discovery walk** finds context files strictly below the working
   directory (never the root-level ones — those belong to scoped resolution).
2. A compact **manifest** in the system prompt lists them — path plus a short
   label — with a note that bodies are not loaded.
3. The **body is injected** into the result of the first tool call that
   touches the file's directory, wrapped in `<system-reminder>` tags with the
   same `# From:<absolute-path>` header. The system prompt is never mutated —
   the prompt-cache prefix survives.

### Discovery

Once per process per working directory, the subtree is walked for files whose
basename matches a file-type entry of the effective `contextPaths`
(trailing-`/` directory entries are excluded — their subtrees keep the
inline-everything semantics). The walk skips hidden (dot-prefixed) files and
directories and common dependency/build directories (`node_modules`, `vendor`,
`dist`, `build`, `target`, …). `.gitignore` is not consulted in v1; the caps
below bound over-discovery.

### The manifest

When at least one nested file was discovered, the system prompt carries a
`# Nested Context Files` section: one line per file with the
relative-to-workDir path and a short label (YAML frontmatter `description`,
else the first markdown heading, truncated to ~120 chars). It is byte-stable
across every turn of a session. On overflow it degrades to paths-only lines,
then to a trailing `... N more files not shown` summary. Repos with no nested
context files get no manifest — zero prompt delta.

### Trigger tools and activation

| Tool | Directory derived from |
|------|------------------------|
| `read`, `write`, `edit`, `multiedit` | parent directory of `file_path` |
| `patch` | parent directory of each file in the `patch_text` section headers |
| `grep` | `path` when set; **no `path` activates nothing** |
| `glob` | `path` plus the literal directory prefix of the pattern |
| `ls` | `path` itself |

`bash` is deliberately **not** a trigger (scanning command strings for paths is
too noisy), and neither is `delete` (removing files is not working within a
subtree). Activation fires only when the call **succeeded** and its resolved
directory is at or below a discovered file's owning directory — the working
directory itself never activates anything. When several context files own
nested directories on the path (`services/AGENTS.md` and
`services/auth/AGENTS.md`), all not-yet-injected ones are delivered
outermost-first in the same tool result.

Each session injects a given file **at most once**, across all trigger tools —
a `read` that fired the injection means a later `grep` on the same directory
stays clean. Subagent sessions have their own activation state and do not
inherit the parent's. After a process restart a session may receive a
duplicate injection on its next touch; this is accepted (bodies are
idempotent).

Failure isolation: an unreadable, deleted, or oversized (`maxFileBytes`)
nested file is skipped with a WARN log and the tool result is returned
unchanged — disclosure never fails a tool call. When a session's injected
bodies reach `maxSessionBytes`, one INFO log records the exhaustion and no
further bodies are injected for that session.

### Configuration and opt-out

Top-level `contextDiscovery` object:

```json
{
  "contextDiscovery": {
    "enabled": true,
    "maxFiles": 100,
    "maxDepth": 8,
    "maxFileBytes": 32768,
    "maxSessionBytes": 131072
  }
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `true` | `false` disables discovery, manifest, and injection for all agents. |
| `maxFiles` | 100 | Discovery-set cap; the walk stops and the manifest notes the truncation. |
| `maxDepth` | 8 | Maximum directory depth below the working directory. |
| `maxFileBytes` | 32768 | A nested file larger than this is never injected (WARN-logged skip). |
| `maxSessionBytes` | 131072 | Total injected bytes per session; exhaustion stops further injection. |

Per-agent or per-step opt-out without touching global discovery: set
`context.nested: false` on the agent or step. That agent/step gets neither the
manifest nor body injections, while other agents still do.
