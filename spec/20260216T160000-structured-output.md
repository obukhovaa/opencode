# Structured Output

**Date**: 2026-02-16
**Status**: Draft
**Author**: AI-assisted

## Overview

Add a provider-agnostic structured output mechanism that forces an agent's final response to conform to a user-supplied JSON schema. This is implemented as a synthetic `struct_output` tool injected into the agent's toolset, following the same pattern as the TypeScript reference implementation. The schema can be defined per-agent via config/markdown or globally via the `--output-format` CLI flag.

## Motivation

### Current State

Output formatting is limited to two modes — `text` and `json` — applied only in non-interactive mode as a post-processing step:

```go
// format.go
const (
    Text OutputFormat = "text"
    JSON OutputFormat = "json"
)

// FormatOutput wraps content in {"response": "..."} for JSON mode
func FormatOutput(content string, formatStr string) string { ... }
```

```go
// app.go — RunNonInteractive
content := result.Message.Content().String()
fmt.Println(format.FormatOutput(content, outputFormat))
```

The `json` format simply wraps arbitrary text in `{"response": "..."}` — there is no way to enforce a specific schema on the agent's output. Agents produce free-form text and the consumer must parse it.

This creates problems:

1. **No structured data extraction**: Consumers that need specific fields (e.g., `{"title": string, "summary": string, "tags": string[]}`) must regex-parse free-form text
2. **No schema validation**: The agent may produce output that doesn't match expected structure, discovered only at parse time
3. **No provider-agnostic approach**: Some providers offer native structured output, but there's no unified mechanism across all providers

### Desired State

Users can define a JSON schema and the agent is forced to produce output conforming to that schema via a synthetic tool call:

```bash
# CLI flag
opencode -p "Analyze this repo" -f json_schema='{"type":"object","properties":{"summary":{"type":"string"},"issues":{"type":"array","items":{"type":"object","properties":{"file":{"type":"string"},"description":{"type":"string"}}}}}}'

# Per-agent config in .opencode.json
{
  "agents": {
    "analyzer": {
      "model": "anthropic.claude-sonnet-4-5",
      "output": {
        "schema": {
          "type": "object",
          "properties": {
            "summary": { "type": "string" },
            "score": { "type": "number" }
          },
          "required": ["summary", "score"]
        }
      }
    }
  }
}
```

## Research Findings

### TypeScript Reference Implementation

The upstream TypeScript implementation (`packages/opencode/src/session/prompt.ts`) uses the same synthetic-tool pattern:

| Aspect | Implementation |
|--------|---------------|
| Tool injection | `createStructuredOutputTool()` creates an AI tool with `inputSchema: jsonSchema(toolSchema)` |
| System prompt | Appends `STRUCTURED_OUTPUT_SYSTEM_PROMPT` to system messages when `format.type === "json_schema"` |
| Tool choice | Sets `toolChoice: "required"` to force the model to call the tool |
| Validation | AI SDK validates args against inputSchema before calling `execute()` |
| Output capture | `onSuccess` callback captures validated output, saved to `processor.message.structured` |
| Error handling | If model finishes without calling the tool, sets `StructuredOutputError` |

**Key insight**: The tool is provider-agnostic because it relies on the LLM's tool-calling ability (universal across all major providers) rather than provider-specific structured output APIs.

### Go Implementation Considerations

