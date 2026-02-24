# Bash Tool Full Output Persistence

**Date**: 2026-02-23
**Status**: Implemented
**Author**: AI-assisted

## Overview

When a bash command produces output exceeding the 50KB / 2000-line truncation limit, the middle section is permanently discarded. This spec adds temp file persistence for the full output so the agent can paginate through it using the `view` tool.

## Motivation

### Current State

`truncateOutput` in `internal/llm/tools/bash.go` sliced the raw string at byte boundaries, which had two problems:

1. **Permanent data loss**: The middle section was gone. The agent could not recover it.
2. **Broken byte slicing**: `content[:halfBytes]` cut at a byte offset with no regard for line boundaries or UTF-8 character boundaries.

The global safety net `validateAndTruncate` in `tools.go` had the same mid-character slicing issue.

### Desired State

When output exceeds limits, the full content is written to a process-scoped temp file. The truncated response includes the file path and total line count so the agent can use `view` with `offset`/`limit` to read any section:

```
<stdout truncated: 8,432 lines total>
Full output saved to: /tmp/opencode-<pid>/bash-stdout-<timestamp>.txt
Use the view tool with offset/limit to read specific sections.

--- First 500 lines ---
<first 500 lines, line-aligned>

--- Last 500 lines ---
<last 500 lines, line-aligned>
```

Truncation boundaries are always line-aligned. The `view` tool already supports `offset`/`limit` pagination and handles files up to 250KB per read window.

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Temp file location | `/tmp/opencode-<pid>/` | Process-scoped directory enables bulk cleanup; avoids collisions across concurrent processes |
| Cleanup strategy | `CleanupTempDir()` called from `app.Shutdown()` and `app.ForceShutdown()` | Simple, reliable; OS also cleans `/tmp` on reboot as a backstop |
| Max persisted size | 100MB per file, enforced when writing | Prevents runaway commands from filling disk; agent can still paginate the first 100MB |
| Line-aligned truncation | Split on `\n`, take first/last N lines | Fixes the current mid-line/mid-UTF-8 slicing bug; lines are the natural unit for `view` pagination |
| Stderr persistence | Yes, separate from stdout | Stderr can also be large; separate files preserve the distinction. Named `bash-stderr-<ts>.txt` |
| Response format | Inline summary with file path + head/tail preview | Agent sees enough context to decide whether to paginate; file path is immediately actionable |
| `validateAndTruncate` fix | Line-aligned truncation via `truncateToMaxChars` | Uses `strings.LastIndex` to find the nearest `\n` before the cut point, preventing mid-line/mid-UTF-8 cuts. The bash tool's own truncation happens first (with temp file persistence), so the global safety net rarely triggers for bash output — but it now handles all tool responses safely |
| Temp file naming | `bash-<label>-<unixNano>.txt` within process dir | Unique per invocation; label distinguishes stdout/stderr; timestamp aids debugging |
| `BashResponseMetadata` | Added `TempFilePath` field | Low cost, high utility for TUI or other consumers |

## Architecture

```
bash.Run()
    │
    ├── sh.Exec() → stdout (string), stderr (string)
    │
    ├── persistAndTruncate(stdout, "stdout") → persistResult{content, filePath}
    │       │
    │       ├── if len <= limits: return as-is, no file written
    │       │
    │       └── else:
    │               ├── persistToTempFile() → writes to /tmp/opencode-<pid>/bash-stdout-<ts>.txt
    │               ├── buildPreview() → line-aligned head+tail preview
    │               ├── buildTruncationHeader() → header with file path
    │               └── return header + preview, filePath
    │
    ├── persistAndTruncate(stderr, "stderr") → persistResult{content, filePath}
    │
    └── build response string with preview + temp file path in metadata
```

Process temp directory lifecycle:

```
First bash command that exceeds limits
    └── ensureTempDir()
            └── os.MkdirAll(/tmp/opencode-<pid>/, 0700)
            └── path stored in package-level var (sync.Mutex protected)

App shutdown (normal or forced)
    └── CleanupTempDir() → os.RemoveAll(/tmp/opencode-<pid>/)
```

## Implementation

### Files Changed

