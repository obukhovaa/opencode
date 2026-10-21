## Why

OpenCode sessions sharing a filesystem currently cannot capture and retrieve durable project memory through the model's established memory-tool interface. A native tool backed by the same portable aggregate as local Claude and Codex enables knowledge reuse across sessions and runtimes.

## What Changes

- Add the `memory` tool with Anthropic's `memory_20250818` command names and parameters, mapped onto a configurable filesystem store.
- Implement view, create, string replacement, insertion, deletion, and rename with path confinement, bounded output, atomic updates, and cross-process coordination.
- Describe the current project and Claude-style index convention to enabled agents while retaining explicit tool grants and permissions.
- Emit Anthropic's native memory tool on compatible Claude requests and an equivalent function schema on other providers, including token-count and cache handling.
- Publish configuration/schema documentation and compatibility fixtures for the paired Piano Developer `unified-project-memory` change.

## Capabilities

### New Capabilities
- `project-memory`: Memory tool API, filesystem persistence, project discovery, configuration, permission enforcement, and provider integration.

### Modified Capabilities

None.

## Impact

Touches configuration/schema, tool registration, provider serialization, prompt guidance, documentation and tests. Memory remains opt-in. This change does not create an external memory service or alter session SQLite/MySQL persistence.
