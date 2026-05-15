# Grep Tool: Feature Parity and Prompt Improvements

**Date**: 2026-05-14
**Status**: Implemented
**Author**: AI-assisted

## Problem Statement

Agents frequently bypass our built-in grep tool in favor of `bash` + `grep`/`rg`. A Langfuse trace shows the model choosing:

```bash
grep -r "commons-lang3\|commons.lang3\|apache.commons.lang3" licensing/infrastructure/build.gradle* licensing/build.gradle* build.gradle* 2>/dev/null | head -10
```

This happens for two reasons:

1. **The description actively tells the model to use bash.** Line 82 of `grep.go` says: *"If you need to identify or count the number of matches within files, use the Bash tool with `rg` directly."*
2. **Real functional gaps** make the built-in tool insufficient for common search patterns — no output modes, no context lines, no case-insensitive flag, no file type filter, no pagination.

Reference implementation: Claude Code's `GrepTool` (TypeScript, wrapping ripgrep) with rich parameter surface and explicit "NEVER use bash for grep" prompt.

## Proposed Changes

### 1. New Parameters

Add these parameters to `GrepParams` and the tool schema, mirroring the reference:

| Parameter | Type | Default | Maps to | Description |
|---|---|---|---|---|
| `output_mode` | `enum("content","files_with_matches","count")` | `"files_with_matches"` | `-l` / `-c` / (none) | Controls output format |
| `context` | `int` | — | `-C N` | Lines of context around each match (content mode only) |
| `before_context` | `int` | — | `-B N` | Lines before each match (content mode only) |
| `after_context` | `int` | — | `-A N` | Lines after each match (content mode only) |
| `case_insensitive` | `bool` | `false` | `-i` | Case-insensitive search |
| `file_type` | `string` | — | `--type X` | Ripgrep file type filter (e.g. `go`, `js`, `py`) |
| `head_limit` | `int` | `250` | — | Max entries returned. `0` = unlimited |
| `offset` | `int` | `0` | — | Skip first N entries before applying head_limit |
| `multiline` | `bool` | `false` | `-U --multiline-dotall` | Match patterns across line boundaries |

**Keep existing parameters:**
- `pattern` (required) — unchanged
- `path` (optional) — unchanged
- `include` (optional) — rename to `glob` for clarity and consistency with reference (`--glob`)
- `literal_text` (optional) — unchanged

### 2. Ripgrep Invocation Changes

Update `searchWithRipgrep()` to:

```
rg --hidden                          # search hidden files (new)
    --glob '!.git'                   # exclude VCS dirs (new)
    --glob '!.svn'
    --glob '!.hg'
    --max-columns 500                # prevent minified/base64 bloat (new)
    --field-match-separator=\x00     # already used
    -H -n --no-messages              # already used
    [-i]                             # if case_insensitive
    [-l]                             # if output_mode=files_with_matches
    [-c]                             # if output_mode=count
    [-C N]                           # if context
    [-B N]                           # if before_context
    [-A N]                           # if after_context
    [-U --multiline-dotall]          # if multiline
    [--type X]                       # if file_type
    [--glob PATTERN]                 # if glob
    [-e] PATTERN                     # -e when pattern starts with dash
    PATH
```

### 3. Result Processing Changes

#### files_with_matches mode (default)
- `rg -l` returns one file path per line
- Sort by mtime (newest first) — existing behavior
- Apply head_limit/offset pagination
- Return: file paths, count, pagination metadata

#### content mode
- `rg -H -n` returns `file:line:content` lines (or with context separators `--`)
- Apply head_limit/offset to output lines
- Convert absolute paths to relative paths (saves tokens)
- Return: raw content lines, pagination metadata

#### count mode
- `rg -c` returns `file:count` lines
- Sum total matches across files
- Apply head_limit/offset to file entries
- Return: per-file counts, total, pagination metadata

#### Response format change
Current format mixes file paths and line content in a single string. Change to structured output per mode:

```
# files_with_matches
Found 12 files (limit: 250)
path/to/file1.go
path/to/file2.go
...

# content
path/to/file1.go:42:  func SearchFiles(pattern string) {
path/to/file1.go:43:    matches := grep(pattern)
--
path/to/file2.go:10:  grep.Run(ctx, call)
...
[Showing results with pagination = limit: 250]

# count
path/to/file1.go:5
path/to/file2.go:2
Found 7 total occurrences across 2 files.
```

### 4. Description Rewrite

Replace the entire `grepDescription` constant:

```go
const grepDescription = `A powerful search tool built on ripgrep

