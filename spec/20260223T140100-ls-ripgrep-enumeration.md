# LS Tool — Use Ripgrep for File Enumeration

**Date**: 2026-02-23
**Status**: Implemented
**Author**: AI-assisted

## Overview

Replace `filepath.Walk` with ripgrep-based file enumeration (`rg --files`) in the `ls` tool so that `.gitignore` rules are automatically respected, reducing noise from build artifacts and generated files that are irrelevant to the project.

## Motivation

### Current State

`listDirectory` in `internal/llm/tools/ls.go` uses `filepath.Walk` exclusively:

```go
err := filepath.Walk(initialPath, func(path string, info os.FileInfo, err error) error {
    if err != nil {
        return nil // silently swallowed
    }
    if shouldSkip(path, ignorePatterns) {
        if info.IsDir() {
            return filepath.SkipDir
        }
        return nil
    }
    ...
})
```

`shouldSkip` compensates with a hardcoded `commonIgnored` list of 28 entries (`node_modules`, `dist`, `build`, `target`, `vendor`, `bin`, `__pycache__`, etc.) and skips any path whose base name starts with `.`. User-supplied `ignore` patterns are matched via `filepath.Match` against the base name only — not the full path.

Problems:
- No `.gitignore` awareness. Files explicitly ignored by the project (e.g., `*.pb.go`, `*.gen.go`, lock files, compiled assets) are listed anyway.
- The `commonIgnored` list is a brittle approximation that misses project-specific patterns and requires ongoing maintenance.
- `filepath.Match` on base name only means patterns like `src/generated/**` never match.
- Walk errors are silently swallowed, making permission issues invisible.

### Desired State

When `rg` is available, `listDirectory` delegates to `rg --files` which natively respects `.gitignore`, `.ignore`, and `.rgignore` files. The flat file list from rg is fed into the existing `createFileTree` function unchanged. Directories are inferred from file paths. When `rg` is not available, the tool falls back to the current `filepath.Walk` implementation. The `commonIgnored` list is retained in the fallback path only.

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Fallback strategy | `filepath.Walk` when `exec.LookPath("rg")` fails | Matches the pattern established in `skill.go`; keeps the tool functional in minimal environments |
| `commonIgnored` list | Retain in fallback path only; drop from rg path | rg + `.gitignore` makes the list redundant for the primary path; removing it from the rg path avoids double-filtering |
| Directory inference | Extract unique parent dirs from rg file paths | `rg --files` outputs files only; directories must be synthesized. `createFileTree` already handles this via path splitting — no change needed |
| Hidden files | Do not pass `--hidden` to rg | Current behavior skips dotfiles; rg also skips hidden files by default. Consistent. If a project needs hidden files listed, they can add them to `.ignore` negation rules |
| User-supplied ignore patterns | Translate to `--glob='!pattern'` flags | Allows rg to apply them during traversal rather than post-filtering, which is more efficient and supports path-relative patterns |
| `commonIgnored` glob patterns (`*.pyc`, `*.so`, etc.) | Drop from rg path | These are typically covered by `.gitignore` in real projects; keeping them would require translating 28 entries to `--glob` flags on every call |
| Limit enforcement | Post-filter: truncate rg output at `MaxLSFiles` | rg has no built-in line limit; truncation logic stays in Go |
| Context propagation | Pass `ctx` to `exec.CommandContext` | Consistent with `grep.go`; cancels the rg process if the tool call is cancelled |

## Implementation Plan

### Phase 1: Core rg Integration

- [x] **1.1 Add `listDirectoryWithRipgrep`** — New function in `internal/llm/tools/ls.go` that:
  1. Calls `exec.LookPath("rg")` and returns `("", false, ErrRipgrepNotFound)` if missing.
  2. Builds args: `rg --files [--glob='!pattern' ...] <path>` where `--glob` flags come from `ignorePatterns`.
  3. Runs via `exec.CommandContext(ctx, rgPath, args...)`.
  4. Splits stdout on newlines, skips empty lines.
  5. Appends each file path to results; stops at `limit` and sets `truncated = true`.
  6. Returns `([]string, bool, error)` — same signature as `listDirectory`.

- [x] **1.2 Update `listDirectory` to try rg first** — Wrap the existing `filepath.Walk` implementation. Attempt `listDirectoryWithRipgrep`; on success return its results. On `ErrRipgrepNotFound` or any exec error, fall through to the existing Walk path. Log the fallback at debug level.

- [x] **1.3 Infer directories from rg output** — `rg --files` emits only file paths. `createFileTree` infers directory nodes from intermediate path segments, so no change is needed there. However, the current Walk path appends a trailing separator to directory paths (`path + string(filepath.Separator)`) to signal directory type. The rg path must not do this — `createFileTree` uses the trailing separator as the directory signal, so file paths from rg must be passed as-is (no trailing separator).

