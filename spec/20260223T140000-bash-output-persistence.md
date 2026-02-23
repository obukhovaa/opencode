# Bash Tool Full Output Persistence

**Date**: 2026-02-23
**Status**: Draft
**Author**: AI-assisted

## Overview

When a bash command produces output exceeding the 50KB / 2000-line truncation limit, the middle section is permanently discarded. This spec adds temp file persistence for the full output so the agent can paginate through it using the `view` tool.

## Motivation

### Current State

`truncateOutput` in `internal/llm/tools/bash.go` slices the raw string at byte boundaries:

```go
const (
    MaxOutputBytes = 50 * 1024  // 50KB
    MaxOutputLines = 2000
)

func truncateOutput(content string) string {
    lines := strings.Split(content, "\n")
    totalBytes := len(content)

    if totalBytes <= MaxOutputBytes && len(lines) <= MaxOutputLines {
        return content
    }

    halfBytes := MaxOutputBytes / 2
    start := content[:halfBytes]
    end := content[len(content)-halfBytes:]

    truncatedLinesCount := countLines(content[halfBytes : len(content)-halfBytes])
    return fmt.Sprintf("%s\n\n... [%d lines truncated] ...\n\n%s", start, truncatedLinesCount, end)
}
```

This has two problems:

1. **Permanent data loss**: The middle section is gone. The agent cannot recover it — it can only re-run the command.
2. **Broken byte slicing**: `content[:halfBytes]` cuts at a byte offset with no regard for line boundaries or UTF-8 character boundaries. The first and last lines of each half are likely garbled.

The global safety net in `tools.go` has the same issue:

```go
func validateAndTruncate(response toolResponse) toolResponse {
    if estimatedTokens > MaxToolResponseTokens {
        maxChars := MaxToolResponseTokens * 4
        truncated := response.Content[:maxChars]
        response.Content = truncated + "\n\n[Output truncated due to size limit...]"
    }
    return response
}
```

### Desired State

When output exceeds limits, the full content is written to a session-scoped temp file. The truncated response includes the file path and total line count so the agent can use `view` with `offset`/`limit` to read any section:

```
<stdout truncated: 8,432 lines total>
Full output saved to: /tmp/opencode-<sessionID>/bash-<timestamp>.txt
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
| Temp file location | `/tmp/opencode-<sessionID>/` | Session-scoped directory enables bulk cleanup; avoids collisions across concurrent sessions |
| Cleanup strategy | Delete directory at process exit via `os.MkdirTemp` + deferred cleanup registered at session start | Simple, reliable; OS also cleans `/tmp` on reboot as a backstop |
| Max persisted size | 100MB per file, enforced by capping shell output read | Prevents runaway commands from filling disk; agent can still paginate the first 100MB |
| Line-aligned truncation | Split on `\n`, take first/last N lines | Fixes the current mid-line/mid-UTF-8 slicing bug; lines are the natural unit for `view` pagination |
| Stderr persistence | Yes, same as stdout | Stderr can also be large (e.g., compiler errors); consistent behavior is simpler |
| Response format | Inline summary with file path + head/tail preview | Agent sees enough context to decide whether to paginate; file path is immediately actionable |
| Interaction with `validateAndTruncate` | No change needed | The truncated response (head + tail preview) is well under `MaxToolResponseTokens`; the temp file is the escape hatch for the full content |
| Temp file naming | `bash-<unixNano>.txt` within session dir | Unique per invocation; timestamp aids debugging |

## Architecture

```
bash.Run()
    │
    ├── sh.Exec() → stdout (string), stderr (string)
    │
    ├── persistOutput(sessionID, stdout) → (truncatedStdout, tempFilePath)
    │       │
    │       ├── if len <= limits: return as-is, no file written
    │       │
    │       └── else:
    │               ├── ensureSessionTempDir(sessionID) → /tmp/opencode-<sessionID>/
    │               ├── write full content to bash-<ts>.txt (capped at MaxPersistBytes)
    │               └── return line-aligned head+tail preview + file path
    │
    ├── persistOutput(sessionID, stderr) → (truncatedStderr, tempFilePath)
    │
    └── build response string with preview + "Full output saved to: <path>"
