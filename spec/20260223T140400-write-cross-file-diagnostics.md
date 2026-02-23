# Write Tool — Cross-File LSP Diagnostics

**Date**: 2026-02-23
**Status**: Draft
**Author**: AI-assisted

## Overview

After writing a file, the LSP server asynchronously rechecks dependent files, but the tool only waits for the written file's diagnostics. This spec adds a short post-write wait that collects freshly-published cross-file diagnostics from the LSP cache, surfacing import errors and type mismatches in dependent files before the agent's next action.

## Motivation

### Current State

`write.go` (and identically `edit.go`, `multiedit.go`, `patch.go`) follows this sequence after writing:

```go
// internal/llm/tools/write.go
recordFileWrite(filePath)
recordFileRead(filePath)
w.lsp.WaitForDiagnostics(ctx, filePath)   // blocks until LSP publishes for THIS file

result := fmt.Sprintf("<result>\n%s\n</result>", result)
result += w.lsp.FormatDiagnostics(filePath)
```

`WaitForDiagnostics` in `internal/app/lsp.go` registers a one-shot notification handler and blocks until the LSP server publishes a `textDocument/publishDiagnostics` notification for `filePath` (or any diagnostic change), with a 5-second timeout:

```go
func (s *lspService) WaitForDiagnostics(ctx context.Context, filePath string) {
    // ...
    handler := func(params json.RawMessage) {
        lsp.HandleDiagnostics(client, params)
        if diagParams.URI.Path() == filePath || lsp.HasDiagnosticsChanged(...) {
            select { case diagChan <- struct{}{}: default: }
        }
    }
    client.RegisterNotificationHandler("textDocument/publishDiagnostics", handler)
    // open/notify the file, then:
    select {
    case <-diagChan:
    case <-time.After(5 * time.Second):
    case <-ctx.Done():
    }
}
```

`FormatDiagnostics` already reads the full LSP diagnostic cache across all clients and splits results into `<file_diagnostics>` (the written file) and `<project_diagnostics>` (all other files):

```go
// internal/lsp/diagnostics.go
func FormatDiagnostics(filePath string, clients map[string]*Client) string {
    for lspName, client := range clients {
        for location, diags := range client.GetDiagnostics() {
            isCurrentFile := location.Path() == filePath
            // ... appends to fileDiagnostics or projectDiagnostics
        }
    }
    // outputs <file_diagnostics>, <project_diagnostics>, <diagnostic_summary>
}
```

This creates two problems:

1. **Stale cross-file diagnostics**: `WaitForDiagnostics` unblocks as soon as the written file's diagnostics arrive. The LSP server (e.g., gopls) then continues rechecking dependent files asynchronously. By the time `FormatDiagnostics` runs, the `<project_diagnostics>` section reflects the pre-write state of other files.
2. **Silent import breakage**: Writing a new file that introduces a package-level symbol conflict, or modifying an exported type, can break callers in other files. The agent sees no indication of this and proceeds, compounding errors across subsequent tool calls.

### Desired State

After the primary file's diagnostics arrive, the tool waits an additional short window (2 seconds) for the LSP server to publish diagnostics for other files. Any new cross-file diagnostics that arrive during this window are included in `<project_diagnostics>`. The wait is bounded and non-blocking — if no cross-file updates arrive, the tool returns immediately after the window expires.

The output format is unchanged. The improvement is freshness, not structure.

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Cross-file wait duration | 2 seconds after primary file diagnostics | gopls typically propagates cross-file diagnostics within 1s; 2s gives headroom without blocking the edit cycle excessively |
| Trigger condition | Any `publishDiagnostics` notification for a file other than the written file | Simplest signal; the LSP cache is already updated by `HandleDiagnostics` before the notification fires |
| Max cross-file files to collect | No new limit — reuse existing 20-entry cap in `FormatDiagnostics` | The cap already exists; adding a separate file count limit adds complexity without clear benefit |
| Where to implement | New `WaitForCrossFileDiagnostics(ctx, filePath, timeout)` method on `LspService` | Keeps `WaitForDiagnostics` unchanged (other callers unaffected); new method is opt-in |
| Which tools get the improvement | `write`, `edit`, `multiedit`, `patch` — all write-like tools | All four follow the same `WaitForDiagnostics` + `FormatDiagnostics` pattern; consistency avoids surprising gaps |
| Context cancellation | Respect `ctx.Done()` in the cross-file wait | Consistent with `WaitForDiagnostics`; prevents hangs if the agent's context is cancelled |
| LSP server differences | No server-specific logic | The notification-based approach works for any LSP server; gopls will be faster, tsserver slower, but the timeout handles both |

## Architecture

