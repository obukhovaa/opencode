# MultiEdit Tool & Edit Tool Improvements

**Date**: 2026-02-07
**Status**: Implemented
**Author**: AI-assisted

## Overview

Add a new `multiedit` tool that batches multiple find-and-replace operations on a single file into one atomic tool call, and improve the existing `edit` tool with a `replaceAll` parameter and an updated prompt.

## Motivation

### Current State

```go
// edit.go - parameters
type EditParams struct {
    FilePath  string `json:"file_path"`
    OldString string `json:"old_string"`
    NewString string `json:"new_string"`
}
```

```go
// edit.go - replace logic (replaceContent)
index := strings.Index(oldContent, oldString)
if index == -1 {
    return NewTextErrorResponse("old_string not found in file..."), nil
}
lastIndex := strings.LastIndex(oldContent, oldString)
if index != lastIndex {
    return NewTextErrorResponse("old_string appears multiple times..."), nil
}
newContent := oldContent[:index] + newString + oldContent[index+len(oldString):]
```

```go
// agent/tools.go - tool registration (no multiedit)
coderTools = append(
    []tools.BaseTool{
        tools.NewBashTool(permissions),
        tools.NewEditTool(lspClients, permissions, history),
        // ...
        tools.NewPatchTool(lspClients, permissions, history),
        tools.NewWriteTool(lspClients, permissions, history),
    }, otherTools...,
)
```

Problems:

1. **No `replaceAll` support**: When the LLM needs to rename a variable or replace a repeated pattern, it must either make separate edit calls for each occurrence or include enough context to uniquely match each one. The upstream TypeScript edit tool supports `replaceAll` to handle this efficiently.
2. **No multi-edit batching**: Making N edits to the same file requires N separate tool calls, each with full round-trip overhead (file read, permission request, file write, LSP diagnostics). The upstream has a `multiedit` tool that batches these into a single atomic operation.
3. **Edit prompt is outdated**: The current prompt doesn't mention `replaceAll`, doesn't guide the LLM to use it for renaming, and doesn't mention `multiedit` as a preferred alternative for multiple same-file edits.

### Desired State

```go
// edit.go - updated parameters
type EditParams struct {
    FilePath   string `json:"file_path"`
    OldString  string `json:"old_string"`
    NewString  string `json:"new_string"`
    ReplaceAll bool   `json:"replace_all,omitempty"`
}
```

```go
// multiedit.go - new tool
type MultiEditParams struct {
    FilePath string          `json:"file_path"`
    Edits    []MultiEditItem `json:"edits"`
}

type MultiEditItem struct {
    OldString  string `json:"old_string"`
    NewString  string `json:"new_string"`
    ReplaceAll bool   `json:"replace_all,omitempty"`
}
```

The edit tool accepts `replaceAll` to replace all occurrences. The multiedit tool applies a sequence of edits to a single file atomically, with a single permission request and a single LSP diagnostics pass.

## Research Findings

### Upstream TypeScript Implementation

| Aspect | Our Go Implementation | Upstream TypeScript |
|---|---|---|
| Edit params | `filePath`, `oldString`, `newString` | `filePath`, `oldString`, `newString`, `replaceAll` |
| Multi-match handling | Error: "appears multiple times" | If `replaceAll`: replace all; else error |
| MultiEdit tool | Does not exist | `multiedit` — batches edits on single file |
| MultiEdit execution | N/A | Sequential: each edit operates on result of previous |
| MultiEdit atomicity | N/A | All-or-nothing: if any edit fails, none applied |
| Permission model | Per-edit permission request | Single permission for multi-edit |
| LSP diagnostics | Per-edit diagnostics pass | Single diagnostics pass at end |
| Edit prompt | Verbose, doesn't mention `replaceAll` | Concise, mentions `replaceAll` for renaming |

**Key finding (multiedit.ts)**: The multiedit tool delegates to the edit tool's `execute` method sequentially. Each edit in the array operates on the result of the previous edit. The tool returns a combined result with all metadata.

