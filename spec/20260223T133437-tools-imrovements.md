# Tools Improvements

**Date**: 2025-02-23
**Status**: Draft
**Author**: AI-assisted

## Overview

Systematic improvements to the existing tool implementations based on a comparative analysis of a reference implementation. Changes cover description/prompt updates, bug fixes, robustness improvements, and feature additions across 12 tools.

## Motivation

### Current State

The tools work but have gaps relative to best-in-class reference implementations:

- **Inconsistent descriptions**: Some tools lack guidance on when to prefer other tools or how to batch calls efficiently.
- **Fragile parsing**: `grep` splits on `:` which breaks on file paths or content containing colons.
- **Sort order bug**: `glob` description says "sorted by modification time" but code sorts by path length.
- **Discarded output**: `bash` truncation permanently discards the middle of large outputs instead of writing to a temp file.
- **No fuzzy matching**: `edit` requires exact string matches — minor whitespace or indentation differences cause failures.
- **Unfiltered diagnostics**: `edit`/`write`/`multiedit` return all LSP diagnostics including warnings, making output noisy.

### Desired State

Every tool has accurate descriptions with cross-tool guidance, robust parsing, correct sort behavior, and improved error messages. Large bash outputs are recoverable. Edit operations tolerate minor whitespace differences.

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Fuzzy matching scope | Deferred to later phase | High complexity, needs careful design to avoid false positives |
| `view` offset indexing | Keep 0-based | Breaking change for existing prompts; not worth the churn |
| `ls` file limit | Keep 1000 | 100 is too conservative for project exploration |
| `view` PDF support | Out of scope | Requires new dependency, low priority |
| `view` directory listing | Out of scope | `ls` tool already covers this use case |
| `grep --hidden` | Do not add | Current behavior (skip hidden) matches description and is the safer default |
| Multi-file multiedit | Deferred | Significant API change, evaluate after other improvements |

## Implementation Plan

### Phase 1: Description and Prompt Improvements

Low-risk changes to tool descriptions that guide the LLM toward better tool usage.

- [x] **1.1 bash**: In `bashDescriptionTemplate`, explicitly name `view` and `write` as the tools to prefer over `cat`/`head`/`tail`/`echo`/`sed` (currently says "dedicated tools" generically). Add explicit encouragement: "When issuing multiple independent commands, make multiple Bash tool calls in a single message rather than chaining with `&&`."
  - File: `internal/llm/tools/bash.go`

- [x] **1.2 glob**: Add to `globDescription`: "When you are doing an open-ended search that may require multiple rounds of globbing and grepping, use the Task tool instead, to find the match more efficiently." and "You have the capability to call multiple tools in a single response. It is always better to speculatively perform multiple searches as a batch."
  - File: `internal/llm/tools/glob.go`

- [x] **1.3 grep**: Add to `grepDescription`: "If you need to identify or count the number of matches within files, use the Bash tool with `rg` directly." and "When you are doing an open-ended search that may require multiple rounds of globbing and grepping, use the Task tool instead."
  - File: `internal/llm/tools/grep.go`

- [x] **1.4 ls**: Add to `lsDescription`: "You should generally prefer the Glob and Grep tools if you know which directories or file patterns to search for."
  - File: `internal/llm/tools/ls.go`

- [x] **1.5 multiedit**: Add to description: "Prefer this tool over the Edit tool when you need to make multiple edits to the same file." and document the create-file-via-multiedit pattern: "To create a new file, use an empty `old_string` and the full file contents as `new_string` in the first edit."
  - File: `internal/llm/tools/multiedit.go`

- [x] **1.6 write**: Add behavioral constraints to description: "ALWAYS prefer editing existing files in the codebase over writing entirely new content. NEVER proactively create documentation files (*.md) or README files unless explicitly requested. Only use emojis if the user explicitly requests it."
  - File: `internal/llm/tools/write.go`

- [x] **1.7 view**: Add to description: "Avoid tiny repeated slices (e.g. 30-line chunks). If you need more context, read a larger window in a single call." Improve continuation hint from `"Use 'offset' parameter to read beyond line N"` to `"Showing lines X-Y of Z total. Use offset=N to continue reading."`.
  - File: `internal/llm/tools/view.go`

