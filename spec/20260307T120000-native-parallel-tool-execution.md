# Native Parallel Tool Execution

**Date**: 2026-03-07
**Status**: In Progress
**Author**: AI-assisted

## Overview

Replace the sequential tool execution loop in `internal/llm/agent/agent.go` with a concurrent execution model that runs independent tool calls in parallel using goroutines and `sync.WaitGroup`. This is the same approach Anthropic adopted in Claude Code (which deprecated its earlier BatchTool in favor of native parallelism) and is model-agnostic — any provider that emits multiple `tool_use` blocks benefits automatically.

## Motivation

### Current State

The tool execution loop in `streamAndHandleEvents` (`agent.go:540-656`) processes tool calls **sequentially**:

```go
toolCalls := assistantMsg.ToolCalls()
for i, toolCall := range toolCalls {
    select {
    case <-ctx.Done():
        // cancel remaining
    default:
        // find tool, loop detection, then:
        toolResult, toolErr := tool.Run(ctx, tools.ToolCall{...})  // line 596 — BLOCKING
        // handle result
    }
}
```

When the model returns 5 independent `read` calls, they execute one after another. A `TODO` comment at line 595 explicitly acknowledges this: `// TODO: add parallelism so tool calls can run concurrently (at least for Task tool)`.

The `TaskTool` (`agent-tool.go:155-159`) spawns a goroutine via `a.Run()` but immediately blocks on the result channel:

```go
done, err := a.Run(ctx, taskSession.ID, params.Prompt)  // spawns goroutine
result := <-done                                          // blocks here
```

Since the outer loop is sequential, even multiple `task` calls execute one at a time.

### Problems

1. **Latency**: 5 parallel `read` calls take 5× longer than necessary. `grep`/`glob`/`websearch` calls that could overlap are serialized.
2. **Subagent bottleneck**: The hivemind agent frequently spawns 2-4 explorer/workhorse subagents per turn. Sequential execution means a 30-second task per subagent becomes 2 minutes wall time.
3. **UX perception**: The TUI shows all tool calls immediately (streamed via `EventToolUseStart`), but only one progresses at a time — the rest show "Waiting for response..." indefinitely until their turn comes.

### Desired State

- Read-only tools (`read`, `glob`, `grep`, `ls`, `view_image`, `webfetch`, `websearch`, `sourcegraph`, `skill`) run concurrently when multiple are requested in the same turn.
- `task` tool calls run concurrently (each already has internal goroutine — just stop blocking on the channel synchronously in the sequential loop).
- File-mutating tools (`edit`, `write`, `multiedit`, `delete`, `patch`) run concurrently only when targeting different files; same-file mutations serialize.
- `bash` commands from `safeReadOnlyCommands` (`git status`, `git diff`, `go test`, `go build`, `ls`, `pwd`, etc.) run concurrently; other bash commands run sequentially.
- `struct_output` runs sequentially (terminal action, always last).
- TUI reflects concurrent execution — multiple tools show active spinners simultaneously.

## Research Findings

### How Claude Code Handles This

