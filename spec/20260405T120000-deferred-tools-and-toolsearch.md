# Deferred Tools and ToolSearchTool

**Date**: 2026-04-05
**Status**: Draft
**Author**: AI-assisted

## Overview

Introduce a deferred tool loading mechanism and a new `toolsearch` tool that allows agents to discover and activate tools on-demand. Tools marked as deferred are excluded from the initial API request (saving context window budget) but remain listed by name in a `<system-reminder>` block inside the system prompt. When the model needs a deferred tool, it calls `toolsearch` to load its full schema and description.

The activation path is **provider-specific**: Anthropic's API natively supports `defer_loading` and `tool_reference` content blocks (the Go SDK v1.30.0 already has these types), enabling **single-turn activation** where the model can call a deferred tool in the same response that discovers it. For all other providers (OpenAI, Gemini), a **two-turn fallback** is used where the tool becomes available on the next API call after discovery.

## Motivation

### Current State

Every tool assigned to an agent has its full schema (name, description, parameters) sent to the provider on every API call. In `convertTools` (`provider/anthropic.go:187–211`):

```go
func (a *anthropicClient) convertTools(tools []toolsPkg.BaseTool) []anthropic.ToolUnionParam {
    anthropicTools := make([]anthropic.ToolUnionParam, len(tools))
    for i, tool := range tools {
        info := tool.Info()
        toolParam := anthropic.ToolParam{
            Name:        info.Name,
            Description: anthropic.String(info.Description),
            InputSchema: anthropic.ToolInputSchemaParam{
                Properties: info.Parameters,
                Required:   info.Required,
            },
        }
        // ...
    }
    return anthropicTools
}
```

All tools — including ones the model may never use in a given session — consume context window tokens on every turn.

This creates problems:

1. **Context budget waste**: Tools like `websearch`, `sourcegraph`, `webfetch`, `lsp`, and MCP tools have large descriptions and parameter schemas. Sending all of them on every turn wastes context that could be used for conversation history and tool results.
2. **Scaling limit**: As MCP tool counts grow (users connecting Slack, GitHub, Jira, etc.), the tool payload grows proportionally. A user with 30 MCP tools can lose several thousand tokens per turn to tool schemas they never invoke.
3. **No opt-out mechanism**: The current `Tools` config can only enable/disable tools entirely. There is no middle ground where a tool is available but loaded on-demand.

### Desired State

Agents can mark specific tools as "deferred" via configuration. Deferred tools:
- Are either excluded from the API payload (OpenAI/Gemini) or included with `defer_loading: true` (Anthropic) to avoid expanding in the model's system prompt
- ARE listed by name in a `<system-reminder>` in the system prompt for built-in tools (cache-stable), and via `deferred_tools_delta` user messages for dynamically discovered MCP tools
- Can be discovered and activated via the `toolsearch` tool
- Once activated, behave identically to regular tools on subsequent turns
- Remain in the agent's full toolset for execution at all times

## Research Findings

### Claude Code's Deferred Tool Implementation

Claude Code implements a mature deferred tool system. Key components:

**Tool interface (`src/Tool.ts`):**
```ts
type Tool = {
  shouldDefer?: boolean   // marks tool as deferred
  alwaysLoad?: boolean    // opt-out of deferral (e.g., MCP tools)
  searchHint?: string     // keyword hint for ToolSearch matching
}
```

**Classification logic (`src/tools/ToolSearchTool/prompt.ts`):**
`isDeferredTool()` checks in priority order: `alwaysLoad` → MCP default → ToolSearch itself → `shouldDefer` flag. Built-in tools with `shouldDefer: true` include: `WebSearchTool`, `WebFetchTool`, `LSPTool`, `ConfigTool`, `NotebookEditTool`, all Task/Cron tools, and several others.

**Two announcement modes:**
1. **Legacy** (`<available-deferred-tools>`): Prepended as a user message on every API call. Busts prompt cache when the pool changes.
2. **Delta** (`<system-reminder>`): Persisted attachment injected only when the pool changes. Cache-stable. Used for internal users and behind a feature flag.

**API integration (`src/services/api/claude.ts`):**
- `defer_loading: true` is set on deferred tool schemas (Anthropic API beta feature)
- `tool_reference` blocks in tool results tell the API to inject full schemas inline
- `extractDiscoveredToolNames()` scans message history for `tool_reference` blocks to track which deferred tools have been loaded
- The `advanced-tool-use` beta header enables the feature

**ToolSearchTool behavior:**
- Accepts a query string (keyword search or `select:ToolA,ToolB` for direct selection)
- Returns `tool_reference` content blocks that the API server expands into full schemas
- Supports scoring: exact name match (+10/12), partial match (+5/6), `searchHint` match (+4), description match (+2)
- Required terms via `+prefix` syntax

### Anthropic SDK v1.30.0 Already Has the Primitives

The Anthropic Go SDK (`github.com/anthropics/anthropic-sdk-go@v1.30.0`), already in `go.mod`, has full support for deferred tool loading:

**`DeferLoading` on `ToolParam`** (`message.go:1036`):
```go
type ToolParam struct {
    // ...
    DeferLoading  param.Opt[bool]  `json:"defer_loading,omitzero"`
    // ...
}
```

