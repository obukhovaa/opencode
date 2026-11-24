## Why

`listingScope` (v0.34.0) lists a skill or subagent type in full only when its file location, relative to the working directory, matches a pattern. A location outside the working directory never matches, so such items drop to names-only on every scoped run. Those items are installation-level, not per-team, and patterns cannot reach them. The Piano agent image installs the `agent-browser` skill globally (`~/.agents/skills`), and QA UI runs depend on it. Found in review of the workspace MR (piano-developer !488) before rollout.

## What Changes

- Skills and subagent types whose file lies **outside the working directory** are treated as shared and always listed in full, like agents defined in `.opencode.json`, which have no file.
- Only items inside the working directory are scoped by the patterns. Unscoped behaviour is unchanged.

## Capabilities

### New Capabilities

### Modified Capabilities
- `run-scoped-listings`: items outside the working directory are listed in full under an applying scope.

## Impact

`internal/llm/tools/listing_scope.go` (the matcher), its tests, `docs/skills.md`. No config or schema change.
