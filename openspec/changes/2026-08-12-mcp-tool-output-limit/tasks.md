## 1. Reusable truncate-and-spill helper

- [x] 1.1 In `internal/llm/tools/tempdir.go`, add exported `PersistLargeOutput(content, label, source string, maxBytes int) (preview, filePath string)`: return content unchanged with empty path when `maxBytes <= 0` or content is within the cap; otherwise spill the full content to the process scratch dir via the existing `persistToTempFile` and return `buildOutputOverflowHeader(...) + buildBytePreview(...)` plus the file path.
- [x] 1.2 Add `buildBytePreview(content string, headBytes, tailBytes int) string`: byte-aligned head+tail with an elided-bytes marker; snap cut points to UTF-8 rune boundaries (`toRuneBoundaryBackward`/`Forward` using `utf8.RuneStart`) and to a nearby newline when present.
- [x] 1.3 Add `buildOutputOverflowHeader(label string, totalBytes int, filePath string) string`: `<label output truncated: N bytes total>` + `Full output saved to: <path>` + grep/read/sed guidance.
- [x] 1.4 Unit tests (`tempdir_test.go`): under-cap passthrough (no file); `maxBytes<=0` unlimited; over-cap spills to a readable file holding the full content and returns header + head + tail + marker; single-line (minified-JSON) payload is still bounded; preview is valid UTF-8 across a multi-byte cut; `buildBytePreview` head/tail/marker.

## 2. Per-server config field

- [x] 2.1 Add `CallToolMaxOutputBytes int` (json `callToolMaxOutputBytes,omitempty`) to `config.MCPServer` in `internal/config/config.go`, documented (default / positive override / negative disables).
- [x] 2.2 Add the field to the hand-authored config schema in `cmd/schema/main.go` next to `callToolTimeoutSeconds`, and regenerate `opencode-schema.json`.

## 3. Apply the cap in the MCP tool path

- [x] 3.1 In `internal/llm/agent/mcp-tool.go`, add default const `mcpCallToolMaxOutputBytes = 50 * 1024` next to `mcpCallToolTimeout`.
- [x] 3.2 Add `resolveCallToolMaxOutputBytes(config.MCPServer) int` mirroring `resolveCallToolTimeout`: positive → value; negative → `-1` (unlimited); else default.
- [x] 3.3 Thread the resolved cap from `mcpTool.Run` into `runTool` (new `maxOutputBytes` parameter).
- [x] 3.4 In `runTool`, concatenate all content blocks (replacing the last-block-wins loop) and apply `tools.PersistLargeOutput(..., maxOutputBytes)` before `NewTextResponse`.
- [x] 3.5 Unit tests (`mcp-tool_test.go`): `resolveCallToolMaxOutputBytes` (default / positive / negative); `runTool` with a fake `MCPClient` — small output unchanged, oversized output capped + spilled (header present, not an error), multi-block concatenated when the cap is disabled.

## 4. Docs

- [x] 4.1 Document `callToolMaxOutputBytes` (and `callToolTimeoutSeconds`) as optional per-server settings in the README MCP section.

## 5. Verification

- [x] 5.1 `go build ./...` is clean.
- [x] 5.2 `go vet ./internal/llm/tools/ ./internal/llm/agent/ ./internal/config/` is clean.
- [x] 5.3 `go test ./internal/llm/tools/ ./internal/llm/agent/ ./internal/config/` passes.
- [x] 5.4 `openspec validate 2026-08-12-mcp-tool-output-limit --strict` passes.