```typescript
// Reference: multiedit.ts execute flow
for (const edit of params.edits) {
    const result = await editTool.execute({
        filePath: params.filePath,
        oldString: edit.oldString,
        newString: edit.newString,
        replaceAll: edit.replaceAll,
    }, ctx)
    results.push(result)
}
```

**Key finding (edit.txt prompt)**: The upstream edit prompt is much more concise and includes `replaceAll` guidance:
- "Use `replaceAll` for replacing and renaming strings across the file."
- Error messages reference `replaceAll` as an alternative to providing more context.

**Implication**: We should follow the same pattern — multiedit delegates to edit's core logic, but we can optimize by batching file I/O, permission requests, and LSP diagnostics into single operations rather than per-edit.

### Comparison with Existing Patch Tool

| Aspect | Patch Tool | MultiEdit Tool (proposed) |
|---|---|---|
| Scope | Multiple files | Single file |
| Format | Custom patch syntax | JSON array of edits |
| Matching | Context-line based (fuzzy) | Exact string match |
| Use case | Coordinated cross-file changes | Multiple edits to same file |
| Complexity | High (patch parsing, fuzz) | Low (sequential string replace) |

**Implication**: MultiEdit fills a gap between single-edit and patch — it's simpler than patch but more efficient than repeated single edits for same-file operations.

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Add `replaceAll` to edit | Optional bool, defaults to false | Matches upstream; enables variable renaming without multiple calls |
| `replaceAll` behavior | When true, use `strings.ReplaceAll`; skip uniqueness check | Simple, predictable behavior matching upstream |
| MultiEdit tool structure | Separate `multiedit.go` file | Follows codebase pattern (one tool per file) |
| MultiEdit execution | Read file once, apply edits sequentially in memory, write once | Optimized over N separate file I/O operations |
| MultiEdit permission | Single permission request with combined diff | Reduces permission prompt fatigue |
| MultiEdit LSP diagnostics | Single pass after all edits applied | Avoids N diagnostic passes |
| MultiEdit atomicity | Compute all edits in memory first, write only if all succeed | Prevents partial application |
| MultiEdit history | One history version for the final result | Matches the "atomic operation" semantics |
| Edit prompt update | Adopt upstream prompt style, add `replaceAll` guidance | Better LLM guidance, mention `replaceAll` for renaming |
| Edit error messages | Reference `replaceAll` when multiple matches found | Guide LLM toward efficient solution |

## Architecture

### Edit Tool — replaceAll Flow

```
LLM sends edit tool call
    │
    ├── filePath: "/path/to/file"
    ├── oldString: "oldVar"
    ├── newString: "newVar"
    └── replaceAll: true              ← NEW
         │
         ▼
    replaceContent()
         │
         ├── replaceAll=true?
         │     ├── YES → strings.ReplaceAll(content, old, new)
         │     └── NO  → existing unique-match logic
         │
         ▼
    permission request → write file → LSP diagnostics
```

### MultiEdit Tool — Execution Flow

```
LLM sends multiedit tool call
    │
    ├── filePath: "/path/to/file"
    └── edits: [
    │     { oldString: "A", newString: "B" },
    │     { oldString: "C", newString: "D", replaceAll: true },
    │   ]
    │
    ▼
STEP 1: Validate & Read
────────────────────────
- Validate params (filePath required, edits non-empty)
- Read file once
- Check file was previously read (getLastReadTime)
- Check file not modified since read

STEP 2: Apply Edits in Memory
──────────────────────────────
- For each edit in order:
  - Apply edit.oldString → edit.newString on current content
  - If replaceAll: strings.ReplaceAll
  - Else: unique match check + single replace
  - If any edit fails → return error, no file changes

STEP 3: Permission & Write
───────────────────────────
- Generate diff (original → final)
- Single permission request with combined diff
- Write file once
- Update file history (one version)

STEP 4: LSP Diagnostics
────────────────────────
- Single LSP diagnostics pass
- Return combined result
```