**April 2025 (Sonnet 3.7 era)**: Claude Code shipped with a `BatchTool` — a meta-tool wrapping multiple tool invocations. Reverse-engineered by [Kir Shatrov](https://kirshatrov.com/posts/claude-code-internals):

> "BatchTool — Batch execution tool that runs multiple tool invocations in a single request. Tools are executed in parallel when possible, and otherwise serially."

**October 2025 (Sonnet 4 era)**: BatchTool was **removed**. Reverse-engineered by [Weaxs](https://weaxsey.org/en/articles/2025-10-12/) — the tool is absent from the full tool enumeration. Instead, the system prompt says:

> "You have the capability to call multiple tools in a single response. When multiple independent pieces of information are requested, batch your tool calls together for optimal performance."

Claude Code now relies on **native parallel tool execution** at the runtime level, trusting the model to emit multiple `tool_use` blocks.

### Anthropic's Official Guidance

From Anthropic's [tool use docs](https://platform.claude.com/docs/en/agents-and-tools/tool-use/implement-tool-use):

- Claude 4 models (Opus 4.x, Sonnet 4.x) have built-in parallel tool use with high success rates
- A "batch tool" is recommended only as a **workaround for Claude Sonnet 3.7**
- System prompt addition boosts parallel tool use to ~100%: `"For maximum efficiency, whenever you need to perform multiple independent operations, invoke all relevant tools simultaneously rather than sequentially."`

### Cross-Provider Compatibility

Native parallelism is model-agnostic: any provider that returns multiple `tool_use` blocks in a single response benefits. OpenAI and Gemini models also support parallel function calling natively. A BatchTool approach would only work with models fine-tuned to understand the meta-tool schema.

### TypeScript Fork Reference

The `anomalyco/opencode` TypeScript fork implemented a `BatchTool`. While functional, this approach:
- Creates opaque aggregated results in message history (1 tool_use/result pair instead of N)
- Requires nested TUI rendering inside the batch container
- Needs special `struct_output` extraction from batch results
- Only works with models that understand the batch tool schema

The native approach avoids all of these complications.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Approach | Native parallelism (not BatchTool) | Model-agnostic; follows Anthropic's production direction; no message history changes; no TUI nesting; no struct_output complications |
| Parallelism unit | Per tool-call within `streamAndHandleEvents` | Goroutine per tool call, synchronized with `sync.WaitGroup` |
| Safety classification | `AllowParallelism` method on `BaseTool` interface | Each tool owns its classification logic; no tool-param parsing leaked to agent; handles `delete` using `path`, `patch` using embedded paths, bash using `safeReadOnlyCommands` |
| `bash` execution | Parallel for `safeReadOnlyCommands`, sequential otherwise | Commands like `git status`, `git diff`, `go test`, `go build`, `ls`, `pwd` are read-only with no shell state side effects; unsafe/unknown commands stay sequential |
| `struct_output` execution | Always sequential, always last | Terminal action by definition; existing extraction logic unchanged |
| `task` tool handling | No changes to `agent-tool.go` | Since each `tool.Run()` now executes in its own goroutine, the existing blocking `result := <-done` in TaskTool.Run is fine — it blocks the tool's goroutine, not the main loop |
| Permission denied handling | Cancel all pending parallel calls via shared context | Replaces the current `break` + fill-remaining pattern |
| Loop detection | Run before goroutine dispatch (synchronous pre-check) | `callTracker` is not thread-safe; keep it single-threaded |
| TUI status | Change "Waiting for tool" to "Running N tools" when multiple are active | Reflects concurrent execution |

## Architecture

### `AllowParallelism` Interface Method

Instead of the agent layer classifying tools by name and parsing tool-specific parameters, each tool decides for itself whether it can run in parallel given the full batch context:

```go
type BaseTool interface {
    Info() ToolInfo
    Run(ctx context.Context, params ToolCall) (ToolResponse, error)
    AllowParallelism(call ToolCall, allCalls []ToolCall) bool
}
```

**Contract**: The tool receives its own `call` and the full `allCalls` slice for the current turn. It returns `true` if it can safely run concurrently with the other calls. It parses its own params using its own schema — the agent never parses tool inputs.

**Why not agent-level classification?** Tool param schemas vary in ways the agent shouldn't know about:
- `edit`, `write`, `multiedit` use `file_path` for their target
- `delete` uses `path` (not `file_path`)
- `patch` has no file path param — paths are embedded inside `patch_text`
- `bash` needs to check its command against `safeReadOnlyCommands`
- MCP tools have unknown schemas

Putting classification logic in each tool keeps this knowledge encapsulated.

**Double unmarshal tradeoff**: Each tool unmarshals its JSON params once in `AllowParallelism` and once in `Run`. For tool inputs (typically <1KB JSON), this costs ~microseconds — negligible compared to tool execution times (milliseconds to seconds).

### Per-Tool Classification Logic

```
┌──────────────────┬───────────────────────────────────────────────────────┐
│  Tool            │  AllowParallelism logic                               │
├──────────────────┼───────────────────────────────────────────────────────┤
│  read, glob,     │  return true (always safe, read-only)                │
│  grep, ls,       │                                                      │
│  view_image,     │                                                      │
│  webfetch,       │                                                      │
│  websearch,      │                                                      │
│  sourcegraph,    │                                                      │
│  skill, task     │                                                      │
├──────────────────┼───────────────────────────────────────────────────────┤
│  edit            │  Parse own file_path; scan allCalls for other        │
│                  │  mutating tools targeting same path → false if        │
│                  │  conflict, true otherwise                            │
├──────────────────┼───────────────────────────────────────────────────────┤
│  write           │  Same as edit (uses file_path)                       │
├──────────────────┼───────────────────────────────────────────────────────┤
│  multiedit       │  Same as edit (uses file_path)                       │
├──────────────────┼───────────────────────────────────────────────────────┤
│  delete          │  Parse own path; scan allCalls for other mutating    │
│                  │  tools targeting same path → false if conflict       │
├──────────────────┼───────────────────────────────────────────────────────┤
│  patch           │  Parse own patch_text to extract affected file       │
│                  │  paths; scan allCalls for conflicts → false if any   │
├──────────────────┼───────────────────────────────────────────────────────┤
│  bash            │  Parse own command; check against                    │
│                  │  safeReadOnlyCommands list → true if safe,           │
│                  │  false otherwise                                     │
├──────────────────┼───────────────────────────────────────────────────────┤
│  struct_output   │  return false (always sequential, terminal action)   │
├──────────────────┼───────────────────────────────────────────────────────┤
│  MCP tools       │  Default: return true (assume read-only)             │
│                  │  MCP tools implement BaseTool, can override          │
└──────────────────┴───────────────────────────────────────────────────────┘
```

### Cross-Tool File Conflict Detection

File-mutating tools need to detect conflicts with OTHER mutating tools in the same batch. To avoid each tool knowing every other tool's param schema, provide a shared utility in `tools.go`:

```go
func ExtractPathsFromCall(call ToolCall) []string {
    var common struct {
        FilePath  string `json:"file_path"`
        Path      string `json:"path"`
    }
    json.Unmarshal([]byte(call.Input), &common)
    var paths []string
    if common.FilePath != "" {
        paths = append(paths, common.FilePath)
    }
    if common.Path != "" {
        paths = append(paths, common.Path)
    }
    return paths
}
```

This covers `edit` (`file_path`), `write` (`file_path`), `multiedit` (`file_path`), and `delete` (`path`). For `patch`, paths are extracted from `patch_text` using `diff.IdentifyFilesNeeded` — `patch` handles this itself.

The convention is minimal: "try common JSON keys for file paths." It's not deep coupling — it's a best-effort heuristic. If a tool can't determine conflicts, it returns `false` (conservative).

Example implementation for `edit`:

```go
func (e *editTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool {
    var params EditParams
    if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
        return false
    }
    myPath := params.FilePath
    for _, other := range allCalls {
        if other.ID == call.ID { continue }
        if !IsMutatingTool(other.Name) { continue }
        for _, p := range ExtractPathsFromCall(other) {
            if p == myPath { return false }
        }
    }
    return true
}
```

Example implementation for `bash`:

```go
func (b *bashTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool {
    var params BashParams
    if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
        return false
    }
    return isSafeReadOnlyCommand(params.Command)
}
```

### Execution Flow

```
streamAndHandleEvents():
  1. Stream completes → toolCalls = assistantMsg.ToolCalls()

  2. Pre-processing (synchronous, single-threaded):
     for each toolCall:
       a. Look up tool by name (error result if not found)
       b. Run loop detection — tracker.Track() (error result if loop)
       c. Call tool.AllowParallelism(call, allCalls)

  3. Build execution groups:
     Group A (parallel):  tools where AllowParallelism returned true
     Group B (sequential): tools where AllowParallelism returned false

  4. Execute Group A concurrently:
     permCtx, permCancel := context.WithCancel(ctx)
     var wg sync.WaitGroup
     for each tool in Group A:
       wg.Add(1)
       go func(i int, tool, toolCall) {
         defer wg.Done()
         result, err := tool.Run(permCtx, toolCall)
         // store in toolResults[i] (pre-allocated, no race — each goroutine owns its index)
         // on permission denied: permCancel() to signal other goroutines
       }(i, tool, toolCall)
     wg.Wait()
     
  5. Check for permission denied in Group A results
     → if any: cancel remaining Group B, fill with "canceled" results

  6. Execute Group B sequentially:
     for each tool in Group B:
       select { case <-ctx.Done(): cancel remaining }
       result, err := tool.Run(ctx, toolCall)
       // same handling as current code

  7. Persist all results as single Tool message (unchanged)
```

### Cancellation Semantics

```
                    ctx (parent — user cancellation)
                     │
              permCtx (derived — permission denied propagation)
               │   │   │
            ┌──┘   │   └──┐
            ▼      ▼      ▼
         tool_1  tool_2  tool_3   (Group A — parallel)
         
When tool_2 returns permission.ErrorPermissionDenied:
  1. permCancel() is called
  2. tool_1 and tool_3 see permCtx.Done() on their next ctx check
  3. After wg.Wait(), Group B is skipped entirely
  4. All incomplete results filled with "canceled" message
```

### TUI Changes

**Status indicator** (`list.go:332-356`):

Current `working()` shows `"Waiting for tool"` when any finished tool call has no result. With parallel execution, multiple tools run simultaneously. Change to:

```go
func (m *messagesCmp) working() string {
    // ...
    if pendingCount := countToolsWithoutResponse(m.messages); pendingCount > 1 {
        task = fmt.Sprintf("Running %d tools", pendingCount)
    } else if pendingCount == 1 {
        task = "Waiting for tool"
    }
    // ...
}
```

**Tool message rendering** (`message.go:700-720`):

No structural changes needed. The existing rendering already handles multiple tools with `Finished=true` and `response == nil` ("Waiting for response..."). With parallel execution, multiple tools will transition from "Waiting for response..." to showing results near-simultaneously. The pubsub event flow (`messages.Create` for the Tool-role message) triggers a single re-render that updates all tool results at once, since all results are persisted in one `message.Create` call (line 665).

The visual effect: instead of tools resolving one-by-one top-to-bottom, they all resolve together — a noticeable UX improvement with zero rendering code changes.

### System Prompt Enhancement

Add to agent system prompts (coder, hivemind, workhorse) to encourage parallel tool use:

```
You have the capability to call multiple tools in a single response. When 
multiple independent pieces of information are requested, batch your tool 
calls together for optimal performance. For example, when reading 3 files, 
call read 3 times in parallel rather than sequentially.
```

This matches Anthropic's recommended prompting from their docs.

## Implementation Plan

### Phase 1: `AllowParallelism` Interface & Implementations

- [x] **1.1** Extend `BaseTool` interface in `internal/llm/tools/tools.go` with `AllowParallelism(call ToolCall, allCalls []ToolCall) bool`. Add shared helpers: `IsMutatingTool(name string) bool` (returns true for edit/write/multiedit/delete/patch) and `ExtractPathsFromCall(call ToolCall) []string` (tries `file_path` and `path` JSON keys).

- [x] **1.2** Implement `AllowParallelism` for all read-only tools (`read`, `glob`, `grep`, `ls`, `view_image`, `webfetch`, `websearch`, `sourcegraph`, `skill`): always return `true`.

- [x] **1.3** Implement `AllowParallelism` for `task` tool in `agent-tool.go`: always return `true`.

- [x] **1.4** Implement `AllowParallelism` for file-mutating tools (`edit`, `write`, `multiedit`, `delete`, `patch`): parse own params, extract own target path(s), scan `allCalls` for other mutating tools with overlapping paths using `ExtractPathsFromCall`. For `patch`: use `diff.IdentifyFilesNeeded` + `diff.IdentifyFilesAdded` to extract affected paths from `patch_text`.

- [x] **1.5** Implement `AllowParallelism` for `bash`: parse `BashParams.Command`, check against `safeReadOnlyCommands` using existing `isSafeReadOnlyCommand` logic (prefix match with boundary check). Return `true` for safe commands, `false` otherwise.

- [x] **1.6** Implement `AllowParallelism` for `struct_output`: always return `false`.

- [x] **1.7** Implement `AllowParallelism` for MCP tools (in `mcp-tool.go`): default return `true`.

- [x] **1.8** Add `buildExecutionGroups(toolCalls []message.ToolCall, toolResults []message.ToolResult, toolSet []tools.BaseTool, tracker *callTracker) (parallel []int, sequential []int)` in `agent.go` that: looks up tools, runs loop detection, calls `AllowParallelism`, and partitions indices into two groups.

### Phase 2: Parallel Execution Engine

- [x] **2.1** Refactor the tool execution section of `streamAndHandleEvents` (`agent.go:540-656`). Replace the sequential `for i, toolCall := range toolCalls` loop with:
  1. Call `buildExecutionGroups` to partition
  2. Execute parallel group with `sync.WaitGroup` + goroutines
  3. Execute sequential group with existing logic
  4. Preserve the `goto out` / permission-denied / cancellation semantics

- [x] **2.2** Implement permission-denied propagation: create a derived context (`permCtx`) for the parallel group. Any goroutine that gets `permission.ErrorPermissionDenied` calls `permCancel()`. After `wg.Wait()`, scan parallel results for permission denied — if found, skip sequential group and fill remaining with "canceled".

- [x] **2.3** Ensure `toolResults[i]` writes are race-free. Each goroutine writes to its own pre-allocated index — no mutex needed since indices don't overlap. The `toolResults` slice is pre-allocated at line 541: `toolResults := make([]message.ToolResult, len(toolCalls))`.

### Phase 3: TUI Updates

- [x] **3.1** Add `countToolsWithoutResponse` helper to `list.go` (variant of `hasToolsWithoutResponse` that returns count instead of bool).

- [x] **3.2** Update `working()` in `list.go` to show `"Running N tools"` when multiple tools are pending.

### Phase 4: System Prompt

- [x] **4.1** Add parallel tool use encouragement to `internal/llm/prompt/coder.go`, `hivemind.go`, and `workhorse.go` and `explorer.go` system prompts.

### Phase 5: Tests

- [x] **5.1** Unit test `AllowParallelism` for each tool type: read-only tools always true; struct_output always false; bash true for safe commands (`git status`, `go test`), false for unsafe (`rm`, `curl`); edit/write true when no file conflict, false when same file; delete uses `path` key correctly; patch extracts paths from patch text.

- [x] **5.2** Unit test `ExtractPathsFromCall`: verify extraction from edit (`file_path`), delete (`path`), write (`file_path`), unknown tool (empty). Verify `IsMutatingTool` returns correct classification.

- [x] **5.3** Unit test `buildExecutionGroups`: verify partitioning logic — read-only tools go to parallel, same-file edits go to sequential, different-file edits go to parallel, safe bash goes to parallel, unsafe bash goes to sequential, struct_output always sequential, not-found tools get error results.

- [x] **5.4** Integration test: mock 3 read-only tools that each sleep 100ms. Verify total execution time is ~100ms (parallel) not ~300ms (sequential).

- [x] **5.5** Integration test: mock 2 edit tools targeting the same file. Verify they execute sequentially (total time ~200ms).

- [x] **5.6** Integration test: permission denied in parallel group cancels remaining tools.

- [x] **5.7** Verify existing agent tests pass unchanged (the external behavior — tool results in message history — is identical).

### Phase 6: Observability (optional follow-up)

- [x] **6.1** Add logging: `logging.Info("Executing tools", "parallel", len(parallelGroup), "sequential", len(sequentialGroup), "session_id", sessionID)`

- [x] **6.2** Log per-tool execution time in parallel group for performance monitoring.

## Edge Cases

### Single tool call

1. Model returns 1 tool call
2. `buildExecutionGroups` puts it in the appropriate group (parallel or sequential)
3. If parallel group has 1 item, it still spawns a goroutine — negligible overhead
4. Alternatively, skip goroutine for single-call case (optimization, not required)

### All tools are sequential

1. Model returns `[bash "curl ...", struct_output]` (unsafe bash + struct_output)
2. `bash.AllowParallelism` returns false (not in safeReadOnlyCommands), `struct_output` returns false
3. Parallel group is empty, `wg.Wait()` returns immediately
4. Sequential group executes as before — identical to current behavior

### Bash safe read-only commands in parallel

1. Model returns `[bash "git status", bash "go test ./...", read file.go]`
2. `bash.AllowParallelism` returns true for both (both in `safeReadOnlyCommands`)
3. `read.AllowParallelism` returns true
4. All 3 go to parallel group → execute concurrently
5. Wall time ≈ slowest command (likely `go test`) instead of sum of all three

### Mixed parallel and sequential

1. Model returns `[read, read, bash, grep]`
2. Parallel group: `[read, read, grep]` (indices 0, 1, 3)
3. Sequential group: `[bash]` (index 2)
4. Parallel group runs first → all 3 complete concurrently
5. Sequential group runs after → bash executes
6. Results assembled in original order (by index)

### Same-file edit conflict

1. Model returns `[edit file.go:10, edit file.go:50]`
2. `edit.AllowParallelism` for first call: scans allCalls, finds second edit targeting same `file_path` → returns false
3. `edit.AllowParallelism` for second call: same logic → returns false
4. Both moved to sequential group
4. They execute in order: edit line 10 first, then edit line 50
5. No race condition

### Permission denied during parallel execution

1. Model returns `[read, task, edit]` — all parallelizable (different files)
2. `edit` returns `permission.ErrorPermissionDenied`
3. `permCancel()` called → `read` and `task` goroutines see context cancelled
4. After `wg.Wait()`: edit result is "Permission denied", read/task results may be complete (ran before cancel) or cancelled
5. Sequential group skipped, message finished with `FinishReasonPermissionDenied`

### Context cancelled by user mid-parallel

1. 3 tools running in parallel
2. User presses Ctrl+C → parent `ctx` cancelled
3. All goroutines see `ctx.Done()` (permCtx inherits from ctx)
4. `wg.Wait()` completes, results filled with "canceled" where incomplete
5. `goto out` path persists results

### Loop detection with parallel tools

1. `callTracker.Track()` runs synchronously in the pre-processing step (Phase 1.3)
2. Loop-detected tools get error results immediately, never reach execution groups
3. No thread-safety concern — tracker is only accessed from the main goroutine

### struct_output in mixed batch

1. Model returns `[read, grep, struct_output]`
2. `struct_output` classified as sequential
3. `read` and `grep` run in parallel first
4. `struct_output` runs after in sequential group
5. Existing `structOutput` extraction in `processGeneration` (line 487-490) works unchanged — it scans `toolResults` by tool name regardless of execution order

### TaskTool in parallel group

1. Model returns `[task "explore A", task "explore B", read file.go]`
2. All classified as parallel (task is always-parallel)
3. 3 goroutines spawned
4. Each TaskTool.Run() internally does `done := a.Run(...)` then `result := <-done` — this blocks **its own goroutine**, not the others
5. All 3 complete independently, concurrently
6. No changes needed to `agent-tool.go` — the existing blocking channel receive is correct because it now blocks a dedicated goroutine instead of the main sequential loop

### Multiedit with multiple files

1. Model returns `[multiedit {file_path: "a.go", edits: [...]}, edit {file_path: "a.go"}]`
2. `multiedit.AllowParallelism`: parses own `file_path` ("a.go"), scans allCalls, finds edit targeting same path → returns false
3. `edit.AllowParallelism`: parses own `file_path` ("a.go"), scans allCalls, finds multiedit targeting same path → returns false
4. Both moved to sequential group
5. Conservative but safe — avoids partial file conflicts

### Delete uses `path` not `file_path`

1. Model returns `[edit {file_path: "a.go"}, delete {path: "a.go"}]`
2. `edit.AllowParallelism`: scans allCalls, calls `ExtractPathsFromCall` on delete call → extracts "a.go" from `path` key → conflict detected → returns false
3. `delete.AllowParallelism`: parses own `path` ("a.go"), calls `ExtractPathsFromCall` on edit call → extracts "a.go" from `file_path` key → conflict detected → returns false
4. Both serialized correctly despite different param key names
5. This is exactly why `AllowParallelism` + `ExtractPathsFromCall` is better than a hardcoded `extractFilePath` that only checks `file_path`

### Patch with embedded file paths

1. Model returns `[patch {patch_text: "*** Update File: a.go\n..."}, edit {file_path: "a.go"}]`
2. `patch.AllowParallelism`: uses `diff.IdentifyFilesNeeded` to extract ["a.go"] from patch_text, scans allCalls, finds edit targeting same path → returns false
3. Both serialized — no race condition on a.go

## Open Questions

1. **Should parallel group always execute before sequential group?**
   - Current design: yes, parallel first, then sequential
   - Alternative: interleave based on original order
   - **Recommendation**: Parallel-first is simpler and matches the common pattern (reads before writes). The model already orders tool calls with dependencies in mind.

2. **Should we add a config flag to disable parallel execution?**
   - Could be useful for debugging or for models that produce conflicting parallel calls
   - **Recommendation**: Defer. If needed, a simple `parallelTools: false` in `.opencode.json` could bypass the grouping logic and fall back to sequential. Not worth adding before we see real problems.

3. ~~**Should we parallelize within the sequential group using a semaphore?**~~
   - Resolved: bash commands in `safeReadOnlyCommands` now go to the parallel group via `AllowParallelism`. Unsafe bash commands remain sequential. No semaphore needed.

4. **How to handle MCP tools?**
   - MCP tools are external and may have unknown side effects
   - **Recommendation**: MCP tools implement `BaseTool`, so they get `AllowParallelism`. Default implementation returns `true` (assume read-only). Users who add write-heavy MCP tools would need to handle conflicts at the MCP server level. This can be revisited if problems emerge — a future MCP protocol extension could declare tool side effects.

5. **Should we cap the number of parallel goroutines?**
   - Models typically emit 3-8 parallel tool calls, rarely more
   - **Recommendation**: No cap initially. If models start emitting 20+ calls, add a semaphore (e.g., `maxParallel = 10`). Current tool call volumes don't warrant it.

## Success Criteria

- [ ] Multiple read-only tool calls in a single turn execute concurrently (wall time ≈ slowest tool, not sum of all)
- [ ] Multiple `task` tool calls execute concurrently (subagents run in parallel)
- [ ] Safe bash commands (`git status`, `go test`, etc.) execute in parallel; unsafe bash commands execute sequentially
- [ ] `struct_output` still executes sequentially
- [ ] Same-file mutations detected and serialized (no race conditions)
- [ ] Permission denied in any parallel tool cancels remaining parallel tools
- [ ] User cancellation (Ctrl+C) cleanly cancels all parallel goroutines
- [ ] Loop detection works correctly (pre-check before parallel dispatch)
- [ ] `struct_output` extraction in `processGeneration` works unchanged
- [ ] TUI shows concurrent execution status ("Running N tools")
- [ ] Message history format unchanged (same `tool_use`/`tool_result` pairs)
- [ ] All existing agent tests pass
- [ ] System prompts encourage parallel tool use across providers

## References

- `internal/llm/agent/agent.go` — `streamAndHandleEvents` tool loop (lines 540-674), `processGeneration` struct_output handling (lines 467-503), `Run()` goroutine spawn (lines 229-272), `processEvent` streaming (lines 732-785)
- `internal/llm/agent/agent-tool.go` — `TaskTool.Run()` blocking channel (lines 155-159)
- `internal/llm/agent/tools.go` — `NewToolSet` tool registration (lines 22-204)
- `internal/llm/agent/loop_detection.go` — `callTracker` struct
- `internal/llm/tools/tools.go` — `BaseTool` interface, `ToolCall`, `ToolResponse` types (lines 10-114)
- `internal/llm/tools/bash.go` — `safeReadOnlyCommands` list (lines 51-59), `BashParams` struct (lines 17-28)
- `internal/llm/tools/edit.go` — `EditParams` struct with `file_path` (lines 21-31)
- `internal/llm/tools/delete.go` — `DeleteParams` struct with `path` (not `file_path`) (lines 19-26)
- `internal/llm/tools/patch.go` — `PatchParams` with embedded paths in `patch_text` (lines 21-23)
- `internal/llm/tools/struct_output.go` — `StructOutputToolName`, terminal action semantics
- `internal/tui/components/chat/message.go` — `renderToolMessage` (lines 665-799)
- `internal/tui/components/chat/list.go` — `working()` status, `hasToolsWithoutResponse` (lines 296-356)
- `internal/message/content.go` — `ToolCall.Finished`, `ToolResult`, `StructOutput()` (lines 85-209)
- Anthropic tool use docs: https://platform.claude.com/docs/en/agents-and-tools/tool-use/implement-tool-use
- Claude Code reverse engineering (BatchTool era): https://kirshatrov.com/posts/claude-code-internals
- Claude Code reverse engineering (post-BatchTool): https://weaxsey.org/en/articles/2025-10-12/