```

Session temp directory lifecycle:

```
Session created
    └── RegisterSessionTempDir(sessionID)
            └── os.MkdirAll(/tmp/opencode-<sessionID>/, 0700)
            └── runtime.SetFinalizer / process exit hook → os.RemoveAll(dir)

Session ends / process exits
    └── CleanupSessionTempDir(sessionID) → os.RemoveAll(/tmp/opencode-<sessionID>/)
```

## Implementation Plan

### Phase 1: Line-Aligned Truncation (no temp files)

Fix the existing bug independently of the persistence feature. Low risk, immediately improves output quality.

- [ ] **1.1** Replace byte-slice truncation in `truncateOutput` with line-based truncation:
  - Split on `\n`
  - Take first `MaxOutputLines/2` lines and last `MaxOutputLines/2` lines
  - Respect `MaxOutputBytes` as a secondary cap: if the line-aligned head+tail still exceeds 50KB, trim lines from the inner boundary until it fits
  - File: `internal/llm/tools/bash.go`

- [ ] **1.2** Add a test for `truncateOutput` covering: output under limits (no-op), output over line limit, output over byte limit, multi-byte UTF-8 content (verify no garbled characters in output)
  - File: `internal/llm/tools/bash_test.go` (create if absent)

### Phase 2: Session Temp Directory Management

- [ ] **2.1** Add `ensureSessionTempDir(sessionID string) (string, error)` in a new file `internal/llm/tools/tempdir.go`:
  - Creates `/tmp/opencode-<sessionID>/` with `os.MkdirAll(..., 0700)`
  - Registers cleanup via `sync.Once` + channel-based exit hook (or `os/signal` handler)
  - Returns the directory path

- [ ] **2.2** Add `CleanupSessionTempDir(sessionID string)` that calls `os.RemoveAll` on the session directory. Wire this into session teardown in `internal/app/app.go` or wherever sessions are closed.

- [ ] **2.3** Add constants:
  ```go
  const (
      MaxPersistBytes = 100 * 1024 * 1024  // 100MB
      TruncatedHeadLines = 500
      TruncatedTailLines = 500
  )
  ```

### Phase 3: Output Persistence

- [ ] **3.1** Add `persistOutput(sessionID, content string) (preview string, filePath string, err error)`:
  - If `len(content) <= MaxOutputBytes && lineCount <= MaxOutputLines`: return content unchanged, empty filePath
  - Otherwise: write full content (capped at `MaxPersistBytes`) to `<sessionTempDir>/bash-<time.Now().UnixNano()>.txt`
  - Return line-aligned head+tail preview and the file path

- [ ] **3.2** Update `bash.Run()` to call `persistOutput` instead of `truncateOutput` for both stdout and stderr. Build the response string to include the temp file path when set:
  ```
  <stdout truncated: N lines total>
  Full output saved to: /tmp/opencode-<sessionID>/bash-<ts>.txt
  Use the view tool with offset/limit to read specific sections.

  --- First 500 lines ---
  ...

  --- Last 500 lines ---
  ...
  ```

- [ ] **3.3** Update `bashDescriptionTemplate` to reflect the new behavior: replace the current truncation warning with a note that full output is saved to a temp file and can be paginated with `view`.

- [ ] **3.4** Add integration test: run a command that generates >2000 lines, verify the response contains a valid file path, verify the file exists and contains the full output, verify `view` can read it with offset.

## Edge Cases

### Command generates > 100MB of output

1. Shell writes unbounded output to stdout buffer
2. `persistOutput` writes the first `MaxPersistBytes` (100MB) to the temp file, then stops
3. Response notes: `"Full output saved to: <path> (truncated at 100MB)"`
4. Agent can paginate the 100MB file; anything beyond is lost

### Session temp directory creation fails (disk full, permissions)

1. `ensureSessionTempDir` returns an error
2. `persistOutput` falls back to the line-aligned head+tail preview without a file path
3. Response omits the "Full output saved to" line; truncation message is still present
4. No error is surfaced to the agent — degraded gracefully

### Two concurrent bash calls in the same session

1. Both call `ensureSessionTempDir` — idempotent via `os.MkdirAll`
2. Each writes to a unique `bash-<unixNano>.txt` — no collision
3. Both files are cleaned up when the session ends

### Process killed (SIGKILL, crash)

1. Deferred cleanup does not run
2. `/tmp/opencode-<sessionID>/` remains on disk
3. OS cleans `/tmp` on next reboot (standard behavior)
4. No correctness issue — stale files are inert

### `view` tool's 250KB per-read limit vs large temp files

1. Agent calls `view` on a 10MB temp file
2. `view` returns the first 2000 lines with a continuation hint
3. Agent uses `offset` to paginate — this is the intended workflow
4. No changes needed to `view`

## Open Questions

1. **Where to wire `CleanupSessionTempDir`?**
   - The session lifecycle is managed in `internal/app/app.go` and `internal/session/session.go`. The cleanest hook is wherever a session's context is cancelled.
   - **Recommendation**: Register cleanup in `app.go` when the session goroutine exits. If no clean hook exists, use a `sync.Map` of session dirs and clean up on process exit via `os/signal`.

2. **Should the temp file path appear in `BashResponseMetadata`?**
   - Currently `BashResponseMetadata` carries `StartTime`, `EndTime`, `Description`, `ExitCode`. Adding `TempFilePath` would let the TUI or other consumers surface it without parsing the text response.
   - **Recommendation**: Yes, add `TempFilePath string \`json:"temp_file_path,omitempty"\`` to `BashResponseMetadata`. Low cost, high utility.

