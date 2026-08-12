# Cap MCP Tool-Call Output Size

## Why

An MCP tool call can return an arbitrarily large payload, and today nothing bounds it per call before it enters the model context. The only guard is a global per-response backstop (`MaxToolResponseTokens = 300_000`, ~1.2MB) that truncates head-only with no way to recover the rest — so a single tool result can consume ~1.2MB of context, and a few of them overflow the window entirely.

This bit a production review job (`piano-gitlab-andy`, `eu-claude-sonnet-5`, opencode v0.14.6): the `check-pipelines` step fetched two CI build logs of ~1.2MB each via `teamcity-vx_fetch_build_log`. Context grew to 664,693 input tokens (that generation succeeded); the second build log then pushed the next request past the model's 1M context window. The backend rejected it (input too long), the LiteLLM proxy surfaced that as an HTTP/2 `RST_STREAM INTERNAL_ERROR`, and opencode retried it as a transient stream error until the budget was exhausted — the step "failed with no reason". Any tool that returns a big payload (build logs, large diffs, verbose API responses) can trigger this.

The bash tool already solves the same problem well: when output exceeds a threshold it spills the full content to a temp file and returns a head+tail preview that points the agent at the file, which it explores with `grep`/`read`/`sed`. We should give MCP tool output the same treatment, with a per-server size cap that mirrors the existing per-server `callToolTimeoutSeconds`.

## What Changes

- New per-server MCP config field `callToolMaxOutputBytes` (sibling of `callToolTimeoutSeconds`) on `MCPServer`, with a sane built-in default (50KB), a positive override to raise/lower it, and a negative value to disable the cap (intentional unbounded output).
- `runTool` concatenates all returned content blocks (fixing a pre-existing bug where only the last block survived) and applies the resolved cap. Oversized output is spilled to the per-process scratch dir and replaced with a compact head+tail preview plus a header naming the byte size and file path and instructing the agent to `grep`/`read`/`sed` it — reusing the bash tool's temp-file infrastructure.
- A new exported, byte-aware truncate-and-spill helper (`PersistLargeOutput`) in the `tools` package. Byte-based (not line-based) so it also bounds single-line payloads such as minified JSON, and rune-boundary-safe so previews never split a UTF-8 character. Spill file names sanitize the server-supplied tool name (no path traversal), are collision-safe under concurrent calls (`os.CreateTemp`), and each spill is logged.
- Config JSON schema (`opencode-schema.json` via `cmd/schema/main.go`) and README gain the new setting.

The existing global `MaxToolResponseTokens` backstop is unchanged and still applies on top.

## Capabilities

### New Capabilities

- `mcp-tool-output-limit`: a configurable per-server cap on a single MCP tool call's output kept in the model context, with overflow spilled to a temp file and replaced by a head+tail preview the agent can explore with its existing tools.

### Modified Capabilities

<!-- none — no existing spec covers MCP tool-call result handling -->

## Impact

- Modified: `internal/config/config.go` (`MCPServer.CallToolMaxOutputBytes`), `internal/llm/agent/mcp-tool.go` (default const, `resolveCallToolMaxOutputBytes`, `runTool` concatenation + cap), `internal/llm/tools/tempdir.go` (exported `PersistLargeOutput` + byte-preview + rune helpers), `cmd/schema/main.go` + regenerated `opencode-schema.json`, `README.md`.
- `.opencode.json` public contract: additive `mcpServers.<name>.callToolMaxOutputBytes` field.
- Behavior change: MCP tool results larger than 50KB (default) are now previewed + spilled to file rather than passed inline in full. Servers whose large structured output must stay inline can raise `callToolMaxOutputBytes` or set it negative. No new runtime dependencies.
