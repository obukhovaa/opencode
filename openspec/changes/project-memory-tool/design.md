## Context

The paired Piano Developer `unified-project-memory` change defines the aggregate and import/export CLI. OpenCode uses explicit tool grants, per-tool permissions, multiple providers and multiple sessions/processes. The Anthropic SDK already supports `MemoryTool20250818Param`.

## Goals / Non-Goals

Goals: match the existing memory tool's names and parameters, keep data durable across sessions sharing a filesystem, and expose project-scoped Markdown with a configurable storage boundary.

Non-goals: session-store migrations, remote synchronization inside the model tool, inferred tenant authorization, or mandatory memory for existing agents.

## Decisions

1. `memory.enabled` defaults false and `memory.directory` defaults `~/.agents/memory`. The tool additionally requires an explicit `memory` grant. `/memories` maps to that configured root; project directories follow the paired format. A deployment can narrow the root or separate roots by tenant. Current-project routing is derived from canonical origin/explicit `memory.projectID`; aliases use the aggregate map. Working-directory hints do not collapse all target repositories into the harness project.
2. Preserve Anthropic's API exactly: command plus conditional path/view_range/file_text/old_str/new_str/insert_line/insert_text/old_path/new_path. Reject malformed arguments, root deletion/rename, traversal and symlinks. Root listing of an empty store succeeds. `create` refuses an existing file; exact replacement requires one occurrence; insertion uses zero to prepend and otherwise inserts after the specified line. Directory rename/delete are supported within the root.
3. Use bounded file reads, image responses for PNG/JPEG, line-numbered text and 16,000-character text views. Directory views stop at two levels and omit hidden entries and node_modules. Changes are written atomically under the same aggregate lock as mem-sync. Use OS-root-relative operations for confinement and reject non-regular files. Internal metadata/lock files remain inaccessible to model operations.
4. Evaluate the `memory` permission against command plus virtual path, checking both paths on rename. Permission denial occurs before store mutation. Expose the tool only with memory enabled and an explicit grant. Granting read tools alone never grants memory writes.
5. Compatible Claude requests use the native `memory_20250818` union. Other models keep the equivalent function schema. Preserve deferral ordering, cache breakpoints, declared tool names and token-count conversion for the native union. Add short prompt guidance only when the tool is enabled, describing indexes, project boundaries, stale evidence and durable learning capture.

## Risks / Trade-offs

- Cross-process contention → bounded/context-aware locking and atomic writes; a busy store produces a retryable tool error.
- External editors ignore locks → operations are serialized among cooperating writers; updates use narrow exact replacements and create refuses overwrite.
- Models can access every project within their root → treat root selection and filesystem permissions as the isolation boundary and document per-tenant roots.
- Provider compatibility differs → native encoding only for Claude; function fallback elsewhere, with serialization tests.

## Migration Plan

Deploy the runtime, configure a persistent directory, enable memory and grant it to intended agents. No import is automatic. Use the paired CLI to populate the store. Rollback disables memory while retaining Markdown data.