3. **Should stderr get its own temp file or be merged with stdout?**
   - Separate files preserve the distinction between stdout and stderr. Merged is simpler for the agent to paginate.
   - **Recommendation**: Separate files. The current code already handles them independently; keep that separation. Name them `bash-<ts>-stdout.txt` and `bash-<ts>-stderr.txt`.

4. **Head/tail line counts (500 + 500 = 1000 lines in response)**
   - 1000 lines at ~80 chars/line ≈ 80KB, well under `MaxToolResponseTokens`. But it's more than the current 50KB limit.
   - **Recommendation**: Start with 500+500. If token usage becomes a concern, make `TruncatedHeadLines`/`TruncatedTailLines` configurable constants.

## Success Criteria

- [ ] `truncateOutput` never cuts mid-line or mid-UTF-8 character — verified by test with multi-byte content
- [ ] Commands producing >2000 lines write a temp file; response includes the path
- [ ] Agent can use `view` with `offset` on the temp file to read any section
- [ ] Temp files are removed when the session ends (verified by test or manual inspection)
- [ ] Commands producing output under limits behave identically to today (no temp file written)
- [ ] `go test ./internal/llm/tools/...` passes
- [ ] `make test` passes

## References

- `internal/llm/tools/bash.go` — `truncateOutput`, `MaxOutputBytes`, `MaxOutputLines`, `bash.Run()`
- `internal/llm/tools/tools.go` — `validateAndTruncate`, `MaxToolResponseTokens`, `BashResponseMetadata`
- `internal/llm/tools/view.go` — `MaxReadSize` (250KB), `DefaultReadLimit` (2000 lines), pagination via `offset`/`limit`
- `internal/llm/tools/file.go` — Pattern for package-level shared state (`fileRecords`)
- `internal/app/app.go` — Session lifecycle, candidate location for cleanup hook
- `spec/20260223T133437-tools-imrovements.md` — Parent spec; this feature is item 3.1