```
write.Run() / edit.Run() / multiedit.Run() / patch.Run()
    │
    ├── [existing] WaitForDiagnostics(ctx, filePath)
    │       └── blocks until LSP publishes for filePath (5s timeout)
    │
    ├── [new] WaitForCrossFileDiagnostics(ctx, filePath, 2*time.Second)
    │       │
    │       ├── snapshot current diagnostic cache for all files != filePath
    │       ├── register notification handler watching for publishDiagnostics
    │       │   where URI.Path() != filePath
    │       └── select:
    │               case notification received → return (cache is updated)
    │               case timeout (2s) → return
    │               case ctx.Done() → return
    │
    └── FormatDiagnostics(filePath)
            └── reads updated cache → fresh <project_diagnostics>
```

### `LspService` interface addition

```go
// internal/lsp/service.go
type LspService interface {
    // ... existing methods ...
    WaitForCrossFileDiagnostics(ctx context.Context, filePath string, timeout time.Duration)
}
```

### Implementation in `internal/app/lsp.go`

```go
func (s *lspService) WaitForCrossFileDiagnostics(ctx context.Context, filePath string, timeout time.Duration) {
    clients := s.Clients()
    if len(clients) == 0 {
        return
    }

    diagChan := make(chan struct{}, 1)

    for _, client := range clients {
        c := client
        handler := func(params json.RawMessage) {
            lsp.HandleDiagnostics(c, params)
            var diagParams protocol.PublishDiagnosticsParams
            if err := json.Unmarshal(params, &diagParams); err != nil {
                return
            }
            if diagParams.URI.Path() != filePath {
                select {
                case diagChan <- struct{}{}:
                default:
                }
            }
        }
        c.RegisterNotificationHandler("textDocument/publishDiagnostics", handler)
    }

    select {
    case <-diagChan:
    case <-time.After(timeout):
    case <-ctx.Done():
    }
}
```

### Call site change (same pattern for all four tools)

```go
// Before:
w.lsp.WaitForDiagnostics(ctx, filePath)
result += w.lsp.FormatDiagnostics(filePath)

// After:
w.lsp.WaitForDiagnostics(ctx, filePath)
w.lsp.WaitForCrossFileDiagnostics(ctx, filePath, 2*time.Second)
result += w.lsp.FormatDiagnostics(filePath)
```

## Implementation Plan

### Phase 1: Interface and implementation

- [ ] **1.1** Add `WaitForCrossFileDiagnostics(ctx context.Context, filePath string, timeout time.Duration)` to the `LspService` interface in `internal/lsp/service.go`.
- [ ] **1.2** Implement `WaitForCrossFileDiagnostics` in `internal/app/lsp.go` following the pattern above.
- [ ] **1.3** Add a no-op stub to the mock in `internal/lsp/mocks/lsp_mock.go` (run `go generate ./...` or add manually to match the mock pattern).
- [ ] **1.4** Add a no-op stub to the test helper `noopLspService` in `internal/llm/tools/lsp_test.go`.

### Phase 2: Wire into write-like tools

- [ ] **2.1** In `write.go`: add `w.lsp.WaitForCrossFileDiagnostics(ctx, filePath, 2*time.Second)` between `WaitForDiagnostics` and `FormatDiagnostics`.
- [ ] **2.2** In `edit.go` (`replaceContent` path): same insertion after `WaitForDiagnostics`.
- [ ] **2.3** In `multiedit.go`: same insertion after `WaitForDiagnostics`.
- [ ] **2.4** In `patch.go`: same insertion after `WaitForDiagnostics` (note: `patch.go` calls it per-file in a loop — insert after the per-file wait, before `FormatDiagnostics` which is called once at the end).
- [ ] **2.5** Run `go test ./...` and fix any compilation errors from the interface change.

### Phase 3: Observability (deferred)

- [ ] **3.1** Log at `DEBUG` level when cross-file diagnostics arrive vs when the timeout fires, to measure real-world LSP propagation latency.
- [ ] **3.2** After data collection, tune the 2-second timeout or make it configurable per LSP server type.

## Edge Cases

### No LSP clients active

1. `WaitForCrossFileDiagnostics` is called with no clients.
2. Early return — no wait, no panic.
3. `FormatDiagnostics` returns empty output as today.

### LSP server never publishes cross-file diagnostics (e.g., no dependents)

1. No `publishDiagnostics` notification arrives for other files.
2. The 2-second timeout fires.
3. `FormatDiagnostics` reads the cache — `<project_diagnostics>` may be empty or stale.
4. No regression vs current behavior.

### Cross-file notification arrives before `WaitForCrossFileDiagnostics` registers the handler

1. The notification was already processed by the existing `HandleDiagnostics` handler registered at client init.
2. The diagnostic is already in the cache.
3. `WaitForCrossFileDiagnostics` times out after 2 seconds, but `FormatDiagnostics` reads the already-updated cache.
4. Result is correct — the wait was unnecessary but harmless.