SDK docs: *"If true, tool will not be included in initial system prompt. Only loaded when returned via `tool_reference` from tool search."*

**Beta header requirement**: Claude Code sends `advanced-tool-use` (1P) or `tool-search-tool` (Vertex/Bedrock) as an `anthropic-beta` header. OpenCode already supports custom provider headers via `.opencode.json` `providers.anthropic.headers` — users can set `"anthropic-beta": "advanced-tool-use"` there. This avoids hardcoding a beta header that may become obsolete and cause API failures.

**`ToolReferenceBlock` and `ToolReferenceBlockParam`** — content block types for tool results:
```go
type ToolReferenceBlock struct {
    ToolName string                 `json:"tool_name"`
    Type     constant.ToolReference `json:"type" default:"tool_reference"`
}
```

**`ToolSearchToolSearchResultBlock`** (`message.go:1686`) — wraps tool references:
```go
ToolReferences []ToolReferenceBlock `json:"tool_references"`
```

**None of these are currently used** in OpenCode's `convertTools` or message conversion code.

### Dual-Path Strategy: Native vs. Fallback

Since OpenCode is multi-provider, we implement **two activation paths**:

| Path | Providers | Mechanism | Latency |
|------|-----------|-----------|----------|
| **Native** | Anthropic (direct, Bedrock, VertexAI) | `defer_loading: true` on `ToolParam` + `tool_reference` blocks in ToolSearch result | **Single-turn**: model can call deferred tool in the same response |
| **Fallback** | OpenAI, Gemini | Skip deferred tools in `convertTools` + formatted text in ToolSearch result + wrapper activation | **Two-turn**: tool available on next API call |

The native path matches exactly how Claude Code works. The API server expands `tool_reference` blocks into full tool schemas inline, so the model sees the complete definition and can invoke the tool immediately.

The fallback path returns tool info as formatted text. The model reads it, and on the next turn `convertTools` includes the now-activated tool's schema. The model can then call it.