- [x] **1.8 fetch**: Add to `fetchToolDescription`: "IMPORTANT: if another tool is available that offers better web fetching capabilities (such as an MCP tool), prefer using that tool instead." Make `format` default to `"markdown"` so it becomes an optional parameter (remove from `Required` slice).
  - File: `internal/llm/tools/fetch.go`

- [x] **1.9 patch**: Add to `patchDescription`: fuzz tolerance documentation ("Context lines are matched with a fuzz tolerance of up to 3 lines — if the context drifts by more than 3 lines from the expected position, the patch is rejected"). Make the `Move to:` directive more prominent with its own example section.
  - File: `internal/llm/tools/patch.go`

- [x] **1.10 lsp**: Change description from "LSP servers must be running" to "LSP servers must be configured for the file type."
  - File: `internal/llm/tools/lsp.go`

- [x] **1.11 skill**: Change skill locations in `<available_skills>` XML and output from plain filesystem paths to `file://` URLs for consistency with the reference approach. Example: `<location>file:///Users/x/.agents/skills/git-release</location>`.
  - File: `internal/llm/tools/skill.go`

### Phase 2: Bug Fixes and Robustness

Medium-effort changes that fix incorrect behavior or improve reliability.

- [ ] **2.1 glob: Fix sort order** — Current code sorts results by path length, but the description and LLM-facing prompt say "sorted by modification time (newest first)." Fix `globFiles()` to sort by `os.Stat().ModTime()` descending. Cache stat results to avoid double-stat when truncating.
  - File: `internal/llm/tools/glob.go`
  - Current: `sort.Slice(files, func(i, j int) { return len(files[i]) < len(files[j]) })`
  - Target: Sort by `ModTime` descending, fall back to path length if stat fails.

- [ ] **2.2 glob: Validate path is a directory** — Currently the `path` parameter is not validated as a directory before searching. Add an `os.Stat` + `IsDir()` check and return a clear error if the path is a file.
  - File: `internal/llm/tools/glob.go`

- [ ] **2.3 grep: Use field-match-separator** — Replace colon-based output parsing with `--field-match-separator=|` flag for ripgrep. This avoids misparse when file paths or matched content contain colons (e.g., `C:\Users\...` on Windows, `http://...` in code).
  - File: `internal/llm/tools/grep.go`
  - Current: `strings.SplitN(line, ":", 3)`
  - Target: `rg --field-match-separator='|'` + `strings.SplitN(line, "|", 3)`

- [ ] **2.4 grep: Handle broken symlinks gracefully** — Add `--no-messages` flag to ripgrep invocation to suppress error output for broken symlinks and permission-denied files. If exit code is 2 (partial results), still return results with a note: "(Some paths were inaccessible and skipped)".
  - File: `internal/llm/tools/grep.go`

- [ ] **2.5 grep: Informative truncation message** — Change truncation message from generic text to include match counts: "(Results truncated: showing N of M matches. Consider using a more specific path or pattern.)"
  - File: `internal/llm/tools/grep.go`

- [ ] **2.6 edit: Normalize line endings** — Normalize CRLF to LF before performing string matching and diff generation. Apply to both the file content and the `old_string`/`new_string` parameters. This prevents spurious diff noise on Windows-edited files.
  - File: `internal/llm/tools/edit.go`
  - Add: `content = strings.ReplaceAll(content, "\r\n", "\n")` before matching.

- [ ] **2.7 edit: Actionable multiple-match error** — Current error says "old_string appears multiple times in the file." Improve to: "old_string appears N times in the file. Please provide more surrounding context lines in old_string to make the match unique, or use replace_all=true to replace all occurrences."
  - File: `internal/llm/tools/edit.go`

- [ ] **2.8 LSP diagnostics: Filter and cap** — Across `edit`, `multiedit`, `write`, and `patch`, filter LSP diagnostics to errors only (severity 1) and cap at 20 diagnostics per file. Currently all severities are returned without limit.
  - File: `internal/llm/tools/edit.go`, `multiedit.go`, `write.go`, `patch.go`
  - Also requires changes in: `internal/lsp/diagnostics.go` (add severity filter + cap parameters to `FormatDiagnostics`)

