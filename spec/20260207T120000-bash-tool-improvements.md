# Bash Tool Improvements

**Date**: 2026-02-07
**Status**: Implemented
**Author**: AI-assisted

## Overview

Improve the bash tool to add `workdir` and `description` parameters, adopt the upstream prompt template with proper macro variable substitution (`${directory}`, `${maxBytes}`), switch output truncation from character-counting to byte-counting, display the `description` in TUI tool call rendering, and remove the hardcoded banned commands list.

## Motivation

### Current State

```go
// bash.go - parameters
type BashParams struct {
    Command string `json:"command"`
    Timeout int    `json:"timeout"`
}
```

```go
// bash.go - truncation uses character count
const MaxOutputLength = 30000

func truncateOutput(content string) string {
    if len(content) <= MaxOutputLength { // len() counts bytes in Go, but constant name says "chars"
        return content
    }
    // ...
}
```

```go
// bash.go - hardcoded banned commands
var bannedCommands = []string{
    "alias", "curl", "curlie", "wget", "axel", "aria2c",
    "nc", "telnet", "lynx", "w3m", "links", "httpie", "xh",
    "http-prompt", "chrome", "firefox", "safari",
}
```

```go
// message.go - bash tool rendering shows command only
case tools.BashToolName:
    var params tools.BashParams
    json.Unmarshal([]byte(toolCall.Input), &params)
    command := strings.ReplaceAll(params.Command, "\n", " ")
    return renderParams(paramWidth, command)
```

Problems:

1. **No `workdir` parameter**: Users must use `cd <dir> && <command>` patterns, which is error-prone and clutters commands. The upstream TypeScript implementation supports `workdir` natively.
2. **No `description` parameter**: The LLM cannot communicate intent for a bash command. The TUI shows "Building command..." generically. The upstream implementation uses `description` as the tool call title and in metadata.
3. **Outdated prompt**: Our prompt diverges significantly from upstream. It references character counts instead of byte/line limits, has a different structure, and lacks the `workdir` usage guidance.
4. **Character vs byte counting inconsistency**: `MaxOutputLength` is named as characters but Go's `len()` on strings counts bytes. The upstream uses explicit byte counting (`Buffer.byteLength`) and line counting separately.
5. **Hardcoded banned commands are unnecessary**: The upstream TypeScript implementation removed banned commands entirely. Instead, it relies on the permission system (tree-sitter-based command parsing + user approval) to gate dangerous operations. The ban list is overly broad (blocks `curl` which is legitimately useful) and provides a false sense of security since commands can be trivially aliased or wrapped.

### Desired State

```go
type BashParams struct {
    Command     string `json:"command"`
    Timeout     int    `json:"timeout"`
    Workdir     string `json:"workdir"`
    Description string `json:"description"`
}
```

The prompt uses `${directory}` and `${maxBytes}` macro variables like the upstream template. TUI shows the `description` when available, falling back to the command. Output truncation uses byte-based limits with clear naming.

## Research Findings

### Upstream TypeScript Implementation Comparison

| Aspect | Our Go Implementation | Upstream TypeScript |
|---|---|---|
| Parameters | `command`, `timeout` | `command`, `timeout`, `workdir`, `description` |
| Banned commands | Hardcoded list of 16 commands | None — relies on permission system |
| Truncation | `MaxOutputLength = 30000` (chars/bytes ambiguous) | `MAX_LINES = 2000`, `MAX_BYTES = 50 * 1024` (separate line+byte limits) |
| Metadata | `{start_time, end_time}` | `{output, exit, description}` |
| Prompt template | Inline string with `%s`/`%d` sprintf | External `.txt` file with `${directory}`, `${maxLines}`, `${maxBytes}` macros |
| Workdir | Not supported | `params.workdir \|\| Instance.directory` |
| Description | Not supported | Stored in metadata, used as tool call title, truncated at 30KB for metadata |
| Permission model | Banned command list + safe read-only list + permission request | Tree-sitter AST parsing + permission request (no ban list) |

**Key finding**: The upstream removed banned commands because the permission system already gates command execution. The ban list was a redundant layer that blocked legitimate use cases (like `curl` for API testing).