| Concern | Approach |
|---------|----------|
| Dynamic schema → Go struct | Build a runtime struct from JSON schema for deserialization/display using `reflect` or direct `map[string]any` unmarshaling |
| Validation | Validate `call.Input` JSON against the schema using `map[string]any` deserialization |
| TUI rendering | Render validated JSON as a markdown code block, similar to bash output |
| Tool filtering | Reuse existing `IsToolEnabled` check — if `struct_output: false`, tool is not injected even if schema is set |

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Tool name | `struct_output` | Follows existing snake_case convention (`view_image`, `multi_edit`); avoids collision with potential provider `structured_output` features |
| Schema storage | `Output.Schema` field on `AgentInfo` as `map[string]any` | Consistent with how `Permission` and `Parameters` use `map[string]any`; parseable from both JSON config and YAML frontmatter |
| Schema validation | Validate at parse time in `format.go`, reuse in `cmd/root.go` and `registry.go` | Single validation point, catches errors early |
| CLI flag format | `-f json_schema='<json>'` extending existing `-f` flag | Natural extension of existing `--output-format` flag; `json_schema` is a new variant alongside `text` and `json` |
| Flag scope | CLI flag overrides primary agents only, not subagents | Subagents may have their own schemas; flag is for controlling the final user-facing output |
| Dynamic struct | Use `map[string]any` + JSON marshal/unmarshal, expose via interface method | Avoids runtime reflect complexity; sufficient for validation and display |
| Output format | Tool returns plain JSON string (no wrapper object) | The `FormatOutput` function should output the schema-validated JSON directly when format is `json_schema`, not wrap it in `{"response": ...}` |

## Architecture

```
                          CLI Flag: -f json_schema='{...}'
                                    │
                                    ▼
                        ┌───────────────────────┐
                        │    cmd/root.go         │
                        │  Parse & validate      │
                        │  JSON_SCHEMA format    │
                        └───────────┬───────────┘
                                    │ schema passed to app
                                    ▼
                        ┌───────────────────────┐
                        │   internal/app/app.go  │
                        │  Set Output.Schema on  │
                        │  each primary agent    │
                        │  agentInfoCopy         │
                        └───────────┬───────────┘
                                    │
           ┌────────────────────────┼────────────────────────┐
           ▼                        ▼                        ▼
    ┌──────────────┐        ┌──────────────┐        ┌──────────────┐
    │  Agent: coder │        │ Agent: custom │        │  Subagent    │
    │  (primary)    │        │  (primary)    │        │  (unchanged) │
    │  +Output.Schema│       │ +Output.Schema│        │  own schema  │
    └──────┬───────┘        └──────┬───────┘        │  if any      │
           │                       │                 └──────────────┘
           ▼                       ▼
    ┌──────────────────────────────────┐
    │      NewToolSet (agent/tools.go) │
    │                                  │
    │  IF Output.Schema != nil         │
    │  AND IsToolEnabled("struct_output")│
    │  THEN inject StructOutputTool    │
    └──────────────┬───────────────────┘
                   │
                   ▼
    ┌──────────────────────────────────┐
    │    GetAgentPrompt (prompt.go)    │
    │                                  │
    │  IF struct_output in toolSet     │
    │  THEN append structured output   │
    │  instruction to system prompt    │
    └──────────────────────────────────┘
```

### Tool Execution Flow