- [ ] **2.9 edit/write: Trim common leading indentation from diffs** — Implement a `trimDiff()` helper that strips the longest common whitespace prefix from all lines in a unified diff, making diffs more readable in tool responses. Apply to `edit`, `multiedit`, `write`, and `patch` tools.
  - New shared helper in: `internal/llm/tools/file.go` or `internal/diff/diff.go`

- [ ] **2.10 ls: Extend ignore patterns** — Add missing common directories to the auto-ignore list: `.zig-cache/`, `zig-out`, `.coverage`, `coverage/`, `logs/`, `.venv/`, `venv/`, `env/`, `tmp/`, `temp/`, `.cache/`, `cache/`.
  - File: `internal/llm/tools/ls.go`

- [ ] **2.11 ls: Sort directories before files, alphabetical** — Current implementation uses `filepath.Walk` order which is lexicographic but doesn't separate directories from files. Modify `buildTree` or the rendering to show subdirectories before files within each level, both sorted alphabetically.
  - File: `internal/llm/tools/ls.go`

- [ ] **2.12 lsp: Validate file existence before checking clients** — Current code checks for LSP clients first, then opens the file. Reverse the order: check file exists first (via `os.Stat`), then check for LSP clients. This produces clearer error messages when the file simply doesn't exist.
  - File: `internal/llm/tools/lsp.go`

- [ ] **2.13 fetch: Send Accept headers** — Set format-appropriate `Accept` headers on the HTTP request:
  - `text`: `Accept: text/plain;q=1.0, text/html;q=0.5`
  - `markdown`: `Accept: text/markdown;q=1.0, text/html;q=0.7`
  - `html`: `Accept: text/html;q=1.0`
  - File: `internal/llm/tools/fetch.go`

- [ ] **2.14 fetch: Pre-check Content-Length** — Before reading the response body, check the `Content-Length` header. If it exceeds 5MB, return an error immediately instead of downloading and then discarding.
  - File: `internal/llm/tools/fetch.go`

- [ ] **2.15 view: Binary file detection by content** — In addition to extension-based image detection, sample the first 4096 bytes and check for null bytes or >30% non-printable characters. If binary, return a clear error: "File appears to be binary. Use the appropriate tool for this file type."
  - File: `internal/llm/tools/view.go`

- [ ] **2.16 view: Improve continuation output** — Change the footer message format from `"(File has more lines. Use 'offset' parameter to read beyond line N)"` to `"(Showing lines X-Y of Z total. Use offset=N to continue reading.)"` where X, Y, Z are actual line numbers.
  - File: `internal/llm/tools/view.go`

- [ ] **2.17 skill: Use ripgrep for file enumeration** — Replace `os.ReadDir`-based directory walk in `sampleSkillFiles` with ripgrep (`rg --files --hidden`) to pick up hidden files that the current walk misses.
  - File: `internal/llm/tools/skill.go`

### Phase 3: Significant Features (Deferred)

High-effort changes that require careful design. Each should get its own focused spec before implementation.

- [ ] **3.1 bash: Full output persistence** — When output exceeds truncation limits, write the full output to a temp file and return the path in the response so the agent can paginate through it with the `view` tool. Current behavior permanently discards the middle section.
  - File: `internal/llm/tools/bash.go`
  - Design considerations: temp file cleanup strategy, disk space limits, session-scoped temp directory.

- [ ] **3.2 ls: Use ripgrep for file enumeration** — Replace `filepath.Walk` with ripgrep-based file enumeration (`rg --files`) to automatically respect `.gitignore` rules. The current walker ignores `.gitignore` entirely, listing files that are irrelevant to the project.
  - File: `internal/llm/tools/ls.go`
  - Design considerations: fallback when rg is not installed, performance impact, handling of directories-only mode.