**Key finding**: The upstream uses description in metadata with a 30KB truncation limit (`MAX_METADATA_LENGTH = 30_000`), and passes it through to the TUI as the tool call title.

**Implication**: We should adopt the upstream approach for parameters and prompt, but keep our existing permission system (which already requests user approval for non-safe-read-only commands) rather than porting tree-sitter AST parsing.

### Banned Commands Analysis

The upstream implementation removed banned commands because:

1. The permission system already asks users to approve non-read-only commands
2. Commands like `curl` are legitimately useful for API testing, health checks, etc.
3. The ban is trivially bypassed (e.g., `/usr/bin/curl`, `command curl`, piping through other tools)
4. Modern LLM agents need network access for legitimate tasks (checking APIs, downloading dependencies)
5. The `fetch` tool already exists for HTTP requests, making `curl` blocking inconsistent

Our existing `safeReadOnlyCommands` list + permission request flow already provides adequate protection. Removing banned commands brings us in line with upstream while reducing false-positive blocks.

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Add `workdir` parameter | Optional string, defaults to `config.WorkingDirectory()` | Matches upstream; eliminates `cd && cmd` pattern |
| Add `description` parameter | Required string (in schema), but tolerate empty at runtime | Upstream makes it required; provides context for TUI display |
| Description truncation in metadata | Truncate at 80 chars for TUI display | Keeps tool call display compact |
| Remove banned commands | Remove `bannedCommands` list entirely | Upstream removed it; permission system already gates execution |
| Keep safe-read-only commands | Retain `safeReadOnlyCommands` list | Still useful for skipping permission prompts on harmless commands |
| Output truncation | Rename to `MaxOutputBytes` (50KB), add `MaxOutputLines` (2000) | Match upstream approach; clearer naming |
| Prompt template | Adopt upstream `bash.txt` content with Go string replacer | Better prompt with `workdir` guidance, git safety protocol |
| Metadata structure | Add `Description` field to `BashResponseMetadata` | Enables TUI to show description |
| TUI rendering | Show description when available, fall back to command | Better UX: user sees intent, not raw command |

## Architecture

### Parameter Flow

```
LLM sends tool call
    │
    ├── command: "go test ./..."
    ├── timeout: 60000
    ├── workdir: "/foo/bar"       ← NEW
    └── description: "Run tests"  ← NEW
         │
         ▼
    BashParams struct
         │
         ├── workdir → shell.GetPersistentShell(workdir)  ← pass to shell
         │               (falls back to config.WorkingDirectory())
         │
         ├── description → BashResponseMetadata.Description
         │                    │
         │                    ▼
         │               message.go renderToolParams()
         │                    │
         │                    ▼
         │               TUI shows: "Run tests" (styled)
         │                          "go test ./..."
         │
         └── command → shell.Exec(ctx, command, timeout)
```

### Metadata Flow

```
BashResponseMetadata {
    StartTime:   int64
    EndTime:     int64
    Description: string    ← NEW
    ExitCode:    int       ← NEW (nice-to-have)
}
```

### TUI Rendering (message.go)

```
┌──────────────────────────────────────┐
│ ⚡ Bash                              │
│ Run tests                            │  ← description (dimmed/italic)
│ go test ./...                        │  ← command
└──────────────────────────────────────┘
```

When description is empty, fall back to current behavior (show command only).

## Implementation Plan

### Phase 1: Core Parameter Changes

- [ ] **1.1** Update `BashParams` struct: add `Workdir string` and `Description string` fields with JSON tags
- [ ] **1.2** Update `BashPermissionsParams` struct: add `Workdir string` field
- [ ] **1.3** Update `BashResponseMetadata` struct: add `Description string` and `ExitCode int` fields
- [ ] **1.4** Update `Info()` method: add `workdir` and `description` parameter definitions to the schema
- [ ] **1.5** Update `Run()` method:
  - Use `params.Workdir` (falling back to `config.WorkingDirectory()`) when getting shell and executing
  - Pass workdir to `shell.GetPersistentShell()` and `permission.CreatePermissionRequest.Path`
  - Store `params.Description` in response metadata

### Phase 2: Prompt Template Update

