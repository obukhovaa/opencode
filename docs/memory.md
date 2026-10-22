# Project memory through ordinary file tools

Enable project memory to carry useful knowledge across sessions sharing a filesystem. OpenCode adds a small system reminder and a bounded project index. It adds no memory tool or provider-specific tool schema.

```json
{
  "memory": {"enabled": true, "directory": "~/.agents/memory"},
  "agents": {"coder": {"allowTools": ["read", "write", "edit", "bash"]}}
}
```

Keep each agent's existing grants; do not replace its list with this example.

To keep separate stores per profile, select the root per launch with `AGENTS_MEMORY_HOME`, the way `CLAUDE_CONFIG_DIR` selects a Claude Code profile: `AGENTS_MEMORY_HOME=~/.agents/memory-work opencode`. A non-empty value overrides `memory.directory`; otherwise `memory.directory`, then `~/.agents/memory`, applies. mem-sync reads the same variable for its default `--store`, so both address the same store.

There are two capability levels, and the reminder adapts to the agent's grants and permissions:

- **Read-only** — `read`, `grep` or permitted Bash can read the index: retrieval guidance only.
- **Read+write** — writing always comes with reading, so a writer gets both the retrieval and the maintenance guidance. `write`, `edit`, `multiedit` and `patch` count as memory writers only alongside the `read` tool, because they refuse to change an existing file the `read` tool has not recorded a read of; permitted Bash reads and writes on its own.

Writing without reading is not supported: file write tools without the `read` tool leave an agent read-only when `grep` or Bash can read memory, and without any reminder when nothing can; an agent without relevant capabilities gets none either. Denied or approval-required reads are never performed automatically; the agent can use its ordinary tools and approval flow where allowed. Automatic loading verifies the opened index against its permission-checked canonical path, so replacing the store root cannot redirect a snapshot to another file.

Writers are told today's UTC date so `updatedAt` has a clock source in every agent mode; Bash writers are also given `date -u +%Y-%m-%dT%H:%M:%SZ` for the time of day.

## Context budget

Only `MEMORY.md` for the current project is loaded automatically: at most **200 lines or 25 KiB**, whichever is reached first. A synthetic context message carries one escaped snapshot per active session history. Later turns reuse it; a new session reads the current index, and compaction reloads it if the old snapshot was removed. The system reminder remains stable rather than embedding changing note bodies into the provider prefix. The snapshot participates in ordinary message token accounting.

Topic files are never preloaded. The model chooses relevant links and reads those files or sections through ordinary tools. Treat the index as a snapshot and re-read the current file before editing or when freshness matters. Large indexes carry an explicit truncation notice; files are never destructively truncated by the loader.

Existing write/edit/multiedit/patch tools append feedback when an index exceeds its budget or a topic exceeds **8 KiB**, lacks a description, or has an invalid updatedAt. The underlying write succeeds and the tool schema stays unchanged. This is an authoring budget, not a filesystem quota: arbitrary Bash commands retain their existing permissions and cannot be constrained by a new memory-specific tool limit. The reminder instructs Bash-capable agents to follow the same budgets.

## Notes and freshness

Use a short retrieval index:

```markdown
- [Deployment](deployment.md) — Release checks and rollback links; updatedAt: 2026-10-21T12:00:00Z
```

Each topic has minimal frontmatter:

```markdown
---
description: Release checks and rollback links
updatedAt: 2026-10-21T12:00:00Z
---
- Run the release checks in [the runbook](https://example.com/runbook).
- Roll back to the last verified image if the health check fails.
```

Record facts, short sentences, user corrections, and evidence links. Omit preambles, transcripts, raw dumps, duplication, secrets and facts readily recovered from source code. Split large topics; keep one index line per topic. Preserve useful legacy metadata.

`updatedAt` means the last factual update, not the last read, copy or import. Review old volatile claims and reconcile contradictions against current evidence. Age alone does not make stable knowledge false. Update or retire superseded claims and their index entries; do not make a note look fresh merely by touching its timestamp.

## Project scope and interchange

```text
~/.agents/memory/
  store.json                         # mem-sync's format/source identity
  aliases.json                       # optional identity aliases
  projects/<sha256-hex-of-id>/
    project.json                     # {"version":1,"id":"git:host/org/repo","cwds":[...]}
    memory/MEMORY.md
    memory/topic.md
  .sync/                             # mem-sync bookkeeping; not prompt context
```

Current identity comes from the Git origin, or explicit `memory.projectID`. Canonical Git IDs remove credentials, transport, `.git`, trailing slash and default ports while preserving repository-path case. Worktrees share that identity. `aliases.json` maps mirror IDs to a canonical ID; cycles are rejected. Non-Git work falls back to `local:<absolute-cwd>`; use an explicit ID for portability.

Multi-repository harness jobs must select each target project's directory, not treat the harness index as global knowledge. The reminder tells agents to find another repository's directory by its `id` in `projects/*/project.json`; for a repository with no directory yet, Bash writers compute the key (`printf %s '<id>' | sha256sum`) and other agents report the facts instead of saving them into the current project. Ordinary file permissions and OS access remain authoritative. Separate tenants with separate roots/permissions. A shared persistent mount is required across pod replacement; no network service or volume is provisioned.

Piano Developer's `scripts/mem-sync` imports/exports custom Claude and Codex profiles, retains conflicts, lints authoring budgets and transfers archives. Existing notes are preserved, including legacy metadata. Ordinary OpenCode file/Bash writers do not take mem-sync's internal lock: re-read before changing files and avoid simultaneous writes to the same note. Existing file-tool conflict checks and sync baselines remain in use.

If the home directory of a `~` root cannot be resolved, or the project identity cannot be (a malformed `aliases.json`, an alias cycle), memory is unavailable and a warning is logged once per process; the store is never relocated into the working directory.

Disable `memory.enabled` to stop future reminders/index loads. Existing session history and Markdown are retained.

## Practices adopted

- [Claude Code memory](https://code.claude.com/docs/en/memory): bounded index at startup, detailed notes read on demand, and feedback after oversized writes.
- [Karpathy's LLM Wiki](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f): concise linked retrieval cues, synthesis kept current, and checks for contradictions/orphans. This implementation deliberately keeps only an index and topic notes in the model-facing layer.
- [Anthropic context engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents): use compact references and existing tools to load information only when needed.