Usage:
- ALWAYS use grep for search tasks. NEVER invoke ` + "`grep`" + ` or ` + "`rg`" + ` as a bash command. The grep tool has been optimized for correct permissions and access.
- Supports full regex syntax (e.g., "log.*Error", "function\\s+\\w+")
- Filter files with glob parameter (e.g., "*.js", "**/*.tsx") or file_type parameter (e.g., "js", "py", "go")
- Output modes: "content" shows matching lines, "files_with_matches" shows only file paths (default), "count" shows match counts
- Use Task tool for open-ended searches requiring multiple rounds
- Pattern syntax: Uses ripgrep (not grep) — literal braces need escaping (use ` + "`interface\\{\\}`" + ` to find ` + "`interface{}`" + ` in Go code)
- Multiline matching: By default patterns match within single lines only. For cross-line patterns like ` + "`struct \\{[\\s\\S]*?field`" + `, use multiline=true
`
```

Key changes:
- **Removed** the line telling the model to use bash
- **Added** explicit "NEVER use bash for grep" instruction
- **Added** output mode documentation
- **Added** file_type mention
- **Added** multiline hint
- **Removed** verbose "WHEN TO USE / HOW TO USE / TIPS" structure — shorter is better for token budget

### 5. Agent Prompt Updates

In `internal/llm/prompt/explorer.go` and `internal/llm/prompt/workhorse.go`, strengthen the grep guidance:

```
- ALWAYS use grep for searching file contents. NEVER invoke `grep` or `rg` via bash.
```

(workhorse already says "do not use grep in bash" — keep that, just make explorer match)

### 6. Raise Default Limit

Change the hard cap from 100 to 250 (matching reference). The `head_limit` parameter lets models request fewer when they want.

### 7. Fallback Search

The Go regex fallback (`searchFilesWithRegex`) should remain as-is for environments without ripgrep. New parameters (output_mode, context, etc.) will only be available when ripgrep is present. If ripgrep is missing and the model requests these features, return results in the basic format with a note: "(Advanced search features require ripgrep. Install it for full functionality.)"

## What NOT to Change

- **`literal_text` parameter** — Keep it. The reference doesn't have it, but it's useful and already in use.
- **Go regex fallback** — Keep the fallback for environments without ripgrep.
- **Permission system** — No changes needed to the existing read permission checks.
- **Response metadata struct** — Extend `GrepResponseMetadata` with new fields (mode, pagination) rather than replacing it.

## Implementation Plan

### Phase 1: Description + prompt fix (immediate impact, zero risk)
- [ ] **1.1** Rewrite `grepDescription` — remove bash encouragement, add "NEVER use bash" instruction
- [ ] **1.2** Update explorer/workhorse agent prompts

### Phase 2: Parameter surface expansion
- [ ] **2.1** Rename `include` → `glob` in `GrepParams` and schema (keep `include` as alias for backwards compat during transition)
- [ ] **2.2** Add `output_mode`, `case_insensitive`, `file_type`, `head_limit`, `offset`, `multiline` to `GrepParams`
- [ ] **2.3** Add `context`, `before_context`, `after_context` to `GrepParams`
- [ ] **2.4** Update `Info()` to expose all new parameters with descriptions

### Phase 3: Ripgrep invocation
- [ ] **3.1** Add `--hidden`, `--glob '!.git'` (and other VCS dirs), `--max-columns 500` to base args
- [ ] **3.2** Build rg args from new parameters (output_mode flags, context flags, etc.)
- [ ] **3.3** Handle `-e` prefix for patterns starting with dash

### Phase 4: Result processing
- [ ] **4.1** Implement `files_with_matches` mode processing (sort by mtime, pagination)
- [ ] **4.2** Implement `content` mode processing (relative paths, pagination)
- [ ] **4.3** Implement `count` mode processing (sum totals, pagination)
- [ ] **4.4** Update response format and metadata struct

### Phase 5: Tests
- [ ] **5.1** Table-driven tests for each output mode
- [ ] **5.2** Tests for pagination (head_limit, offset)
- [ ] **5.3** Tests for context lines, case insensitive, file type, multiline
- [ ] **5.4** Test fallback behavior when ripgrep unavailable
- [ ] **5.5** Integration test: verify `--hidden` finds dotfiles, `--max-columns` truncates long lines

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Parameter naming: `glob` vs `include` | Rename to `glob` | Matches rg flag name and reference impl. Less ambiguous. |
| Parameter naming: `-A`/`-B`/`-C` vs words | Use words (`before_context`, `after_context`, `context`) | Go JSON convention. Models handle words better than flag-style names. |
| Default output mode | `files_with_matches` | Matches reference. Most common use case. Cheapest in tokens. |
| Default head_limit | 250 | Matches reference. Current 100 is too conservative. |
| `--hidden` flag | Add it | Reference does it. Missing it means skipping `.github/`, config dotfiles. VCS dirs excluded via explicit `--glob '!.git'`. |
| `--max-columns` | 500 | Matches reference. Prevents minified JS / base64 from bloating results. |
| Multiline default | false | Matches reference. Multiline is expensive and rarely needed. |

## Success Criteria

- [ ] `grepDescription` contains "NEVER invoke `grep` or `rg` as a bash command"
- [ ] `grepDescription` does NOT contain "use the Bash tool"
- [ ] All three output modes work and produce clean output
- [ ] Pagination via head_limit/offset works correctly
- [ ] Context lines (-A/-B/-C) work in content mode
- [ ] Case-insensitive search works
- [ ] File type filter works
- [ ] `--hidden` is passed to rg (dotfiles are searchable)
- [ ] All existing tests pass: `make test`
- [ ] New table-driven tests cover all new parameters

## References

- `internal/llm/tools/grep.go` — Current implementation
- `internal/llm/prompt/explorer.go` — Explorer agent prompt
- `internal/llm/prompt/workhorse.go` — Workhorse agent prompt
- Claude Code reference: `src/tools/GrepTool/GrepTool.ts` and `src/tools/GrepTool/prompt.ts`
- Previous tools spec: `spec/20260223T133437-tools-imrovements.md` (items 1.3, 2.3, 2.4, 2.5 are superseded by this spec)