- **`internal/llm/tools/tempdir.go`** (new): Contains `ensureTempDir`, `persistToTempFile`, `CleanupTempDir`, `buildPreview`, `buildTruncationHeader`, `truncateToMaxChars` and constants (`MaxPersistBytes`, `TruncatedHeadLines`, `TruncatedTailLines`).

- **`internal/llm/tools/bash.go`**:
  - Replaced `truncateOutput` and `countLines` with `persistAndTruncate` which returns a `persistResult{content, filePath}`.
  - Updated `Run()` to use `persistAndTruncate` for both stdout and stderr, and populate `TempFilePath` in metadata.
  - Added `TempFilePath string` to `BashResponseMetadata`.
  - Updated `bashDescriptionTemplate` to describe the new behavior.

- **`internal/llm/tools/tools.go`**: Updated `validateAndTruncate` to use `truncateToMaxChars` for line-aligned truncation instead of raw byte slicing.

- **`internal/app/app.go`**: Added `tools.CleanupTempDir()` calls in both `Shutdown()` and `ForceShutdown()`.

- **`internal/llm/tools/bash_test.go`** (new): Tests for `buildPreview`, `persistAndTruncate`, `truncateToMaxChars`, and `CleanupTempDir`.

### What Was Not Changed

- **`view` tool**: No changes needed. It already supports `offset`/`limit` pagination and handles files up to 250KB per read window.
- **Session-scoped directories**: Simplified to process-scoped (`/tmp/opencode-<pid>/`) since cleanup at shutdown is sufficient and avoids needing to wire into per-session lifecycle.

## Edge Cases

### Command generates > 100MB of output

1. `persistToTempFile` writes the first `MaxPersistBytes` (100MB) to the temp file, then stops
2. Response notes: `"Full output saved to: <path> (truncated at 100MB)"`
3. Agent can paginate the 100MB file; anything beyond is lost

### Temp directory creation fails (disk full, permissions)

1. `persistToTempFile` returns empty string
2. `buildTruncationHeader` omits the "Full output saved to" line
3. Preview is still shown — degraded gracefully

### Two concurrent bash calls

1. Both call `ensureTempDir` — idempotent via `sync.Mutex` + `os.MkdirAll`
2. Each writes to a unique `bash-<label>-<unixNano>.txt` — no collision

### Process killed (SIGKILL, crash)

1. Deferred cleanup does not run
2. `/tmp/opencode-<pid>/` remains on disk
3. OS cleans `/tmp` on next reboot (standard behavior)

### `validateAndTruncate` global safety net

1. For bash tool output, `persistAndTruncate` runs first — the preview (header + 500+500 lines) is well under `MaxToolResponseTokens`
2. For other tools that produce very large output, `validateAndTruncate` now cuts at the nearest line boundary before the character limit
3. No temp file is written by `validateAndTruncate` — it is a fallback for non-bash tools

## Success Criteria

- [x] `persistAndTruncate` never cuts mid-line or mid-UTF-8 character — verified by test with multi-byte content
- [x] `validateAndTruncate` never cuts mid-line — uses `truncateToMaxChars` with line-boundary search
- [x] Commands producing >2000 lines write a temp file; response includes the path
- [x] Agent can use `view` with `offset` on the temp file to read any section
- [x] Temp files are removed at app shutdown (verified by `TestCleanupTempDir`)
- [x] Commands producing output under limits behave identically to before (no temp file written)
- [x] `go test ./internal/llm/tools/...` passes
- [x] `make test` passes

## References

- `internal/llm/tools/bash.go` — `persistAndTruncate`, `MaxOutputBytes`, `MaxOutputLines`, `bash.Run()`
- `internal/llm/tools/tempdir.go` — `ensureTempDir`, `persistToTempFile`, `CleanupTempDir`, `buildPreview`, `truncateToMaxChars`
- `internal/llm/tools/tools.go` — `validateAndTruncate`, `MaxToolResponseTokens`
- `internal/llm/tools/view.go` — `MaxReadSize` (250KB), `DefaultReadLimit` (2000 lines), pagination via `offset`/`limit`
- `internal/app/app.go` — Shutdown hooks for temp directory cleanup