### File Structure

```
internal/llm/tools/
    ├── edit.go          ← modify: add replaceAll param + logic
    ├── multiedit.go     ← new file
    └── ...
internal/llm/agent/
    └── tools.go         ← modify: register multiedit tool
internal/tui/components/chat/
    └── message.go       ← modify: add multiedit rendering
```

## Implementation Plan

### Phase 1: Edit Tool — Add `replaceAll`

- [ ] **1.1** Update `EditParams` struct: add `ReplaceAll bool` field with `json:"replace_all,omitempty"`
- [ ] **1.2** Update `Info()` method: add `replace_all` parameter to schema (optional boolean)
- [ ] **1.3** Update `replaceContent()` method:
  - When `replaceAll` is true: use `strings.ReplaceAll(oldContent, oldString, newString)`, skip the uniqueness check
  - When `replaceAll` is false: keep existing behavior (unique match required)
- [ ] **1.4** Update `deleteContent()` method: add `replaceAll` support (same pattern — when true, replace all occurrences with empty string)
- [ ] **1.5** Update edit prompt (`editDescription`):
  - Add `replace_all` parameter documentation
  - Add guidance: "Use `replaceAll` for replacing and renaming strings across the file"
  - Update error message when multiple matches found to mention `replaceAll` as alternative
  - Match upstream prompt style for clarity

### Phase 2: MultiEdit Tool

- [ ] **2.1** Create `internal/llm/tools/multiedit.go`:
  - Define `MultiEditParams`, `MultiEditItem`, `MultiEditResponseMetadata` structs
  - Define `MultiEditToolName = "multiedit"` constant
  - Define prompt/description following upstream `multiedit.txt` content
  - Implement `multiEditTool` struct with same dependencies as `editTool` (lspClients, permissions, files)
  - Implement `NewMultiEditTool()` constructor
  - Implement `Info()` method with parameter schema
  - Implement `Run()` method:
    1. Parse params, validate filePath and edits array
    2. Read file once, check read-time and mod-time
    3. Apply edits sequentially in memory (reuse edit logic)
    4. Generate combined diff (original → final)
    5. Single permission request
    6. Write file once
    7. Update history once
    8. Run LSP diagnostics once
    9. Return combined result with metadata

### Phase 3: Integration

- [ ] **3.1** Register multiedit tool in `internal/llm/agent/tools.go` — add `tools.NewMultiEditTool(lspClients, permissions, history)` to `CoderAgentTools`
- [ ] **3.2** Update TUI rendering in `internal/tui/components/chat/message.go`:
  - Add `MultiEditToolName` case to `getToolTitle()` → "MultiEdit"
  - Add `MultiEditToolName` case to `getToolAction()` → "Preparing edits..."
  - Add `MultiEditToolName` case to `renderToolParams()` → show file path and edit count
  - Add `MultiEditToolName` case to `renderToolResponse()` → show combined diff
- [ ] **3.3** Generate schema: `go run cmd/schema/main.go > opencode-schema.json`

### Phase 4: Testing

- [ ] **4.1** Add tests for `replaceAll` in edit tool (table-driven: replaceAll true/false, single/multiple matches)
- [ ] **4.2** Add tests for multiedit tool (sequential edits, atomic failure, combined diff)
- [ ] **4.3** Run `make test` to verify no regressions

## Edge Cases

### replaceAll with No Matches

1. `replaceAll` is true but `oldString` not found in file
2. `strings.ReplaceAll` returns the content unchanged
3. Should return error: "old_string not found in file"

### replaceAll with oldString == newString

1. `replaceAll` is true, `oldString` equals `newString`
2. Content unchanged after replace
3. Should return error: "new content is the same as old content"

### MultiEdit — Empty Edits Array

1. `edits` array is empty
2. Should return error: "edits array must not be empty"