- [ ] **2.1** Replace the `bashDescription()` function body with the upstream `bash.txt` content
- [ ] **2.2** Use `strings.NewReplacer` for macro substitution: `${directory}` → `config.WorkingDirectory()`, `${maxBytes}` → `MaxOutputBytes`, `${maxLines}` → `MaxOutputLines`
- [ ] **2.3** Remove the `bannedCommands` slice and the banned command check in `Run()`
- [ ] **2.4** Remove the `bannedCommandsStr` formatting from the description

### Phase 3: Output Truncation Update

- [ ] **3.1** Rename `MaxOutputLength` → `MaxOutputBytes` and set to `50 * 1024` (50KB)
- [ ] **3.2** Add `MaxOutputLines = 2000` constant
- [ ] **3.3** Update `truncateOutput()` to check both byte size and line count
- [ ] **3.4** Update the prompt template to reference `${maxBytes}` and `${maxLines}` instead of `%d characters`

### Phase 4: TUI Display

- [ ] **4.1** Update `renderToolParams()` in `message.go` for `BashToolName`:
  - Parse `Description` from params
  - If description present, render it styled (dimmed/italic) above the command
  - Fall back to command-only display if no description
- [ ] **4.2** Update `getToolAction()` to use description as action text when available (truncated for display)

## Edge Cases

### Empty Workdir

1. `workdir` is empty string or not provided
2. Fall back to `config.WorkingDirectory()`
3. Existing behavior preserved

### Empty Description

1. `description` is empty or not provided
2. TUI falls back to showing command only
3. Metadata stores empty string

### Workdir Outside Project

1. `workdir` points to directory outside project root
2. Permission system already handles this via `Path` field
3. Command executes in requested directory if approved

### Very Long Description

1. Description exceeds display width
2. Truncate with ellipsis for TUI display (80 char limit)
3. Full description stored in metadata

### Backward Compatibility

1. Existing tool calls without `workdir`/`description` fields
2. JSON unmarshaling with `omitempty` handles missing fields gracefully
3. Zero values trigger fallback behavior

## Open Questions

1. **Should `description` be required in the JSON schema?**
   - The upstream makes it required (no `.optional()`)
   - Our Go implementation can make it required in the schema but tolerate empty at runtime
   - **Recommendation**: Make it required in schema (so LLMs always provide it) but don't error on empty

2. **Should we persist the shell with workdir or create new shells per workdir?**
   - Current `GetPersistentShell(dir)` creates one shell per working directory
   - Upstream spawns a new process per invocation
   - **Recommendation**: Keep current approach — pass workdir to `GetPersistentShell()`, which already handles per-directory shells

3. **Should we adopt the upstream's line+byte dual truncation or keep simpler byte-only?**
   - Upstream uses both `MAX_LINES` and `MAX_BYTES` with full output saved to file
   - Our truncation is simpler (middle-truncate with line count)
   - **Recommendation**: Adopt dual truncation (bytes + lines) to match upstream behavior. Keep middle-truncation style.

## Success Criteria

- [ ] `workdir` parameter is accepted and used as shell working directory
- [ ] `description` parameter is accepted and stored in metadata
- [ ] TUI displays description above command in bash tool calls
- [ ] Prompt template matches upstream content with proper macro substitution
- [ ] Banned commands list is removed; permission system handles safety
- [ ] Output truncation uses byte-based limit (`MaxOutputBytes`)
- [ ] Existing tests pass; no regressions in bash tool behavior
- [ ] `go build ./...` and `go vet ./...` pass

## References

- `internal/llm/tools/bash.go` — Main implementation to modify
- `internal/llm/tools/tools.go` — `ToolResponse`, `ToolInfo`, metadata helpers
- `internal/llm/tools/shell/shell.go` — Shell execution layer
- `internal/tui/components/chat/message.go` — TUI rendering of tool calls
- `internal/permission/permission.go` — Permission service
- `internal/config/config.go` — `WorkingDirectory()` function
- [Upstream bash.ts](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/tool/bash.ts)
- [Upstream bash.txt](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/tool/bash.txt)
- [Upstream truncation.ts](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/tool/truncation.ts)