- [ ] **3.3 edit: Fuzzy matching pipeline** — Implement a multi-strategy matching pipeline for `old_string` lookup when exact match fails. At minimum: whitespace-normalized matching, indentation-flexible matching, and trimmed-boundary matching. The reference uses 9 strategies with Levenshtein distance scoring.
  - Files: `internal/llm/tools/edit.go`, new file `internal/llm/tools/edit_match.go`
  - Design considerations: false positive risk (matching wrong code section), performance on large files, strategy ordering, confidence thresholds.
  - **Recommendation**: Start with 3-4 strategies (exact → whitespace-normalized → indentation-flexible → trimmed-boundary) and measure edit failure rates before adding more.

- [ ] **3.4 fetch: Cloudflare challenge detection** — Detect Cloudflare bot challenges (HTTP 403 + `cf-mitigated: challenge` header) and retry with a simpler `User-Agent`. This handles a common failure mode when fetching documentation sites.
  - File: `internal/llm/tools/fetch.go`
  - Design considerations: retry limit, alternative user-agents, detection of other WAF providers.

- [ ] **3.5 write: Cross-file LSP diagnostics** — After writing a file, collect LSP diagnostics not just for the written file but for up to 5 other project files. This catches import errors and type mismatches introduced by the write.
  - Files: `internal/llm/tools/write.go`, `internal/lsp/diagnostics.go`
  - Design considerations: which files to check (imports, recently viewed), timeout for cross-file diagnostics, output format.

## Open Questions

1. **Should `view` switch to 1-based offset?**
   - The reference uses 1-based indexing which is more intuitive. However, this is a breaking change for existing agent prompts and cached system messages.
   - **Recommendation**: Keep 0-based. The cost of breaking existing behavior outweighs the marginal usability gain.

2. **Should `ls` limit be reduced to 100?**
   - The reference caps at 100 files. Our 1000-file limit can produce very large responses.
   - **Recommendation**: Keep 1000 but investigate token usage. If most `ls` calls return <100 files anyway, the limit is academic.

3. **Should `multiedit` support multiple files per call?**
   - The reference allows each edit to specify its own `filePath`. This is convenient but changes the API surface.
   - **Recommendation**: Defer. The current single-file atomic approach is simpler and safer. Evaluate after Phase 2.

4. **Fuzzy matching confidence threshold**
   - When multiple fuzzy strategies match, how do we pick the best one? The reference uses Levenshtein distance.
   - **Recommendation**: Defer until Phase 3. Start with a simple ordered pipeline (first match wins) and iterate based on real-world failure data.

## Success Criteria

- [x] All Phase 1 description changes are applied and verified by running `go build ./...`
- [ ] `glob` sort order matches its description (mod time, not path length) — verified by test
- [ ] `grep` correctly parses lines containing colons (new test case)
- [ ] LSP diagnostics are filtered to errors only with a cap of 20 per file — verified by test
- [ ] `edit` returns actionable error messages on multiple matches — verified by test
- [ ] All existing tests pass: `make test`

## References

- `internal/llm/tools/bash.go` — Bash tool, truncation logic, description template
- `internal/llm/tools/edit.go` — Edit tool, string matching, permission checks
- `internal/llm/tools/glob.go` — Glob tool, sort order bug, path validation
- `internal/llm/tools/grep.go` — Grep tool, ripgrep parsing, truncation messages
- `internal/llm/tools/ls.go` — LS tool, ignore patterns, sort order
- `internal/llm/tools/lsp.go` — LSP tool, operation order, description wording
- `internal/llm/tools/multiedit.go` — MultiEdit tool, atomic writes, description
- `internal/llm/tools/write.go` — Write tool, diagnostics, behavioral constraints
- `internal/llm/tools/view.go` — View tool, binary detection, continuation hints
- `internal/llm/tools/fetch.go` — Fetch tool, Accept headers, Content-Length
- `internal/llm/tools/skill.go` — Skill tool, file:// URLs, ripgrep enumeration
- `internal/llm/tools/patch.go` — Patch tool, fuzz documentation, Move to directive
- `internal/llm/tools/file.go` — Shared file tracking utilities
- `internal/llm/tools/tools.go` — Shared types, truncation, response constructors
- `internal/lsp/diagnostics.go` — LSP diagnostics formatting (needs severity filter)
- `internal/diff/diff.go` — Diff generation (potential home for trimDiff helper)