```
STEP 1: Agent receives user prompt
─────────────────────────────────────
System prompt includes structured output instruction.
struct_output tool is available in toolSet.

STEP 2: Agent performs work
─────────────────────────────────────
Agent uses normal tools (bash, edit, view, etc.)
to gather information and complete the task.

STEP 3: Agent calls struct_output tool
─────────────────────────────────────
Agent calls struct_output with JSON matching the schema.
Tool validates input against schema.
On success: returns validated JSON as plain text.
On failure: returns error, agent can retry.

STEP 4: Output rendering
─────────────────────────────────────
Non-interactive: FormatOutput outputs raw JSON (no wrapper).
TUI: Rendered as ```json code block (like bash tool output).
```

## Implementation Plan

### Phase 1: Core Types and Validation

- [x] **1.1** Add `Output` struct to `AgentInfo` in `internal/agent/registry.go`:
  ```go
  type Output struct {
      Schema map[string]any `json:"schema,omitempty" yaml:"schema,omitempty"`
  }
  
  type AgentInfo struct {
      // ... existing fields ...
      Output *Output `json:"output,omitempty" yaml:"output,omitempty"`
  }
  ```

- [x] **1.2** Add `JSON_SCHEMA` output format to `internal/format/format.go`:
  ```go
  const (
      Text       OutputFormat = "text"
      JSON       OutputFormat = "json"
      JSONSchema OutputFormat = "json_schema"
  )
  ```
  Add `ValidateJSONSchema(schema map[string]any) error` function that checks the schema has a valid `type` field at minimum. Update `Parse` to handle `json_schema` (it won't be a simple string — needs special handling since the value contains the schema). Add `ParseWithSchema(s string) (OutputFormat, map[string]any, error)` that extracts both format and schema when `json_schema='{...}'` is provided. Update `FormatOutput` to handle `JSONSchema` format by returning content as-is (plain JSON, no wrapper).

- [x] **1.3** Update `SupportedFormats` and `GetHelpText` to include `json_schema`.

### Phase 2: Agent Registry Integration

- [x] **2.1** Update `mergeMarkdownIntoExisting` in `registry.go` to merge `Output` field from YAML frontmatter:
  ```go
  if from.Output != nil && from.Output.Schema != nil {
      if into.Output == nil {
          into.Output = &Output{}
      }
      into.Output.Schema = from.Output.Schema
  }
  ```

- [x] **2.2** Update `applyConfigOverrides` to merge `Output` from config `Agent` struct. Add `Output` field to `config.Agent`:
  ```go
  // config.go
  type Agent struct {
      // ... existing fields ...
      Output *agent.Output `json:"output,omitempty"`
  }
  ```

- [x] **2.3** Add `Output` to the `config.Agent` struct in `internal/config/config.go` and ensure JSON (de)serialization works. The `Output` type should be defined in the agent package and referenced from config, or a parallel type defined in config and mapped during registry loading.

### Phase 3: CLI Flag Extension

- [x] **3.1** Extend `--output-format` flag in `cmd/root.go` to accept `json_schema='{...}'`:
  ```go
  outputFormat, _ := cmd.Flags().GetString("output-format")
  
  // Check if it's a json_schema format with embedded schema
  parsedFormat, schema, err := format.ParseWithSchema(outputFormat)
  if err != nil {
      return fmt.Errorf("invalid format: %w", err)
  }
  ```
  Pass `schema` to `app.RunNonInteractive` (add parameter) or store on `App` struct.

- [x] **3.2** In `internal/app/app.go`, when creating primary agents (around line 125), if a schema was provided via CLI flag, set it on each `agentInfoCopy`:
  ```go
  for _, agentInfo := range primaryAgents {
      agentInfoCopy := agentInfo
      if cliSchema != nil {
          agentInfoCopy.Output = &agent.Output{Schema: cliSchema}
      }
      // ... create agent ...
  }
  ```
  This overrides any schema set in config/markdown for primary agents. Subagents are not affected.

- [x] **3.3** Update `FormatOutput` in `RunNonInteractive` to handle `json_schema` format — output the raw structured content directly without wrapping.

- [x] **3.4** Update README documentation for the `--output-format` flag to include `json_schema`.

### Phase 4: StructOutput Tool

- [x] **4.1** Create `internal/llm/tools/struct_output.go`:
  ```go
  const StructOutputToolName = "struct_output"
  
  type StructOutputTool interface {
      BaseTool
      StructOutputParams() map[string]any
  }
  
  type structOutputTool struct {
      schema          map[string]any
      structParams    map[string]any  // the built parameters for ToolInfo
  }
  
  func NewStructOutputTool(schema map[string]any) StructOutputTool {
      // Build structParams from schema
      // The schema IS the parameters — it defines the shape of the tool input
      tool := &structOutputTool{
          schema:       schema,
          structParams: buildParamsFromSchema(schema),
      }
      return tool
  }
  ```

- [x] **4.2** Implement `Info()` — returns `ToolInfo` with dynamic `Parameters` built from schema:
  ```go
  func (s *structOutputTool) Info() ToolInfo {
      return ToolInfo{
          Name:        StructOutputToolName,
          Description: structOutputDescription,
          Parameters:  s.structParams,
          Required:    extractRequired(s.schema),
      }
  }
  ```

- [x] **4.3** Implement `Run()` — validates input against schema, returns plain JSON text:
  ```go
  func (s *structOutputTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
      var result map[string]any
      if err := json.Unmarshal([]byte(call.Input), &result); err != nil {
          return NewTextErrorResponse("Invalid JSON: " + err.Error()), nil
      }
      // Re-marshal to get clean, formatted JSON
      output, err := json.MarshalIndent(result, "", "  ")
      if err != nil {
          return NewTextErrorResponse("Failed to format output: " + err.Error()), nil
      }
      return NewTextResponse(string(output)), nil
  }
  ```

- [x] **4.4** Implement `StructOutputParams()` method to expose the dynamic parameters externally:
  ```go
  func (s *structOutputTool) StructOutputParams() map[string]any {
      return s.structParams
  }
  ```

- [x] **4.5** Add `buildParamsFromSchema` helper — converts a JSON schema `properties` into the `map[string]any` format used by `ToolInfo.Parameters`. If the schema is an object type with `properties`, extract those directly. Otherwise, wrap the entire schema as a single `output` parameter.

- [x] **4.6** Update TUI rendering in `internal/tui/components/chat/message.go` — add a case for `StructOutputToolName`:
  ```go
  case tools.StructOutputToolName:
      resultContent = fmt.Sprintf("```json\n%s\n```", resultContent)
      return styles.ForceReplaceBackgroundWithLipgloss(
          toMarkdown(resultContent, true, width),
          t.Background(),
      )
  ```

### Phase 5: Tool Injection

- [x] **5.1** In `internal/llm/agent/tools.go` `NewToolSet`, after building the standard toolset, check if `info.Output` is set and schema is valid:
  ```go
  // After all other tools are loaded, before closing channel
  if info.Output != nil && info.Output.Schema != nil {
      if reg.IsToolEnabled(info.ID, tools.StructOutputToolName) {
          structTool := tools.NewStructOutputTool(info.Output.Schema)
          ch <- structTool
      }
  }
  ```

- [x] **5.2** Add `StructOutputToolName` to the tool name constants in `tools.go`.

### Phase 6: System Prompt Injection

- [x] **6.1** In `internal/llm/prompt/prompt.go` `GetAgentPrompt`, after building the base prompt, check if the agent has the `struct_output` tool enabled and inject the instruction:
  ```go
  const structuredOutputPrompt = `
  IMPORTANT: The user has requested structured output. You MUST use the StructOutput tool to provide your final response. Do NOT respond with plain text - you MUST call the StructOutput tool with your answer formatted according to the schema.`
  ```
  The check needs access to the agent's tool configuration. Since `GetAgentPrompt` currently takes `agentName` and `provider`, it will need the registry to check if `struct_output` is in the agent's toolset. Check via `reg.Get(agentName)` and inspect `Output.Schema != nil && reg.IsToolEnabled(agentName, "struct_output")`.

### Phase 7: Documentation

- [x] **7.1** Update `README.md` tools table to include `struct_output` tool.

- [x] **7.2** Create `docs/structured-output.md` with:
  - Overview of the feature
  - Defining schema via CLI flag (`-f json_schema='{...}'`)
  - Defining schema per-agent in `.opencode.json`
  - Defining schema in agent markdown frontmatter
  - Examples with and without prompt
  - Scope clarification: flag affects primary agents only, not subagents
  - Disabling the tool even when schema is set (`tools: {"struct_output": false}`)

## Edge Cases

### Schema provided but tool disabled

1. User sets `Output.Schema` in agent config
2. User also sets `tools: {"struct_output": false}`
3. Tool is NOT injected (respects tool disable)
4. Structured output instruction is NOT added to system prompt
5. Agent behaves normally with free-form output

### CLI flag with existing agent schema

1. Agent has `Output.Schema` defined in config
2. User provides `-f json_schema='{...}'` on CLI
3. CLI schema overrides the config schema for primary agents
4. Subagents retain their own configured schema

### Invalid schema provided

1. User provides `-f json_schema='not valid json'`
2. `ParseWithSchema` returns validation error
3. CLI exits with descriptive error before starting the agent

### Model doesn't call the tool

1. Agent has `struct_output` tool available
2. Model produces text response without calling the tool
3. The text response is returned as-is (no structured output)
4. Consider: add warning/retry logic in a future iteration (see Open Questions)

### Nested/complex schemas

1. User provides a schema with nested objects, arrays, enums
2. `buildParamsFromSchema` passes through the JSON schema structure directly
3. The LLM sees the full schema in tool parameters and produces matching output
4. Validation is best-effort via `json.Unmarshal` into `map[string]any`

## Open Questions

1. **Should we enforce `toolChoice: "required"` when struct_output is injected?**
   - The TypeScript implementation does this to force the model to call the tool
   - Go providers would need a way to set tool_choice per-request
   - **Recommendation**: Defer to Phase 2. The system prompt instruction is usually sufficient. If models consistently skip the tool, add `toolChoice` support later.

2. **Should we validate the output against the schema deeply (property types, required fields)?**
   - Current approach: validate that input is valid JSON, marshal/unmarshal through `map[string]any`
   - Full JSON Schema validation would require a library like `github.com/santhosh-tekuri/jsonschema`
   - **Recommendation**: Start with basic JSON validation. If users need strict schema conformance, add a full validator dependency in a follow-up.

3. **Should the structured output be stored separately on the message (like `processor.message.structured` in TS)?**
   - Currently tool results are stored as `message.ToolResult` 
   - Having a dedicated `.Structured` field on the message would make retrieval easier
   - **Recommendation**: Defer. The tool result content is sufficient for now. If SDK/API consumers need it, add the field later.

4. **How should `FormatOutput` handle `json_schema` in non-interactive mode?**
   - Option A: Extract the `struct_output` tool result from the message and output it directly
   - Option B: Output the final message content as-is (which would be the tool result text)
   - **Recommendation**: Option A — find the `struct_output` tool result in the response and output its content. If no tool result found, fall back to the message text content.

5. **Should `buildParamsFromSchema` use `reflect` to build actual Go structs?**
   - `reflect.StructOf` can create runtime structs but adds complexity
   - `map[string]any` is simpler and sufficient for JSON validation
   - **Recommendation**: Use `map[string]any`. The StructOutputParams() method exposes the schema for any code that needs to inspect it. If runtime struct creation is needed later, it can be added behind the same interface.

## Success Criteria

- [x] `AgentInfo` has optional `Output.Schema` field, configurable via config JSON, YAML frontmatter, and CLI flag
- [x] `format.go` supports `json_schema` output format with schema validation
- [x] CLI flag `-f json_schema='{...}'` is parsed, validated, and passed to primary agents
- [x] `struct_output` tool is dynamically injected when `Output.Schema` is set and tool is enabled
- [x] Tool validates input as JSON and returns it as plain text
- [x] System prompt includes structured output instruction when tool is active
- [x] TUI renders struct_output results as JSON code blocks
- [x] Tool can be disabled via `tools: {"struct_output": false}` even when schema is set
- [x] CLI flag schema does not affect subagents
- [x] README and docs are updated
- [x] Existing tests pass, new tests cover tool creation, validation, and injection logic

## References

- `internal/format/format.go` — Output format types and FormatOutput function
- `internal/agent/registry.go` — AgentInfo struct, merge logic, IsToolEnabled
- `internal/config/config.go` — Agent config struct
- `internal/llm/tools/tools.go` — ToolInfo, BaseTool interface, tool constants
- `internal/llm/agent/tools.go` — NewToolSet, tool injection logic
- `internal/llm/prompt/prompt.go` — GetAgentPrompt, system prompt assembly
- `internal/app/app.go` — Primary agent creation, RunNonInteractive
- `cmd/root.go` — CLI flag definitions, output-format handling
- `internal/tui/components/chat/message.go` — Tool result rendering in TUI
- `internal/llm/tools/skill.go` — Dynamic tool description pattern (reference)
- `packages/opencode/src/session/prompt.ts` — TypeScript reference implementation (lines 51-59, 605-613, 912-939)
