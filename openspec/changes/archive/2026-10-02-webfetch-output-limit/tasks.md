## 1. Configuration

- [x] 1.1 Add `WebFetchConfig struct { MaxOutputBytes int \`json:"maxOutputBytes,omitempty"\` }` to `internal/config/config.go` and a `WebFetch *WebFetchConfig \`json:"webFetch,omitempty"\`` field on `Config`, documented (default / positive override / negative disables), placed next to `WebSearch`.
- [x] 1.2 Add the `webFetch` object to the hand-authored schema in `cmd/schema/main.go` next to the `webSearch` block, and regenerate `opencode-schema.json` (`make schema`).
- [x] 1.3 Config unit test covering `viper.Unmarshal` of a `webFetch` block (unset / positive / negative).

## 2. Apply the cap in the webfetch path

- [x] 2.1 In `internal/llm/tools/webfetch.go`, add default const `webFetchMaxOutputBytes = 50 * 1024` and `resolveWebFetchMaxOutputBytes(cfg) int` mirroring `resolveCallToolMaxOutputBytes` (positive → value; negative → `-1`; else default).
- [x] 2.2 Collapse the four `NewTextResponse` returns in the `switch format` block into one: each branch computes the formatted `out string` (keeping the existing early error returns for conversion failures), then a single tail applies `tools.PersistLargeOutput(out, "webfetch", "webfetch", cap)` and returns.
- [x] 2.3 Log each spill (`logging.Info`: url, format, totalBytes, maxOutputBytes, file).
- [x] 2.4 Update `fetchToolDescription` to state the cap and the spill-file workflow (grep/read the saved file; do not re-fetch the URL).

## 3. Report 5MB body truncation

- [x] 3.1 Read `maxSize+1` bytes via `io.LimitReader`; when the body reached the extra byte, trim back to `maxSize` and set a `bodyTruncated` flag.
- [x] 3.2 When `bodyTruncated`, prepend a notice to the returned content stating the response exceeded the 5MB limit and was truncated before conversion; keep it a non-error response.

## 4. Tests

- [x] 4.1 `webfetch_test.go`: content under the cap returned verbatim, no file written.
- [x] 4.2 Content over the cap → header with byte total + file path, head/tail fragments with elision marker, response smaller than the original and not an error; the spill file contains the full converted content.
- [x] 4.3 Cap applied post-conversion: an HTML fixture whose markdown exceeds the cap spills markdown, not HTML; parameterized across `text` / `markdown` / `html`.
- [x] 4.4 `maxOutputBytes` resolution table (default / positive / negative-unlimited).
- [x] 4.5 Oversized body (>5MB, no `Content-Length`, chunked) → truncation notice present, non-error; body within the limit → no notice.
- [x] 4.6 `t.Cleanup(CleanupTempDir)` in every test that can spill.

## 5. Recovery-guidance correction (found by the e2e)

- [x] 5.1 The e2e's first run showed the spill header's "read specific ranges with the read tool (offset/limit)" is false for the spills this change creates: `read` rejects any file over `MaxReadSize` (250KB) on size alone, before it looks at offset/limit, and a page that trips a 50KB cap is often larger than that.
- [x] 5.2 Correct `buildOutputOverflowHeader` (shared with the MCP spill path, which has the same defect for multi-MB build logs) to lead with the tools that have no size ceiling — `grep`, and `sed` in bash — and to name `MaxReadSize` as the limit above which `read` declines a file.
- [x] 5.3 Keep raising/windowing `MaxReadSize` out of scope: `read` is used by every agent and letting a windowed read escape the guard raises worst-case output from 250KB to ~4MB. Recorded as a follow-up in design.md §6.

## 6. E2E coverage

- [x] 6.1 `cmd/webfetch-e2e`: black-box driver running the real pipeline (`.opencode.json` → viper → `config.Load` → `agent.NewToolSet` → the real webfetch/grep/read tools → a local HTTP server → the process scratch dir), emitting a JSON verdict per scenario.
- [x] 6.2 `scripts/test/webfetch_output_limit.sh`: four mktemp sandboxes — `default_cap`, `configured_cap`, `unbounded`, `under_cap` — asserted with jq, plus a check that the published schema carries the field. No network: the driver serves its own fixture over 127.0.0.1.
- [x] 6.3 The load-bearing assertion: a needle buried mid-page is ABSENT from the capped reply yet recoverable through the agent's own `grep` tool at the advertised path — cross-tool recovery is the feature's whole promise, and a preview the agent cannot follow up on would be a regression, not a fix.
- [x] 6.4 `read_tool_opens_spill_file` on a spill inside `read`'s ceiling, and `oversized_spill_is_beyond_read_as_documented` on one past it — the latter fails if `MaxReadSize` behaviour ever changes, so the header guidance cannot go stale silently.

## 7. Docs

- [x] 7.1 Document `webFetch.maxOutputBytes` in `README.md` (next to the tools table / web search config) with the default, the override, and `-1` to disable.
- [x] 7.2 Note the cap and the spill-file workflow wherever the tool list is described for agent authors.

## 8. Verification

- [x] 8.1 `go build ./...` clean.
- [x] 8.2 `go vet ./internal/llm/tools/ ./internal/config/` clean.
- [x] 8.3 `go test ./internal/llm/tools/ ./internal/config/ ./cmd/schema/...` passes.
- [x] 8.4 `make schema-check` passes (regenerated schema committed).
- [x] 8.5 `openspec validate webfetch-output-limit --strict` passes.
- [x] 8.6 `bash scripts/test/webfetch_output_limit.sh` passes (15 checks). This replaces the originally planned live re-fetch of `https://code.claude.com/docs/en/settings-reference`: the `default_cap` scenario reproduces the incident's shape hermetically — a 423KB page, a 51KB reply, the needle recovered by `grep` from the spill file — with no network dependency and no page that can change under the test. The live-page sizes behind the proposal (374KB markdown / ~93k tokens) are the pre-change measurement recorded there.
