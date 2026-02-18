# Structured Output

OpenCode supports structured output, allowing you to force an agent's final response to conform to a user-supplied JSON schema. This is implemented as a synthetic `struct_output` tool injected into the agent's toolset.

## How It Works

When a JSON schema is provided, OpenCode:

1. Injects a `struct_output` tool whose parameters match the schema
2. Appends an instruction to the system prompt telling the agent to call the tool
3. The agent performs its work normally, then calls `struct_output` with the result
4. The tool validates the input as JSON and returns it as plain text

## Usage

### CLI Flag

Use the `-f` / `--output-format` flag with `json_schema=`:

```bash
# Inline JSON schema
opencode -p "Analyze this repo" -f json_schema='{"type":"object","properties":{"summary":{"type":"string"},"issues":{"type":"array","items":{"type":"object","properties":{"file":{"type":"string"},"description":{"type":"string"}}}}}}'

# Load schema from a file
opencode -p "Rate this code" -f json_schema=./schema.json

# Use $ref to point to a file
opencode -p "Rate this code" -f json_schema='{"$ref":"./schema.json"}'
```

The schema value after `json_schema=` is resolved in this order:

1. **Inline JSON** — if it parses as valid JSON, use it directly
2. **`$ref` redirect** — if the parsed JSON has a root-level `"$ref"` key with a file path string, load the entire schema from that file (other fields in the original JSON are ignored)
3. **File path** — if it doesn't parse as JSON, treat it as a file path and read the schema from that file

The schema must be valid JSON with at least a `type` field.

### Per-Agent Config

Define a schema in `.opencode.json` for specific agents:

```json
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

### Agent Markdown Frontmatter

Agents defined as markdown files can include the schema in YAML frontmatter:

```markdown
---
name: Code Analyzer
description: Analyzes code quality
mode: subagent
output:
  schema:
    type: object
    properties:
      summary:
        type: string
      score:
        type: number
    required:
      - summary
      - score
---

You are a code analysis specialist...
```

## Scope

- **CLI flag**: Overrides the schema for **primary agents only** (not subagents)
- **Config/markdown**: Schema applies to the specific agent it's defined on
- Subagents retain their own configured schema regardless of CLI flags

## Disabling

Even when a schema is configured, the tool can be disabled per-agent:

```json
{
  "agents": {
    "coder": {
      "tools": {
        "struct_output": false
      }
    }
  }
}
```

When the tool is disabled, the structured output instruction is not added to the system prompt and the agent behaves normally with free-form output.

## Output Formats

The `--output-format` flag supports three formats:

| Format | Description |
|--------|-------------|
| `text` | Plain text output (default) |
| `json` | Wraps response in `{"response": "..."}` |
| `json_schema='{...}'` | Inline JSON schema |
| `json_schema=/path/to/file.json` | Load schema from file |
| `json_schema='{"$ref":"/path/to/file.json"}'` | Load schema via `$ref` |

In non-interactive mode with `json_schema`, the output is the raw structured JSON from the `struct_output` tool call — no wrapper object is added.