### `patch.go` multi-file loop

1. `patch.go` writes multiple files in a loop, calling `WaitForDiagnostics` per file.
2. `WaitForCrossFileDiagnostics` is called once after the loop, before the single `FormatDiagnostics` call.
3. This is the correct placement — cross-file effects accumulate across all written files; one wait at the end is sufficient.

### Context cancelled during cross-file wait

1. Agent's context is cancelled (e.g., user interrupts).
2. `WaitForCrossFileDiagnostics` returns immediately via `ctx.Done()`.
3. `FormatDiagnostics` still runs on whatever is in the cache.
4. No hang, no panic.

### Notification handler registration race

1. `WaitForCrossFileDiagnostics` registers a handler via `RegisterNotificationHandler`.
2. `WaitForDiagnostics` also registers a handler for the same method.
3. `RegisterNotificationHandler` replaces the previous handler (map assignment in `client.go`).
4. The cross-file handler must be registered **after** `WaitForDiagnostics` returns — which it is, since the calls are sequential.

## Open Questions

1. **Should the 2-second timeout be configurable?**
   - Options: (a) hardcoded constant, (b) per-server-type default in `internal/app/lsp.go`, (c) user-configurable in `.opencode.json`.
   - **Recommendation**: Start with a hardcoded constant `CrossFileDiagnosticsTimeout = 2 * time.Second` in `internal/app/lsp.go`. Promote to config only if real-world data shows the timeout needs tuning per environment.

2. **Should `WaitForCrossFileDiagnostics` be skipped when no LSP clients handle the written file's extension?**
   - If the written file is `.md` and no LSP handles it, cross-file diagnostics are irrelevant.
   - Options: (a) always call it (harmless, just times out), (b) check `ClientsForFile` first and skip if empty.
   - **Recommendation**: Always call it. The early-return for empty clients already handles the no-LSP case. Checking extensions adds complexity for negligible gain.

3. **Should `edit.go`'s `createNewFile` path also get the cross-file wait?**
   - Currently `createNewFile` in `edit.go` does not call `WaitForDiagnostics` at all — it returns before the LSP wait. New files are the most likely to introduce import conflicts.
   - **Recommendation**: Yes, add both `WaitForDiagnostics` and `WaitForCrossFileDiagnostics` to `createNewFile` in a follow-up. It's out of scope here to avoid scope creep, but it's the highest-value gap.

4. **Does registering a new notification handler in `WaitForCrossFileDiagnostics` clobber the permanent `HandleDiagnostics` handler?**
   - `RegisterNotificationHandler` is a map assignment — it replaces whatever was there. The permanent handler registered at client init (`HandleDiagnostics`) would be lost.
   - **Recommendation**: The implementation must call `lsp.HandleDiagnostics(c, params)` inside the temporary handler (as `WaitForDiagnostics` already does) to preserve cache updates. After the wait completes, re-register the original permanent handler. Alternatively, refactor `RegisterNotificationHandler` to support multiple handlers (chaining) — but that's a larger change. For now, mirror the pattern from `WaitForDiagnostics` exactly.

## Success Criteria

- [ ] After writing a file that breaks an import in another file, `<project_diagnostics>` includes the error in the same tool response.
- [ ] The cross-file wait does not exceed 2 seconds when no cross-file diagnostics arrive.
- [ ] Context cancellation during the cross-file wait does not hang the tool.
- [ ] All four write-like tools (`write`, `edit`, `multiedit`, `patch`) include the cross-file wait.
- [ ] The `LspService` mock and `noopLspService` test stub compile without errors.
- [ ] `go test ./...` passes.
- [ ] `make test` passes.

## References

- `internal/llm/tools/write.go` — primary integration point, lines 229–233
- `internal/llm/tools/edit.go` — `replaceContent`, line 164; `createNewFile` (no LSP wait — see Open Question 3)
- `internal/llm/tools/multiedit.go` — line 310
- `internal/llm/tools/patch.go` — line 364
- `internal/app/lsp.go` — `WaitForDiagnostics` implementation (lines 141–182), `FormatDiagnostics` (line 184)
- `internal/lsp/service.go` — `LspService` interface
- `internal/lsp/diagnostics.go` — `FormatDiagnostics`, `HandleDiagnostics`
- `internal/lsp/client.go` — `RegisterNotificationHandler`, `GetDiagnostics`, `HandleDiagnostics`
- `internal/lsp/mocks/lsp_mock.go` — mock to update
- `internal/llm/tools/lsp_test.go` — `noopLspService` stub to update
- `spec/20260223T133437-tools-imrovements.md` — parent spec