**Key insight**: The `deferredWrapper` and `Activate()` mechanism is needed for **both paths**. For Anthropic, activation tracks which deferred tools should have their schemas included in subsequent API calls (otherwise we'd need to re-scan message history for `tool_reference` blocks like Claude Code does). For other providers, activation is the primary gate in `convertTools`.

### `<system-reminder>` Tag Convention

Claude Code's system prompt explains the tag:

> "Tool results and user messages may include `<system-reminder>` tags. `<system-reminder>` tags contain useful information and reminders. They are automatically added by the system, and bear no direct relation to the specific tool results or user messages in which they appear."

This framing ensures the model treats `<system-reminder>` content as authoritative system-injected information, distinct from user or tool output. OpenCode should adopt the same convention.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Config field | `DeferredTools map[string]bool` on `Agent` | Same format as existing `Tools` field; reuses `IsToolEnabled`/`MatchWildcard` logic; familiar to users |
| Interface change | Add `IsDeferred() bool` to `BaseTool` | Providers check this in `convertTools` to skip deferred tools; clean separation — provider doesn't need to know about deferred config |
| Wrapper pattern | `deferredWrapper` struct with mutable `active` flag | Avoids modifying every tool implementation; wrapper delegates all methods except `IsDeferred()`; `Activate()` flips the flag for subsequent API calls |
| Where filtering happens | In each provider's `convertTools` | The provider owns the API schema format; it naturally decides what to include. Consistent with existing cache-breakpoint logic in `convertTools` |
| System prompt announcement | `<system-reminder>` block in `GetAgentPrompt` | Part of the cached system prompt; stable across turns; computed from config without waiting for tool resolution |
| ToolSearchTool result format | Provider-specific: `tool_reference` blocks for Anthropic; formatted text in `<system-reminder>` tags for others | Anthropic gets single-turn activation (API expands `tool_reference`); others get full tool info as text for next-turn use |
| Activation latency | Single-turn for Anthropic; two-turn for OpenAI/Gemini | Anthropic's `tool_reference` allows same-turn invocation; other providers reject `tool_use` for undeclared tools, so schema must appear in next API call |
| Provider awareness in ToolSearchTool | ToolSearchTool receives `isNativeProvider bool` and agent `Service` interface | `isNativeProvider` set at creation from `model.Provider`; agent set via `SetAgent()` after struct assembly; tool calls `agent.ResolvedTools()` in `Run()` |
| ToolSearchTool auto-inclusion | Only included if agent has at least one deferred tool | No point adding the meta-tool when nothing is deferred |
| ToolSearchTool never deferred | Hardcoded to `IsDeferred() = false` | The discovery tool must always be fully available |
| `struct_output` never deferred | Enforced in `IsToolDeferred` | Terminal action tool; deferring it would break structured output flows |
| Discovery tracking | ToolSearchTool mutates wrapper's `active` flag | No need to scan message history. Simpler than Claude Code's `extractDiscoveredToolNames`. State is per-agent-instance, reset naturally on new sessions |
| `defer_loading` for Anthropic | Set on `ToolParam` in `convertTools` when tool is deferred; always keep flag set regardless of activation | SDK already has `DeferLoading param.Opt[bool]` on `ToolParam`; keeping it set after activation preserves cache stability — the API already has the full schema and ignores the flag for non-deferred tools |
| Beta header for `defer_loading` | User-configurable via `.opencode.json` provider headers, not hardcoded | `"anthropic-beta": "advanced-tool-use"` must be set in `providers.anthropic.headers` for `defer_loading` and `tool_reference` to work; avoids hardcoding headers that may become obsolete |
| MCP tool announcement | `deferred_tools_delta` user message with `<system-reminder>` tags | Plain user-role text message; works across all providers; injected in `processGeneration` after tool resolution; delta-tracked per agent instance |

## Architecture

### Config Extension

```
┌─────────────────────────────────────────────────┐
│ .opencode.json                                  │
│ {                                               │
│   "agents": {                                   │
│     "coder": {                                  │
│       "tools": {                                │
│         "websearch": true,                      │
│         "sourcegraph": true                     │
│       },                                        │
│       "deferredTools": {                        │
│         "websearch": true,                      │
│         "sourcegraph": true,                    │
│         "mcp_*": true                           │
│       }                                         │
│     }                                           │
│   }                                             │
│ }                                               │
└─────────────────────────────────────────────────┘
```

`deferredTools` uses the same wildcard matching as `tools`. A tool is deferred only if:
1. It is enabled (via `tools` / `IsToolEnabled`)
2. It matches a pattern in `deferredTools` with value `true`
3. It is not `toolsearch` or `struct_output` (hardcoded exclusions)

### Wrapper Architecture

```
┌──────────────────────────────────────────────────┐
│ BaseTool interface                               │
│ ├── Info() ToolInfo                              │
│ ├── Run(ctx, call) (ToolResponse, error)         │
│ ├── AllowParallelism(call, allCalls) bool         │
│ ├── IsBaseline() bool                            │
│ └── IsDeferred() bool                    ← NEW   │
└──────────────────────────────────────────────────┘
         ▲                          ▲
         │                          │
┌────────┴─────────┐    ┌──────────┴───────────────┐
│ Concrete tools   │    │ deferredWrapper           │
│ (edit, read, …)  │    │ ├── inner: BaseTool       │
│                  │    │ ├── active: atomic.Bool    │
│ IsDeferred()     │    │ ├── IsDeferred() bool      │
│   → return false │    │ │   → return !active       │
│                  │    │ └── Activate()             │
└──────────────────┘    │     → active.Store(true)   │
                        └──────────────────────────────┘
```

All existing tool implementations add `IsDeferred() bool { return false }`. In `NewToolSet`, tools matching the deferred config are wrapped in `deferredWrapper`. The wrapper delegates all `BaseTool` methods to the inner tool except `IsDeferred()`.

### System Prompt Injection

In `GetAgentPrompt` (`prompt.go`), after building the base prompt:

```
STEP 1: Get AgentInfo from registry
────────────────────────────────────
info.Tools       → which tools are enabled
info.DeferredTools → which enabled tools are deferred

STEP 2: Compute deferred tool names for BUILT-IN tools only
────────────────────────────────────────────────────────────────
Iterate all known built-in tool name constants.
For each: IsToolEnabled(agentID, name) AND IsToolDeferred(name, info.DeferredTools)
  → collect into deferredNames list
Note: MCP tools are NOT included here — they arrive
asynchronously. See deferred_tools_delta below.

STEP 3: Append <system-reminder> to system prompt
──────────────────────────────────────────────────
If len(deferredNames) > 0 OR agent has deferredTools wildcard
patterns that could match MCP tools:
  - Add <system-reminder> explanation to system prompt (once)
  - Add deferred tools block listing known deferred tool names
  - ToolSearchTool is auto-included (not in deferred list itself)
```

The `<system-reminder>` appears as part of the cached system prompt:

```
<system-reminder>
Tool results and user messages may include <system-reminder> tags.
These tags contain useful information and reminders automatically
added by the system.
</system-reminder>

<system-reminder>
The following tools are available for deferred loading via the
toolsearch tool. To use any of them, first call toolsearch with the
tool name to load its full definition:
- websearch
- sourcegraph
- mcp_slack_send_message
- mcp_slack_list_channels
</system-reminder>
```

This block is computed from config alone (no tool resolution needed), so it's available at prompt build time and is cache-stable.

### Deferred Tools Delta (MCP Tools)

MCP tools arrive asynchronously after `GetAgentPrompt` has already built the system prompt. They cannot be included in the static `<system-reminder>`. To announce newly arriving MCP deferred tools, inject a `deferred_tools_delta` user message into the conversation.

Claude Code uses the same approach: `deferred_tools_delta` is normalized into a plain user-role message with `<system-reminder>` tags. It is NOT an Anthropic-specific API feature — it is just text:

```json
{
  "role": "user",
  "content": "<system-reminder>\nThe following deferred tools are now available via toolsearch:\nmcp_slack_send_message\nmcp_slack_list_channels\n</system-reminder>"
}
```

This works identically across all providers since it's a standard user message. The `<system-reminder>` tags are just text — the system prompt already instructs the model to treat them as authoritative system information.

**Injection timing**: In `processGeneration` (`agent.go`), after `resolveTools()` completes (which blocks until all MCP tools are loaded), compute the delta between the deferred MCP tool names and those already announced in the system prompt. If any new deferred MCP tools exist, prepend a `deferred_tools_delta` user message to the conversation history before the first API call. On subsequent turns (after tool execution), re-check for changes (MCP servers can connect/disconnect mid-session).

**Delta tracking**: Maintain a `Set<string>` of announced deferred tool names on the agent instance. On each turn, compare current deferred tool names against the announced set. Inject a delta message only when the set changes.

### ToolSearchTool Flow — Anthropic (Native Path)

```
Model turn N:
  Model sees <system-reminder> listing "websearch" as deferred
  convertTools sends websearch with defer_loading: true (name only, no schema expansion)
  Model needs web search → calls toolsearch(query: "websearch")
    │
    ▼
ToolSearchTool.Run():
  1. Iterate allTools, find those where IsDeferred() == true
  2. Match query against tool names (exact, prefix, keyword)
  3. For each match:
     a. Call wrapper.Activate() → IsDeferred() now returns false
     b. Collect tool name
  4. Return ToolResponse with tool_reference metadata:
     Content: "Loaded tools: websearch"
     Metadata: {"tool_references": ["websearch"]}
    │
    ▼
Anthropic convertMessages detects tool_references metadata:
  Constructs ToolResultBlockParam with tool_reference content blocks:
    {type: "tool_result", tool_use_id: "...", content: [
      {type: "tool_reference", tool_name: "websearch"}
    ]}
  API server expands tool_reference → model sees full websearch schema
    │
    ▼
Model (same turn N, same response):
  Can immediately call websearch(query: "...", provider: "brave")
  → executes normally

Model turn N+1:
  convertTools includes websearch normally (IsDeferred() == false)
  No defer_loading flag needed anymore
```

### ToolSearchTool Flow — Other Providers (Fallback Path)

```
Model turn N:
  Model sees <system-reminder> listing "websearch" as deferred
  convertTools skips websearch entirely (not in API payload)
  Model needs web search → calls toolsearch(query: "websearch")
    │
    ▼
ToolSearchTool.Run():
  1. Iterate allTools, find those where IsDeferred() == true
  2. Match query against tool names (exact, prefix, keyword)
  3. For each match:
     a. Call wrapper.Activate() → IsDeferred() now returns false
     b. Collect Info() (name, description, parameters, required)
  4. Format response:
     <system-reminder>
     The following tools are now loaded and available for use:

     ## websearch
     Search the web for current information...
     Parameters:
       - query (string, required): The search query
       - provider (string, required): Which search provider to use
       - max_results (number, optional): Maximum number of results
     </system-reminder>
  5. Return as NewTextResponse
    │
    ▼
Model turn N+1:
  convertTools now includes websearch (IsDeferred() == false)
  Model calls websearch(query: "...", provider: "brave")
  → executes normally
```

### Provider Changes

**Anthropic provider** (`anthropic.go`, also applies to `bedrock.go` and `vertexai.go` which use the same SDK):

```
convertTools(tools []BaseTool):
  for each tool:
    if tool.IsDeferred():
      include WITH full schema AND defer_loading: true
      → API suppresses expansion in system prompt; needs full schema to expand on tool_reference
    else:
      convert and include as before (full schema)

  cache breakpoint on last tool (tool list is stable — same tools
  every turn, deferred ones just have the flag set permanently)
```

**Other providers** (`openai.go`, `gemini.go`):

```
convertTools(tools []BaseTool):
  for each tool:
    if tool.IsDeferred():
      skip entirely (don't include in API payload)
    else:
      convert and include as before

  adjust cache breakpoint to last NON-deferred tool
```

**Anthropic tool result conversion** (`convertMessages` for Tool role):

When a `ToolResult` has `Metadata` containing `tool_references`, construct `ToolResultBlockParam` with `ToolReferenceBlockParam` content blocks instead of plain text:

```
convertMessages — Tool role:
  for each toolResult:
    if toolResult has tool_references metadata:
      build ToolResultBlockParam with:
        content: [ToolReferenceBlockParam{tool_name: name} for each ref]
    else:
      existing text/image handling
```

This enables the API server to expand the referenced tools' schemas inline for the model.

### NewToolSet Changes

```
newAgent (agent.go):
  1. Resolve model and provider type:
     model := models.SupportedModels[agentConfig.Model]
     isNativeDeferred := isAnthropicFamily(model.Provider)

  2. Call NewToolSet(ctx, info, reg, ..., isNativeDeferred)
     → returns <-chan tools.BaseTool

  3. createAgentProvider(agentInfo.ID) → provider

  4. Assemble agent struct with toolsCh, provider, etc.

  5. If toolsearch was created in step 2:
     set toolSearchTool.SetAgent(agent) via interface
     (ToolSearchTool calls agent.ResolvedTools() in Run())

NewToolSet(ctx, info, reg, ..., isNativeDeferred):
  1. Create tools as before (viewer, editor, manager groups)
  2. For each created tool:
     if IsToolDeferred(tool.Info().Name, info.DeferredTools):
       wrap in deferredWrapper → send wrapped tool to channel
     else:
       send unwrapped tool to channel

  3. Track whether any tools were deferred (hasDeferredTools flag)

  4. If hasDeferredTools AND toolsearch is enabled:
     create ToolSearchTool(isNativeDeferred)
     store reference for later SetAgent call
     send to channel

  5. MCP/LSP goroutines: same deferred check applies
```

ToolSearchTool is created with a nil agent reference. After the agent struct is assembled, `SetAgent(agent)` is called. Inside `ToolSearchTool.Run()`, it calls `agent.ResolvedTools()` (non-blocking, returns `([]BaseTool, bool)`) to get the full tool list and filters for `IsDeferred() == true`.

### ToolSearchTool Query Matching

The search algorithm supports multiple query forms, inspired by Claude Code:

1. **Exact name match**: `query: "websearch"` → matches tool named `websearch`
2. **Prefix match**: `query: "mcp_slack"` → matches `mcp_slack_send_message`, `mcp_slack_list_channels`
3. **Keyword search**: `query: "search web"` → scores tools by keyword matches in name and description
4. **Multi-select**: `query: "websearch,sourcegraph"` → comma-separated exact names

Scoring (simplified from Claude Code):
- Exact name match: +10
- Name contains query term: +5
- Description contains query term (word boundary): +2
- Minimum score threshold: 2 (filters noise)

Only deferred tools (where `IsDeferred() == true`) are searchable. Already-activated tools are excluded from results.

## Implementation Plan

### Phase 1: Config and Interface

- [ ] **1.1** Add `DeferredTools map[string]bool` field to `config.Agent` struct in `config.go` with `json:"deferredTools,omitempty"`. Add same field to `agent.AgentInfo` in `registry.go` with `yaml:"deferredTools,omitempty"`.
- [ ] **1.2** Add `IsToolDeferred(toolName string, deferredConfig map[string]bool) bool` function to `permission/evaluate.go`. Logic mirrors `IsToolEnabled`: exact match → wildcard match → default false. Hardcode exclusions: `toolsearch` and `struct_output` always return false regardless of config.
- [ ] **1.3** Add `IsDeferred() bool` method to `BaseTool` interface in `tools/tools.go`.
- [ ] **1.4** Add `IsDeferred() bool { return false }` to all existing tool implementations: `lsTool`, `globTool`, `grepTool`, `readTool`, `viewImageTool`, `fetchTool`, `skillTool`, `sourcegraphTool`, `websearchTool`, `writeTool`, `editTool`, `multiEditTool`, `deleteTool`, `patchTool`, `bashTool`, `structOutputTool`, `lspTool`, `agentTool`. Also update MCP tool wrapper in `mcp-tool.go`.
- [ ] **1.5** Create `deferredWrapper` struct in `tools/tools.go`:
  - Fields: `inner BaseTool`, `active atomic.Bool` (initially false)
  - `IsDeferred()`: returns `!active.Load()`
  - `Activate()`: `active.Store(true)`
  - All other `BaseTool` methods delegate to `inner`
- [ ] **1.6** Wire `DeferredTools` through `discoverMarkdownAgents` and `applyConfigOverrides` in `registry.go` (same merge logic as `Tools` — `maps.Copy`).
- [ ] **1.7** Regenerate `opencode-schema.json` via `go run cmd/schema/main.go > opencode-schema.json`. Add `deferredTools` property to the agent definition in `cmd/schema/main.go` with the same schema as `tools`: `{"type": "object", "additionalProperties": {"type": "boolean"}}`.

### Phase 2: NewToolSet Deferred Wrapping

- [ ] **2.1** Modify `NewToolSet` in `agent/tools.go`: after creating each tool via `createTool`, check `permission.IsToolDeferred(name, info.DeferredTools)`. If true, wrap in `deferredWrapper` before sending to channel.
- [ ] **2.2** Track whether any tools were marked deferred. If yes and `toolsearch` is enabled (via `reg.IsToolEnabled`), create and send `ToolSearchTool` to the channel. Return a reference to the ToolSearchTool instance.
- [ ] **2.3** In `newAgent` (agent.go), rearrange creation order: resolve model type first (`models.SupportedModels[agentConfig.Model]`), derive `isNativeDeferred` from `model.Provider`, pass to `NewToolSet`. After assembling the agent struct, call `toolSearchTool.SetAgent(agent)` on the returned reference.

### Phase 3: ToolSearchTool Implementation

- [ ] **3.1** Create `tools/toolsearch.go` with `ToolSearchToolName = "toolsearch"` constant.
- [ ] **3.2** Implement `toolSearchTool` struct:
  - Fields: `isNativeProvider bool`, `agent Service` (nil initially, set via `SetAgent`)
  - `SetAgent(agent Service)`: stores the agent reference
  - `Info()`: returns name `toolsearch`, description explaining deferred tool discovery, parameter `query` (string, required)
  - `Run()`: calls `agent.ResolvedTools()` to get tools, find deferred tools matching query, activate matches, return result based on provider path
  - `AllowParallelism()`: return `true`
  - `IsBaseline()`: return `true`
  - `IsDeferred()`: return `false`
- [ ] **3.3** Implement query matching: exact name → prefix → keyword scoring. Support comma-separated multi-select.
- [ ] **3.4** Implement dual result format:
  - **Native path** (`isNativeProvider == true`): Return tool names in content text ("Loaded tools: websearch, sourcegraph"). Set `Metadata` to JSON `{"tool_references": ["websearch", "sourcegraph"]}`. The Anthropic provider's `convertMessages` reads this metadata to construct `ToolReferenceBlockParam` content blocks.
  - **Fallback path** (`isNativeProvider == false`): Return formatted tool info (name, full description, parameters with types and descriptions, required fields) wrapped in `<system-reminder>` tags.
- [ ] **3.5** Handle edge case: no matches found → return informative message listing available deferred tools.

### Phase 4: Provider Changes

- [ ] **4.1** Modify `convertTools` in `provider/anthropic.go`: for deferred tools (where `IsDeferred()` returns true), include with full schema AND `DeferLoading: param.NewOpt(true)`. Keep `DeferLoading` set even after activation to preserve cache stability — the API ignores the flag for tools the model already has access to.
- [ ] **4.2** Modify `convertMessages` in `provider/anthropic.go`: when a `ToolResult` has `Metadata` containing `tool_references` JSON, construct `ToolResultBlockParam` with `ToolReferenceBlockParam` content blocks (type `"tool_reference"`, `tool_name` from metadata). Import `ToolReferenceBlockParam` from the SDK.
- [ ] **4.3** Apply `defer_loading` approach to `provider/bedrock.go` and `provider/vertexai.go` (same Anthropic SDK types).
- [ ] **4.4** Modify `convertTools` in `provider/openai.go` and `provider/gemini.go`: skip tools where `IsDeferred()` returns true entirely (these providers don't support `defer_loading`).
- [ ] **4.5** Update `CountTokens` in providers to account for deferred tools appropriately (Anthropic: count as minimal since defer_loading reduces prompt size; others: don't count deferred tools).

### Phase 5: System Prompt

- [ ] **5.1** Add `<system-reminder>` tag explanation to system prompts for agents that support tools (coder, hivemind, explorer, workhorse). Append once, before deferred tools list.
- [ ] **5.2** In `GetAgentPrompt` (`prompt.go`): after existing prompt sections, compute deferred tool names from `AgentInfo.DeferredTools` using `IsToolEnabled` + `IsToolDeferred` for built-in tool name constants only. If any exist, append `<system-reminder>` block listing them.
- [ ] **5.3** Define `allKnownToolNames` list in `prompt.go` (or import from `tools` + `agent` packages) to iterate when computing deferred names. Include all built-in tool name constants. MCP tools are excluded — they are handled by `deferred_tools_delta` messages.

### Phase 5b: Deferred Tools Delta (MCP)

- [ ] **5b.1** Add `announcedDeferredTools map[string]bool` field to the `agent` struct. Populated from the system prompt's built-in deferred tool names at agent creation.
- [ ] **5b.2** In `processGeneration`, after `resolveTools()`: scan the resolved tool list for deferred tools whose names are NOT in `announcedDeferredTools`. If any new deferred tools exist (from MCP), construct a user message with `<system-reminder>` tags listing the new tool names. Prepend it to `msgHistory` before the first API call. Update `announcedDeferredTools`.
- [ ] **5b.3** On subsequent iterations of the tool-use loop (after tool execution), re-check for MCP tool changes. If the deferred pool changed (new tools, or MCP server disconnected removing tools), inject another delta message.

### Phase 6: Tests

- [ ] **6.1** Unit test `IsToolDeferred`: exact match, wildcard patterns, `struct_output`/`toolsearch` hardcoded exclusions, nil config returns false.
- [ ] **6.2** Unit test `deferredWrapper`: `IsDeferred()` returns true initially, false after `Activate()`. All delegated methods forward correctly.
- [ ] **6.3** Unit test ToolSearchTool with both provider paths: native path returns tool_references metadata; fallback path returns formatted text in `<system-reminder>` tags. Test exact match, prefix match, keyword scoring, multi-select, no-match response, already-activated tools excluded.
- [ ] **6.4** Unit test `NewToolSet` with deferred config: verify deferred tools are wrapped, non-deferred tools are not wrapped, `toolsearch` is auto-included when deferred tools exist, `toolsearch` is NOT included when no deferred tools.
- [ ] **6.5** Unit test Anthropic `convertTools`: verify deferred tools get `DeferLoading: true` flag, non-deferred tools don't. Unit test OpenAI/Gemini `convertTools`: verify deferred tools are excluded entirely.
- [ ] **6.6** Unit test Anthropic `convertMessages`: verify `tool_references` metadata in ToolResult produces `ToolReferenceBlockParam` content blocks in the API message.
- [ ] **6.7** Unit test `GetAgentPrompt`: verify `<system-reminder>` block appears when deferred tools are configured, absent when none are configured. Verify MCP tools are NOT included in the static prompt.
- [ ] **6.8** Unit test deferred_tools_delta injection: verify delta message is prepended when MCP deferred tools are detected; verify no message when no new deferred tools; verify delta message content matches `<system-reminder>` format.
- [ ] **6.9** Integration test: simulate single-turn flow for Anthropic (ToolSearch returns tool_references → convertMessages constructs ToolReferenceBlockParam) and two-turn flow for OpenAI (ToolSearch activates wrapper → next convertTools includes tool).

### Phase 7: Documentation

- [ ] **7.1** Update `AGENTS.md` with `deferredTools` field documentation and examples.
- [ ] **7.2** Add `toolsearch` to the tool descriptions in agent prompts (coder, hivemind, explorer, workhorse) where appropriate.

## Edge Cases

### No deferred tools configured

1. Agent has no `deferredTools` in config (or empty map)
2. No tools are wrapped in `deferredWrapper`
3. `toolsearch` tool is NOT created or added to toolset
4. System prompt has no `<system-reminder>` block for deferred tools
5. Behavior identical to current system

### Tool disabled via `tools` AND listed in `deferredTools`

1. Config: `tools: {"websearch": false}, deferredTools: {"websearch": true}`
2. `IsToolEnabled` returns false → tool not created in `NewToolSet`
3. `deferredTools` has no effect — tool doesn't exist in toolset
4. Correct: you can't defer a tool that's not available

### Wildcard patterns in deferredTools

1. Config: `deferredTools: {"mcp_*": true}`
2. All MCP tools matching `mcp_*` are wrapped as deferred
3. Built-in tools like `ls`, `read` are unaffected
4. Works via existing `MatchWildcard` function in `permission/evaluate.go`

### ToolSearch called with no matching query

1. Model calls `toolsearch(query: "nonexistent_tool")`
2. No deferred tools match
3. Response: "No matching deferred tools found. Available deferred tools: websearch, sourcegraph, ..."
4. Model adjusts strategy or tries a different query

### ToolSearch called when all deferred tools already activated

1. Model calls `toolsearch(query: "websearch")` but websearch was already activated
2. `websearch.IsDeferred()` returns false → not included in search results
3. Response: "No matching deferred tools found. Tool 'websearch' is already loaded and available."
4. Model proceeds to call websearch directly

### Model tries to call deferred tool without ToolSearch

**Anthropic path**: The tool IS in the API payload (with `defer_loading: true`), so the API may allow the call but with degraded behavior since the model hasn't seen the full schema. The `<system-reminder>` instructs the model to use `toolsearch` first.

**Other providers**: The tool is NOT in the API payload, so the API rejects the call. The model receives an error and should learn to call `toolsearch` first based on the `<system-reminder>` instruction.

### Provider switching mid-conversation

1. User switches from Anthropic to OpenAI model mid-session (e.g., via agent config change)
2. Previously activated tools remain activated (wrapper state persists)
3. Deferred tools that were activated via `tool_reference` on Anthropic now appear normally in OpenAI's `convertTools` (they're no longer deferred)
4. Still-deferred tools switch from `defer_loading` to being skipped entirely
5. No special handling needed — the wrapper state is provider-agnostic

### MCP tools arriving after initial resolution

1. MCP goroutine in `NewToolSet` delivers tools asynchronously
2. Each MCP tool is checked against `deferredTools` config
3. Late-arriving MCP tools are wrapped if they match deferred patterns
4. ToolSearchTool's `getTools()` closure returns the full resolved list, which includes MCP tools once resolved

### Cache prefix impact on activation

1. Before activation: deferred tool not in `convertTools` output → shorter tool list (OpenAI/Gemini) or has `defer_loading: true` (Anthropic)
2. After activation (OpenAI/Gemini): tool appears in `convertTools` output → longer tool list → cache prefix changes
3. After activation (Anthropic): tool KEEPS `defer_loading: true` in `convertTools` output → **cache prefix unchanged** — only the `tool_reference` in tool results is new (those aren't part of the prefix)
4. The system prompt (with `<system-reminder>`) remains stable — only tool schemas change
5. For OpenAI/Gemini: to minimize churn, activated tools could be appended after the existing last tool. This preserves the prefix for all previously-sent tools.

### Custom agents via markdown

1. Agent defined in `.agents/types/reviewer.md` with `deferredTools` in frontmatter
2. `discoverMarkdownAgents` parses `deferredTools` from YAML frontmatter
3. Config override merges via `maps.Copy` (same as `Tools`)
4. Works identically to `.opencode.json` agents

### Subagent inherits deferred config

1. Workhorse subagent has `deferredTools: {"websearch": true}` in config
2. When spawned via `task` tool, gets its own toolset with websearch deferred
3. If workhorse needs web search, it calls `toolsearch` → `websearch` → executes
4. Each subagent instance has independent activation state (different wrapper instances)

## Open Questions

1. **Should there be a default set of deferred tools for built-in agents?**
   - Claude Code defers ~20 built-in tools by default (websearch, webfetch, LSP, config, cron, etc.)
   - OpenCode could ship with sensible defaults (e.g., `websearch`, `sourcegraph`, `lsp` deferred for all agents)
   - **Recommendation**: Start with no defaults. Let users opt in via config. Add defaults in a follow-up once we have usage data on which tools are rarely used.

2. **Should `searchHint` be added to `ToolInfo`?**
   - Claude Code's `Tool` type has `searchHint?: string` — a one-line capability phrase for better keyword matching
   - This could improve ToolSearch accuracy for tools with non-descriptive names
   - **Recommendation**: Defer. Tool names and descriptions are sufficient for the initial implementation. Add `searchHint` if keyword matching proves inadequate in practice.

3. **Should activated tools be placed at the end of the tool list for cache stability? (OpenAI/Gemini only)**
   - For Anthropic: resolved — keep `defer_loading: true` permanently, tool list never changes
   - For OpenAI/Gemini: inserting a newly activated tool in its "natural" position (per `OrderTools`) changes indices of subsequent tools, potentially busting the cache prefix
   - Appending at the end preserves the prefix for all previously-sent tools
   - **Recommendation**: Append activated tools at the end of the baseline section. This minimizes cache churn for non-Anthropic providers.

4. **How should compaction interact with deferred tools?**
   - After compaction, the model loses memory of having called `toolsearch`
   - But the activation state is in the wrapper (not in messages) — so tools stay activated
   - The model might call `toolsearch` again for an already-activated tool → it gets "already loaded" response
   - **Recommendation**: This is fine. No special handling needed. Compaction doesn't reset activation state.

5. **Should ToolSearchTool be visible in the TUI?**
   - Claude Code hides ToolSearch from the UI (`isAbsorbedSilently: true`, `userFacingName: ''`)
   - OpenCode could show it (consistent with other tools) or hide it (reduce noise)
   - **Recommendation**: Show it initially (simpler). Consider hiding in a follow-up if it's too noisy.

6. **Should deferred tools support markdown agent definitions?**
   - Current design supports `deferredTools` in YAML frontmatter
   - MCP tools can be deferred via wildcard patterns (`mcp_*: true`)
   - **Recommendation**: Yes, support in markdown frontmatter (same as `tools` field). This is covered by Phase 1.6.

## Success Criteria

- [ ] `deferredTools` config field accepted in `.opencode.json` and markdown agent frontmatter
- [ ] Tools matching deferred patterns are deferred in API tool schemas (excluded for OpenAI/Gemini; `defer_loading: true` for Anthropic)
- [ ] `<system-reminder>` block in system prompt lists deferred tool names
- [ ] `toolsearch` tool correctly discovers and returns deferred tool info
- [ ] After `toolsearch` activation, tool schema appears in subsequent API calls
- [ ] Model can successfully call an activated tool on the turn after `toolsearch`
- [ ] `toolsearch` auto-included only when deferred tools exist
- [ ] `struct_output` and `toolsearch` cannot be deferred (hardcoded)
- [ ] Disabled tools (`tools: {"x": false}`) are unaffected by `deferredTools`
- [ ] Wildcard patterns work in `deferredTools` (e.g., `mcp_*: true`)
- [ ] Schema validates with `deferredTools` field
- [ ] All existing agent tests pass unchanged
- [ ] Cache prefix stable until a tool is activated
- [ ] MCP deferred tools announced via `deferred_tools_delta` user messages with `<system-reminder>` tags
- [ ] Delta messages only injected when deferred tool pool changes (not on every turn)
- [ ] Anthropic: deferred tools sent with `defer_loading: true`, ToolSearch returns `tool_reference` blocks, model can call tool in same turn
- [ ] OpenAI/Gemini: deferred tools excluded from payload, ToolSearch returns formatted text, model can call tool on next turn
- [ ] Anthropic `convertMessages` correctly constructs `ToolReferenceBlockParam` from ToolSearch result metadata

## References

- `internal/llm/tools/tools.go` — `BaseTool` interface, `ToolInfo`, `deferredWrapper` (new)
- `internal/llm/tools/toolsearch.go` — ToolSearchTool implementation (new)
- `internal/llm/agent/tools.go` — `NewToolSet`, `OrderTools`, deferred wrapping (modified)
- `internal/llm/agent/agent.go` — `newAgent`, ToolSearchTool closure setup (modified)
- `internal/llm/prompt/prompt.go` — `GetAgentPrompt`, `<system-reminder>` injection (modified)
- `internal/config/config.go` — `Agent` struct, `DeferredTools` field (modified)
- `internal/agent/registry.go` — `AgentInfo`, `DeferredTools` field, merge logic (modified)
- `internal/permission/evaluate.go` — `IsToolDeferred` function (new), `MatchWildcard` (reused)
- `internal/llm/provider/anthropic.go` — `convertTools` with `DeferLoading`, `convertMessages` with `ToolReferenceBlockParam` (modified)
- `internal/llm/provider/openai.go` — `convertTools` deferred skip (modified)
- `internal/llm/provider/gemini.go` — `convertTools` deferred skip (modified)
- `internal/llm/provider/vertexai.go` — `convertTools` with `DeferLoading` (modified, same as anthropic)
- `internal/llm/provider/bedrock.go` — `convertTools` with `DeferLoading` (modified, same as anthropic)
- Anthropic SDK types used: `ToolParam.DeferLoading`, `ToolReferenceBlockParam`, `ToolResultBlockParamContentUnion`
- `cmd/schema/main.go` — `deferredTools` schema property (modified)
- Claude Code source: `src/tools/ToolSearchTool/ToolSearchTool.ts` — reference implementation
- Claude Code source: `src/tools/ToolSearchTool/prompt.ts` — `isDeferredTool()`, `formatDeferredToolLine()`
- Claude Code source: `src/services/api/claude.ts` — `queryModel()`, tool filtering flow
- Claude Code source: `src/utils/messages.ts` — `wrapInSystemReminder()`
