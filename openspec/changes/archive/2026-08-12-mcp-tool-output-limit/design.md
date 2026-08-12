# Design — Cap MCP Tool-Call Output Size

## Context

- The per-tool-call MCP config is per **server** (`config.MCPServer`), not per individual tool. The existing timeout override (`CallToolTimeoutSeconds`) sits there and is applied in `internal/llm/agent/mcp-tool.go` via `resolveCallToolTimeout` at the single call site (`mcpTool.Run` → `runTool`). The new size cap mirrors this exactly.
- MCP output is captured in `runTool` (`internal/llm/agent/mcp-tool.go`) and returned via `tools.NewTextResponse`, which already runs the global `validateAndTruncate` backstop (`MaxToolResponseTokens = 300_000`).
- The bash tool already implements the desired UX in `internal/llm/tools/tempdir.go` + `bash.go`: `persistToTempFile` writes the full output to `os.TempDir()/opencode-<pid>/`, `buildPreview` makes a head+tail preview, `buildTruncationHeader` emits the "Full output saved to: <path>" marker. The agent is told (bash tool description) to `grep`/`read`/`sed` the file.

## Goals / Non-goals

- Goal: bound a single MCP tool call's context footprint by default, configurable per server, without losing data (full output on disk, agent can retrieve it).
- Goal: reuse the bash temp-file mechanism; do not invent a parallel spill path.
- Non-goal: changing individual MCP servers (e.g. teamcity) to emit smaller output. This is the general, server-agnostic safety net.
- Non-goal: touching the global `MaxToolResponseTokens` backstop (it remains, on top).

## Key decisions

### 1. Per-server config, mirroring the timeout

Add `CallToolMaxOutputBytes int` to `config.MCPServer` next to `CallToolTimeoutSeconds`. Resolution (`resolveCallToolMaxOutputBytes`):

- `> 0` → use it (raise or lower the cap).
- `< 0` → unlimited (returned as `-1`); lets an operator intentionally allow a large payload for a server that needs it.
- `0` / omitted → built-in default `mcpCallToolMaxOutputBytes = 50 * 1024` (50KB).

Rationale for 50KB default: it matches the bash tool's proven `MaxOutputBytes` threshold and reuses the same preview machinery; ~50KB is ~12.5k tokens, a small slice of even a 200K-context model, while still generous for typical structured responses. It is a per-server config away from any value an operator prefers, so the choice is low-risk and reversible.

### 2. Byte-aware preview (not line-aware)

The bash `buildPreview` is line-based; it would fail to bound a single-line minified-JSON MCP response (one "line" = the whole payload). The new `PersistLargeOutput` uses a **byte-based** head+tail preview (`buildBytePreview`) sized to ~`maxBytes` total (head = `maxBytes/2`, tail = `maxBytes/2`), so the agent still gets ~a cap's worth of context and the discontinuity at the threshold is small.

Cut points are snapped to UTF-8 rune boundaries (`toRuneBoundaryBackward`/`Forward`, using `utf8.RuneStart`) and to a nearby newline when one exists, so previews never split a rune or a line mid-way. This matters because MCP payloads are frequently UTF-8 JSON/text.

### 3. Reuse, and one small correctness fix

`PersistLargeOutput` reuses `persistToTempFile` (same scratch dir, 0600, 100MB on-disk cap, shutdown cleanup) and emits a header analogous to `buildTruncationHeader` but byte-oriented and with grep/read/sed guidance. The header names the file path so the agent can explore it with existing tools; no new tool is needed.

While wiring the cap, `runTool`'s content-capture loop is changed from "keep the last block" (`output = v.Text` each iteration — a latent bug that dropped data for multi-block results) to concatenating all blocks, so the cap sees and preserves the full output.

Because the temp-file prefix now embeds a **server-supplied** string (the MCP tool name) — unlike bash, which only ever passes fixed labels — `persistToTempFile` sanitizes the prefix to a single safe path component (`[a-zA-Z0-9._-]`, length-capped, `sanitizeFilePrefix`) so a hostile tool name like `a/../../evil` cannot direct the write outside the scratch dir. File creation goes through `os.CreateTemp` (unique suffix, atomic, 0600), so concurrent spills — MCP tools allow parallelism — can never overwrite each other, which the previous `UnixNano`-only naming could. Each spill is logged (`logging.Info`: tool, total bytes, cap, path) so cap activations are visible when diagnosing context-pressure incidents like the one that motivated this change.

### 4. Ordering vs. the global backstop

`runTool` applies the per-server cap first (producing a small preview) and then calls `NewTextResponse`, whose `validateAndTruncate` backstop is now effectively a no-op for capped output. For the unlimited case (`callToolMaxOutputBytes < 0`) the global 300K-token backstop still protects against catastrophic sizes.

## Risks / mitigations

- Truncating structured JSON breaks inline parsing for a caller that needed the whole object. Mitigation: the full output is on disk and the agent is explicitly told to read/grep it; and any server that must keep large results inline raises or disables the cap per server.
- Behavior change for existing setups relying on 50KB–1.2MB inline results. Mitigation: additive, per-server, documented; default chosen conservatively; trivially overridable.
