# Verification

- `GOTOOLCHAIN=go1.25.8 make test` passed in an isolated home, including vet and repository-wide race tests.
- `scripts/test/inference_payloads.sh` passed. It exercises local HTTP providers, an actual SQLite close/reopen cycle, subsequent tool-result requests and real Bash execution without a live model service. It is executable and therefore participates in the release-tag E2E gate.
- Compatible tests cover streaming/nonstreaming `reasoning_content` and `reasoning`, two tool calls/results, endpoint/provider/model changes, malformed fields and legacy messages. Native Claude requests retain signed/redacted thinking and cache markers byte-for-byte after persistence. First-party GPT requests remain unchanged.
- Interrupted/canceled stream tests require previews to remain non-authoritative and prevent retrying after emitted output. A cancellation test exposed an SDK error-field race; the adapter now reads that field only after the reader's completion channel establishes synchronization. Provider/message race tests pass.
- Bash tests cover exact byte/line limits, limit plus one, minified JSON, long first/last lines, many lines, Unicode, blank/trailing lines, failed/capped persistence and both streams with nonzero exit metadata. Original spill bytes are checked.
- The diagnostic 1 MiB single-line case previously returned approximately the whole input despite a 50 KiB limit. The regression now checks a 50 KiB excerpt plus bounded notice/path overhead. This demonstrates bounded payload size, not a measured production dollar reduction.
- Independent plan and final implementation reviews approved the change after fixing a TLS fixture hostname and reviewing the demonstrated cancellation race. No public HTTP/SSE/ACP or configuration schema change is needed: scalar provenance remains internal persisted metadata.
- The repository-wide hidden-character scan reports existing zero-width spaces in generated `internal/lsp/protocol/tsprotocol.go`. That file matches `origin/main` exactly; every changed Go file passes a separate format/control-character scan.

This completes the correctness wave only. GENAI-671 also tracks a separately designed, default-off MCP composition pilot and its cost/quality evaluation. Background compaction and cache warming are excluded.
