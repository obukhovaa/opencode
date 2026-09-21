## 1. Configuration

- [ ] 1.1 Add `WebFetchConfig struct { MaxOutputBytes int \`json:"maxOutputBytes,omitempty"\` }` to `internal/config/config.go` and a `WebFetch *WebFetchConfig \`json:"webFetch,omitempty"\`` field on `Config`, documented (default / positive override / negative disables), placed next to `WebSearch`.
- [ ] 1.2 Add the `webFetch` object to the hand-authored schema in `cmd/schema/main.go` next to the `webSearch` block, and regenerate `opencode-schema.json` (`make schema`).
- [ ] 1.3 Config unit test covering `viper.Unmarshal` of a `webFetch` block (unset / positive / negative).

## 2. Apply the cap in the webfetch path

- [ ] 2.1 In `internal/llm/tools/webfetch.go`, add default const `webFetchMaxOutputBytes = 50 * 1024` and `resolveWebFetchMaxOutputBytes(cfg) int` mirroring `resolveCallToolMaxOutputBytes` (positive → value; negative → `-1`; else default).
- [ ] 2.2 Collapse the four `NewTextResponse` returns in the `switch format` block into one: each branch computes the formatted `out string` (keeping the existing early error returns for conversion failures), then a single tail applies `tools.PersistLargeOutput(out, "webfetch", "webfetch", cap)` and returns.
- [ ] 2.3 Log each spill (`logging.Info`: url, format, totalBytes, maxOutputBytes, file).
- [ ] 2.4 Update `fetchToolDescription` to state the cap and the spill-file workflow (grep/read the saved file; do not re-fetch the URL).

## 3. Report 5MB body truncation

- [ ] 3.1 Read `maxSize+1` bytes via `io.LimitReader`; when the body reached the extra byte, trim back to `maxSize` and set a `bodyTruncated` flag.
- [ ] 3.2 When `bodyTruncated`, prepend a notice to the returned content stating the response exceeded the 5MB limit and was truncated before conversion; keep it a non-error response.

## 4. Tests

- [ ] 4.1 `webfetch_test.go`: content under the cap returned verbatim, no file written.
- [ ] 4.2 Content over the cap → header with byte total + file path, head/tail fragments with elision marker, response smaller than the original and not an error; the spill file contains the full converted content.
- [ ] 4.3 Cap applied post-conversion: an HTML fixture whose markdown exceeds the cap spills markdown, not HTML; parameterized across `text` / `markdown` / `html`.
- [ ] 4.4 `maxOutputBytes` resolution table (default / positive / negative-unlimited).
- [ ] 4.5 Oversized body (>5MB, no `Content-Length`, chunked) → truncation notice present, non-error; body within the limit → no notice.
- [ ] 4.6 `t.Cleanup(CleanupTempDir)` in every test that can spill.

## 5. Docs

- [ ] 5.1 Document `webFetch.maxOutputBytes` in `README.md` (next to the tools table / web search config) with the default, the override, and `-1` to disable.
- [ ] 5.2 Note the cap and the spill-file workflow wherever the tool list is described for agent authors.

## 6. Verification

- [ ] 6.1 `go build ./...` clean.
- [ ] 6.2 `go vet ./internal/llm/tools/ ./internal/config/` clean.
- [ ] 6.3 `go test ./internal/llm/tools/ ./internal/config/ ./cmd/schema/...` passes.
- [ ] 6.4 `make schema-check` passes (regenerated schema committed).
- [ ] 6.5 `openspec validate webfetch-output-limit --strict` passes.
- [ ] 6.6 Re-fetch `https://code.claude.com/docs/en/settings-reference` through the built binary and confirm the tool result is ~one cap in size, names a spill file, and that `grep` over that file finds the settings keys.