- [x] **1.4 Update `lsDescription`** — Add `.gitignore` awareness to the FEATURES section: `"- Automatically respects .gitignore rules when ripgrep is available"`. Update LIMITATIONS to note: `"- Falls back to built-in walker if ripgrep is not installed (no .gitignore support in fallback mode)"`.

### Phase 2: Cleanup and Tests

- [x] **2.1 Move `commonIgnored` into fallback only** — Guard the `commonIgnored` slice inside the `filepath.Walk` callback so it is not evaluated when rg is used. No behavioral change for the fallback path.

- [x] **2.2 Add tests for `listDirectoryWithRipgrep`** — In `internal/llm/tools/ls_test.go`:
  - Test that files listed in `.gitignore` are excluded from rg results.
  - Test that user-supplied `ignore` patterns are passed as `--glob='!pattern'` and take effect.
  - Test truncation at `MaxLSFiles`.
  - Test fallback: mock `exec.LookPath` failure and verify Walk is used instead.

- [x] **2.3 Verify `createFileTree` compatibility** — Confirm that a flat list of absolute file paths (no trailing separators) from rg produces the same tree structure as the Walk-based path. Add a table-driven test comparing both outputs on a fixture directory.

## Edge Cases

| Case | Handling |
|---|---|
| `rg` not installed | Fall back to `filepath.Walk` silently; no error surfaced to the agent |
| Empty directory (no files) | rg exits 1 with no output; treat as empty result, not an error |
| Directory with only ignored files | Same as empty directory |
| `.gitignore` negation patterns (`!important.log`) | rg respects these natively; no special handling needed |
| Symlinks | rg does not follow symlinks by default; consistent with current Walk behavior (Walk follows symlinks only if `os.Lstat` is used, which it isn't here) |
| Non-git directory (no `.gitignore`) | rg still works; just no ignore rules applied beyond hidden files |
| User pattern with path separator (`src/*.go`) | Passed as `--glob='!src/*.go'`; rg interprets globs relative to the search root |
| `ctx` cancellation mid-enumeration | `exec.CommandContext` kills the rg process; `listDirectoryWithRipgrep` returns the cancellation error, triggering fallback |
| rg exits with code 2 (partial error) | Treat as success if stdout has content; log stderr at debug level |

## Open Questions

1. **Should the fallback be silent or surfaced?**
   - Silently falling back means agents never know they are getting non-gitignore-aware results.
   - **Recommendation**: Silent fallback. Surfacing it adds noise to every response in environments without rg. A debug log is sufficient.

2. **Should `--no-ignore` be offered as an opt-in parameter?**
   - Some users may want to list all files regardless of `.gitignore` (e.g., to audit what is ignored).
   - **Recommendation**: Defer. The `ignore` parameter already lets callers override specific patterns. A full `--no-ignore` mode can be added later if there is demand.

3. **Should the `commonIgnored` list be dropped entirely (including from fallback)?**
   - The list was added precisely because Walk has no `.gitignore` support. With rg as the primary path, the list only matters for the fallback.
   - **Recommendation**: Keep in fallback. Removing it from the fallback would degrade the experience for users without rg.

4. **Should rg output be sorted before feeding into `createFileTree`?**
   - `filepath.Walk` returns paths in lexicographic order. `rg --files` output order is non-deterministic (depends on filesystem and thread scheduling).
   - **Recommendation**: Yes, sort the rg output slice before passing to `createFileTree`. This ensures stable, deterministic tree rendering. Cost is negligible for ≤1000 paths.

## Success Criteria

- [x] Files listed in `.gitignore` do not appear in `ls` output when rg is available.
- [x] User-supplied `ignore` patterns are applied correctly via `--glob` flags.
- [x] The tool falls back to `filepath.Walk` without error when rg is not installed.
- [x] Tree structure output is identical between rg and Walk paths for the same directory (verified by test).
- [x] All existing `ls` tests pass: `go test ./internal/llm/tools/...`
- [x] `make test` passes.

## References

- `internal/llm/tools/ls.go` — Current Walk implementation, `shouldSkip`, `createFileTree`, `printTree`
- `internal/llm/tools/ls_test.go` — Existing tests
- `internal/llm/tools/skill.go` — `sampleSkillFilesWithRipgrep` / `sampleSkillFiles` fallback pattern to follow
- `internal/llm/tools/grep.go` — `exec.CommandContext(ctx, "rg", ...)` usage pattern
- `spec/20260223T133437-tools-imrovements.md` — Parent spec; item 3.2 that this spec expands
