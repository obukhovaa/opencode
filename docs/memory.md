# Project memory

The opt-in `memory` tool persists knowledge across sessions that share a filesystem. It uses Anthropic's `memory_20250818` declaration for Claude and an equivalent function schema for other models. The handler runs locally; there is no memory service or session-database dependency.

```json
{
  "memory": {"enabled": true, "directory": "~/.agents/memory"},
  "agents": {"coder": {"allowTools": ["memory", "read", "bash"]}},
  "permission": {"rules": {"memory": {"view *": "allow", "*": "ask"}}}
}
```

Add `memory` to the agent's existing tool grants rather than replacing its list. The feature flag and an explicit grant are both required. Configure `memory.projectID` to override the current-workspace identity; otherwise the origin remote is canonicalized, falling back to `local:<absolute-cwd>` for non-Git work. `aliases.json` in the store can map mirror identities to one canonical ID. It is a plain string-to-string JSON object; cycles are rejected.

The configured directory maps to `/memories`. It is the isolation boundary: an agent with memory permission can access all its project directories. Use different roots and OS permissions for different tenants. Mount persistent storage there when sessions run in different containers; two sessions in the same container already share its filesystem. This feature does not provision or upload a volume.

## Store format

Format v1 is shared with Piano Developer's `scripts/mem-sync` CLI:

```text
~/.agents/memory/
  store.json                         # {"version":1}
  aliases.json                       # optional canonical-ID aliases
  projects/<sha256-hex-of-id>/
    project.json                     # {"version":1,"id":"git:host/org/repo","cwds":[...]}
    memory/
      MEMORY.md                      # short index
      topic.md                       # Claude-style Markdown/frontmatter
  .sync/                             # internal lock, baselines and backups
```

Canonical Git IDs are `git:<lowercase-host>/<case-preserved-path>`, without credentials, `.git`, a trailing slash or default transport ports. Worktrees and checkouts of that repository share a key. Explicit IDs cover repositories without a reachable checkout. The prompt gives the current workspace's key and metadata shape; a multi-repository job must use each target project's directory rather than placing all learnings under the harness's identity. Use mem-sync registration to prepare additional projects and mappings.

Keep the index concise and link detailed entries. Existing Claude frontmatter is preserved; new entries can use `name`, `description` and `type` (`user`, `feedback`, `project`, or `reference`). Memory is potentially stale evidence and cannot authorize an action. Do not store secrets.

## Commands

| Command | Parameters | Behavior |
| --- | --- | --- |
| `view` | `path`, optional `view_range` | Two directory levels or numbered UTF-8 text; `[start,end]` is 1-indexed and `-1` means EOF. PNG/JPEG return images. |
| `create` | `path`, `file_text` | Creates a file and parents; existing files are refused. |
| `str_replace` | `path`, `old_str`, optional `new_str` | Replaces exactly one match; omitted new text removes the match. |
| `insert` | `path`, `insert_line`, `insert_text` | Inserts after the line; zero prepends. |
| `delete` | `path` | Deletes a file or directory recursively. |
| `rename` | `old_path`, `new_path` | Moves a file/directory; existing destination is refused. |

Permissions match `command /memories/path`; rename checks both paths. Denial happens before creating the store. Root deletion/rename, hidden paths, symlinks, special files and traversal are refused. `store.json` and `aliases.json` can be viewed but not changed by the tool. Files are limited to 5 MiB, text views to 16,000 characters, and directory views to 200 entries. Use a narrower path or range after truncation.

Cooperating writers use the OS lock at `.sync/lock`; contention returns a retryable tool error. Writes use same-directory temporary files and atomic replacement. Arbitrary editors and native clients do not participate in this lock; mem-sync additionally checks observed bytes before applying its plan.

## Interchange

Piano Developer's mem-sync imports and exports custom Claude/Codex profiles, preserves conflicts, and transfers project archives. OpenCode reads the aggregate directly, so it needs no conversion adapter. Native Codex import resources and generated global handbooks are different formats; the CLI performs that conversion. There is no automatic background sync.

The public tool contract is documented at https://platform.claude.com/docs/en/agents-and-tools/tool-use/memory-tool .