### MultiEdit — Edit Fails Mid-Sequence

1. Edit 1 succeeds (in memory), edit 2 fails (old_string not found after edit 1 changed content)
2. No file changes written — atomic rollback
3. Return error indicating which edit failed

### MultiEdit — Later Edit Depends on Earlier Edit

1. Edit 1 changes "foo" → "bar", edit 2 looks for "bar" (the result of edit 1)
2. This works because edits apply sequentially on the in-memory content
3. This is the expected behavior per upstream

### MultiEdit — Create New File

1. `oldString` is empty in first edit (create file mode)
2. MultiEdit should not support file creation — use edit tool for that
3. Return error: "multiedit does not support file creation (old_string cannot be empty)"

### Backward Compatibility

1. Existing edit tool calls without `replaceAll` field
2. JSON unmarshaling with `omitempty` handles missing field → defaults to `false`
3. Existing behavior preserved

## Open Questions

1. **Should multiedit support file creation (empty oldString)?**
   - Upstream's multiedit allows first edit with empty oldString to create file
   - Our edit tool has separate `createNewFile` path with special handling
   - **Recommendation**: Don't support it in multiedit — keep it simple. Use edit tool for file creation.

2. **Should multiedit share the `editTool` struct or be independent?**
   - Sharing allows reusing `replaceContent`/`deleteContent` methods
   - Independent implementation is cleaner and allows optimization (single file read/write)
   - **Recommendation**: Independent struct, but extract shared replace logic into a helper function that both tools use.

3. **Should the `replaceAll` parameter also apply to `deleteContent`?**
   - When `oldString` is provided and `newString` is empty, current behavior deletes first unique match
   - With `replaceAll`, it would delete all occurrences
   - **Recommendation**: Yes, support it for consistency. Same logic pattern.

4. **Permission request granularity for multiedit?**
   - One permission request per edit (matches current edit behavior)
   - One combined permission request for all edits (better UX)
   - **Recommendation**: Single combined permission request showing the full diff from original to final state. Less prompt fatigue, matches atomic semantics.

## Success Criteria

- [ ] Edit tool accepts `replace_all` parameter and replaces all occurrences when true
- [ ] Edit tool errors when `replace_all` is false and multiple matches exist (existing behavior preserved)
- [ ] Edit tool prompt mentions `replaceAll` for renaming use cases
- [ ] MultiEdit tool applies multiple edits to a single file atomically
- [ ] MultiEdit tool reads file once, writes once, runs diagnostics once
- [ ] MultiEdit tool rolls back (no write) if any edit fails
- [ ] MultiEdit tool registered in coder agent tools
- [ ] MultiEdit tool renders correctly in TUI
- [ ] `go build ./...` and `go vet ./...` pass
- [ ] `make test` passes with no regressions

## References

- `internal/llm/tools/edit.go` — Edit tool to modify
- `internal/llm/tools/patch.go` — Similar multi-file tool for pattern reference
- `internal/llm/tools/tools.go` — Tool interfaces and response types
- `internal/llm/agent/tools.go` — Tool registration
- `internal/tui/components/chat/message.go` — TUI tool rendering
- [Upstream multiedit.ts](https://github.com/anomalyco/opencode/blob/fedf9feba8c82c874e250d2fcc108b008fcf5212/packages/opencode/src/tool/multiedit.ts)
- [Upstream multiedit.txt](https://github.com/anomalyco/opencode/blob/fedf9feba8c82c874e250d2fcc108b008fcf5212/packages/opencode/src/tool/multiedit.txt)
- [Upstream edit.ts](https://github.com/anomalyco/opencode/blob/fedf9feba8c82c874e250d2fcc108b008fcf5212/packages/opencode/src/tool/edit.ts)
- [Upstream edit.txt](https://github.com/anomalyco/opencode/blob/fedf9feba8c82c874e250d2fcc108b008fcf5212/packages/opencode/src/tool/edit.txt)
