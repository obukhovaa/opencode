# Structured Output

OpenCode supports structured output, allowing you to force an agent's final response to conform to a user-supplied JSON schema. This is implemented as a synthetic `struct_output` tool injected into the agent's toolset.

## How It Works

When a JSON schema is provided, OpenCode:

1. Injects a `struct_output` tool with a single `output` object parameter
2. Injects the schema itself as a `<struct_output_schema>` block in the message history
3. Appends an instruction to the system prompt telling the agent to call the tool
4. The agent performs its work normally, then calls `struct_output` with the result
5. The tool parses the input as JSON, makes it schema-conformant, and returns it as plain text

## Where the schema is placed

The schema is delivered in the **message history**, not in the tool definition. This
is a prompt-caching decision.

Anthropic renders the cacheable prefix as `tools` → `system` → `messages` and
invalidates everything after the first changed byte. A `struct_output` tool whose
parameters were built from the step's schema therefore put a per-step payload at
position 0 of that prefix: two consecutive flow steps of the same agent shared no
cache at all — not the tool list, not the system prompt, and not even the message
history a `session.fork: true` step had just copied verbatim. The symptom was that
the first LLM call of every flow step was a cache miss.

Keeping the tool definition byte-identical across steps and shipping the schema
after the last cache breakpoint fixes that. The schema block is injected once per
session per schema — a second turn of the same step re-sends nothing, a forked step
with a different schema gets its own block (the block says it supersedes any earlier
one), and a session that compacts past the block has it re-injected.

Nothing about the emitted document changes: the tool result is the bare
schema-conformant object, so flow routing (`${args.<field>}`), the TUI renderer, and
downstream consumers are unaffected.

### Reverting to schema-in-the-tool

`structOutputSchemaDelivery` restores the previous behavior, globally or per agent:

```json
{
  "structOutputSchemaDelivery": "message",
  "agents": {
    "analyzer": { "structOutputSchemaDelivery": "tool" }
  }
}
```

| Value | Behavior |
|-------|----------|
| `message` (default) | Invariant tool definition; schema in the message tail. Consecutive steps share a cached prefix. |
| `tool` | Schema splayed into the tool's parameters. Every step writes a fresh cache entry. |

A per-agent value wins over the top-level one. The values are case-sensitive; an
unrecognized value logs a warning and falls back to `message`.

### Accepted call shapes

In `message` mode the declared parameter is `output`, so the expected call is
`{"output": { ...document... }}`. A model that emits the document flat — without
the wrapper — is accepted too, since it was shown the document's schema rather
than the wrapper's. Only a payload matching neither shape is rejected, with an
error result that re-enters the agent loop for a retry.

Validation is identical in both delivery modes: the tool retains the full schema
and enforces it regardless of how the model was shown it.

### Schema conformance

Before the output is returned (and, in flows, threaded into routing args), the tool reconciles it against the schema:

- **Defaults are materialized.** Any property omitted by the model that declares a `default` in the schema is filled in with that default (recursively, including inside present nested objects). Models routinely drop empty-valued fields — an empty array such as `"blockers": []` is the most common — and a missing key would otherwise be indistinguishable from "unset" to a flow routing rule that references `${args.blockers}`, causing every predicate to evaluate false and the flow to silently strand. Filling declared defaults keeps that contract whole.
- **Required fields are enforced.** If a `required` field is still absent after defaults are applied (i.e. it has no default to fall back on), the call is rejected with an error naming the missing field(s). The agent loop re-enters on that error so the model retries with a complete payload, rather than persisting a half-filled result. A `required` field that also declares a `default` is satisfied by the default and never triggers a rejection.

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
