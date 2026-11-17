## Why

GENAI-671 found that the Go fork already implements deferred schemas and parallel calls, while intermediate MCP results still enter model context and dependent reads require additional model decisions. A bounded composition pilot can test whether selecting required facts before returning tool output yields meaningful savings on our Claude workflows.

## What Changes

- Add an opt-in, bounded sequential MCP read-plan tool with literal arguments, earlier-result bindings and JSON-pointer projections.
- Preserve existing per-tool grants, permissions, hooks, ownership checks and cancellation through a shared dispatch path.
- Preserve MCP error flags and explicitly reject outputs that cannot be safely projected after existing hooks.
- Add deterministic parity and context-volume fixtures before any single-agent pilot. Default toolsets and provider payloads remain unchanged.
- Exclude a general scripting runtime, write tools, background compaction and cache warming.

## Capabilities

### New Capabilities

- `mcp-read-composition`: Compose a bounded set of authorized MCP reads while returning selected facts with provenance and explicit incompleteness.

### Modified Capabilities

None.

## Impact

Agent tool dispatch, MCP result handling, explicit tool/config grants and schema generation, tests and documentation. This proposal is the second GENAI-671 wave; it is not evidence of measured production savings and must not be globally enabled before evaluation.
