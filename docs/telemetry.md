# Telemetry & Observability

OpenCode supports telemetry for tracking LLM usage, costs, and agent behavior. This includes direct [Langfuse](https://langfuse.com) integration for tracing, as well as provider-level metadata injection.

## Langfuse Integration

Langfuse provides LLM observability with traces, generations, tool calls, token usage, and cost tracking. OpenCode sends telemetry via the OpenTelemetry (OTLP) protocol directly to Langfuse — no proxy or collector required.

### What Gets Traced

| Langfuse Concept | OpenCode Source | Description |
|---|---|---|
| **Session** | Root session ID | Groups all traces for a conversation (including subagent sessions) |
| **Trace** | Agent turn | One trace per `processGeneration` call — covers the full tool-use loop. With `telemetry.generations` enabled for the agent, trace input is the user message that started the turn and trace output is the final assistant response (or structured output) |
| **Generation** | LLM API call | Each `StreamResponse`/`SendMessages` call with model, tokens, cost, timing. With `telemetry.generations` enabled for the agent, input is the exact request payload (system prompt + message history, chat format) and output is the exact response (content, reasoning, tool calls, finish reason) |
| **Tool** | Tool execution | Each tool call with name, timing, optional input/output |

### Setup

1. Get your API keys from [Langfuse](https://langfuse.com) (Settings > API Keys)
2. Configure in `.opencode.json`:

```json
{
  "telemetry": {
    "langfuse": {
      "enabled": true,
      "publicKey": "env:LANGFUSE_PUBLIC_KEY",
      "secretKey": "env:LANGFUSE_SECRET_KEY",
      "baseURL": "https://cloud.langfuse.com"
    }
  }
}
```

Or set environment variables directly:

```bash
export LANGFUSE_PUBLIC_KEY=pk-lf-...
export LANGFUSE_SECRET_KEY=sk-lf-...
export LANGFUSE_BASE_URL=https://cloud.langfuse.com  # optional, this is the default
```

### Langfuse Config Fields

| Field | Type | Required | Description |
|---|---|---|---|
| `enabled` | bool | yes | Enable/disable Langfuse tracing |
| `secretKey` | string | yes | Langfuse secret key. Supports `env:VAR_NAME` syntax |
| `publicKey` | string | yes | Langfuse public key. Supports `env:VAR_NAME` syntax |
| `baseURL` | string | no | Langfuse host URL. Falls back to `LANGFUSE_BASE_URL` env var, then `https://cloud.langfuse.com` |

### Trace Hierarchy

A typical conversation produces this structure in Langfuse:

```
Session: abc-123
├── Trace: coder (agent turn 1)
│   ├── Generation: coder/claude-sonnet-4-6
│   ├── Tool: bash
│   ├── Tool: read
│   ├── Generation: coder/claude-sonnet-4-6
│   ├── Tool: edit
│   └── Generation: coder/claude-sonnet-4-6
├── Trace: explorer (subagent)
│   ├── Generation: explorer/claude-sonnet-4-6
│   ├── Tool: glob
│   └── Generation: explorer/claude-sonnet-4-6
└── Trace: descriptor (title generation)
    └── Generation: descriptor/gemini-3.0-flash
```

### Flow Tracing

When running [flows](flows.md), traces are enriched with flow context:

- **Trace name** becomes `agentID/flowID/stepID` (e.g., `coder/deploy/analyze`)
- **Metadata** includes `flow_id`, `flow_step_id`, and any matched flow args (as bare keys, no prefix)

```
Session: deploy-flow-abc123
├── Trace: coder/deploy/analyze      [flow_id=deploy, flow_step_id=analyze]
├── Trace: coder/deploy/implement    [flow_id=deploy, flow_step_id=implement]
└── Trace: coder/deploy/verify       [flow_id=deploy, flow_step_id=verify]
```

## Telemetry Config Reference

The `telemetry` section in `.opencode.json` controls all telemetry behavior:

```json
{
  "telemetry": {
    "userId": "artem@example.com",
    "tags": ["team:platform", "env:dev"],
    "defaultTags": ["agent"],
    "langfuse": { ... },
    "tools": { ... },
    "flowArgs": ["ticket_id", "project*"]
  }
}
```

### Fields

| Field | Type | Description |
|---|---|---|
| `userId` | string | User identifier for traces and provider metadata. Overridden by `OPENCODE_USER_ID` env var. If neither is set, an auto-generated UUID is used. |
| `tags` | string[] | Static tags attached to every trace and provider metadata request (e.g., `["team:platform", "env:prod"]`). |
| `defaultTags` | string[] | Predefined dynamic tag keys. Currently only `"agent"` is supported — adds the active agent name as a tag. |
| `langfuse` | object | Langfuse configuration (see [above](#langfuse-config-fields)). |
| `tools` | object | Controls tool input/output logging (see [below](#tool-io-logging)). |
| `generations` | object | Controls LLM request/response logging (see [below](#llm-request--response-logging)). |
| `flowArgs` | string[] | Flow argument names to extract into Langfuse trace metadata. Supports wildcards (e.g., `"ticket_id"`, `"project*"`, `"*"`). |
| `metadataNamespace` | string | Prefix for custom metadata keys. When set, keys like `flow_id` become `namespace.flow_id` — grouping them in the Langfuse UI while keeping each independently filterable. Empty (default) preserves flat keys. |

### Tool I/O Logging

By default, tool spans record only the tool name and timing. To include input/output content in traces, enable tool logging:

```json
{
  "telemetry": {
    "tools": {
      "enabled": true,
      "logInput": ["bash", "read", "edit"],
      "logOutput": ["bash", "grep"]
    }
  }
}
```

| Field | Type | Description |
|---|---|---|
| `enabled` | bool | Master switch. When `false`, no tool input/output is logged regardless of other fields. |
| `logInput` | string[] | Tool name patterns whose input should be logged. Supports wildcards (`"*"` = all tools, `"datadog*"` = prefix match). If empty, no inputs are logged. |
| `logOutput` | string[] | Tool name patterns whose output should be logged. Same wildcard support. If empty, no outputs are logged. |

Tool input/output is truncated to 10KB. Error output is always logged regardless of `logOutput` patterns — errors are diagnostic, not sensitive content.

### LLM Request / Response Logging

By default, generation observations record only model, tokens, cost and timing — the prompt and completion themselves are **not** sent to Langfuse. Enable `telemetry.generations` to capture them, selecting agents by ID:

```json
{
  "telemetry": {
    "generations": {
      "enabled": true,
      "logInput": ["workhorse", "coder"],
      "logOutput": ["*"]
    }
  }
}
```

| Field | Type | Description |
|---|---|---|
| `enabled` | bool | Master switch. When `false` or absent (default), no prompt or completion content is logged regardless of other fields. |
| `logInput` | string[] | Agent ID patterns whose **request** payload is logged — the system prompt plus the cleaned message history, rendered in chat format. Also sets the trace-level input. Supports wildcards (`"*"` = all agents, `"sub*"` = prefix match). If empty, no request payloads are logged. |
| `logOutput` | string[] | Agent ID patterns whose **response** is logged — content, reasoning, tool calls and finish reason. Also sets the trace-level output. Same wildcard support. If empty, no responses are logged. |

Notes:

- Patterns match the agent ID that issued the call, case-insensitively. Built-in agents that serve a specific job use their own ID, so they are selectable independently: `descriptor` (title generation), `summarizer` (compaction / recap), plus any configured agent (`coder`, `workhorse`, custom markdown agents, …).
- Payloads are truncated to 400KB per attribute. Binary attachments are summarized (`[binary attachment: <mime>, N bytes]`), never embedded.
- **This is the full conversation.** An opted-in agent sends the entire message history — including tool results embedded in it — to Langfuse, independently of the per-tool `logInput`/`logOutput` patterns above, which only govern the dedicated tool observations. Opt in per agent rather than using `"*"` when any agent handles sensitive content.
- Enabling this materially increases export volume (up to ~800KB per LLM call versus a few hundred bytes). Spans are exported in batches, so on a busy session an oversized batch can be rejected by the ingestion endpoint and lose the whole batch — including traces that carried no payload. Prefer scoping `logInput`/`logOutput` to the agents you are actually debugging.

### Flow Args

When using flows, you can extract business-critical arguments into Langfuse trace metadata for filtering and analysis:

```json
{
  "telemetry": {
    "flowArgs": ["ticket_id", "environment", "project*"]
  }
}
```

Each matched arg appears as a dedicated metadata field (e.g., `ticket_id`). Values are truncated to 200 characters. Only top-level args are checked.

### Metadata Namespace

By default, custom metadata keys (`flow_id`, `agent_id`, flow args, etc.) are stored as flat top-level keys in Langfuse metadata. When multiple systems write metadata to the same traces, this can cause collisions or clutter.

Set `metadataNamespace` to group all OpenCode-specific metadata under a common prefix:

```json
{
  "telemetry": {
    "metadataNamespace": "app"
  }
}
```

This changes how metadata keys appear in Langfuse:

| Without namespace (default) | With `"metadataNamespace": "app"` |
|---|---|
| `flow_id` | `app.flow_id` |
| `flow_step_id` | `app.flow_step_id` |
| `ticket_id` | `app.ticket_id` |
| `agent_id` | `app.agent_id` |
| `session_id` | `app.session_id` |
| `opencode_version` | `app.opencode_version` |

Each prefixed key remains a top-level metadata entry in Langfuse and is independently filterable. Standard Langfuse attributes (`langfuse.session.id`, `langfuse.user.id`, `langfuse.trace.tags`, etc.) are not affected — only custom metadata keys are namespaced.

## Provider Metadata

Separate from Langfuse, OpenCode can inject metadata directly into LLM API request bodies. This is configured per-provider:

```json
{
  "providers": {
    "anthropic": {
      "apiKey": "...",
      "metadata": {
        "sessionId": "session_id",
        "userId": "user_id",
        "tags": "tags"
      }
    }
  }
}
```

The `metadata` object maps built-in identifiers to the field names sent in the API request body:

| Key | Description | Resolved From |
|---|---|---|
| `sessionId` | Current session ID | Session context |
| `userId` | User identifier | `OPENCODE_USER_ID` > `telemetry.userId` > auto UUID |
| `tags` | Tag array | `telemetry.tags` + dynamic tags (e.g., agent name) |

### Provider Support

Not all providers handle metadata the same way:

| Provider | Metadata Support | Notes |
|---|---|---|
| **Anthropic** | Partial | Only `user_id` is a native field. Other fields are passed as extra fields. |
| **OpenAI** | Full | All fields supported. Arrays are joined as comma-separated strings. |
| **Gemini** | Full | Metadata passed in HTTP body `ExtraBody`. |
| **Bedrock** | None | **All metadata is stripped.** Bedrock's native API rejects unknown fields, and many proxies (e.g., litellm) also reject them. |
| **VertexAI** | Partial | Delegates to the underlying provider (Anthropic or Gemini). |

> **Tip:** If you're using Langfuse, you don't need provider metadata at all. Langfuse captures richer observability data (traces, tool calls, cost) independently of what the LLM API accepts. Provider metadata is useful when routing through observability proxies that read request bodies.

## Full Example

```json
{
  "telemetry": {
    "userId": "artem@example.com",
    "tags": ["team:composer"],
    "defaultTags": ["agent"],
    "metadataNamespace": "app",
    "langfuse": {
      "enabled": true,
      "publicKey": "env:LANGFUSE_PUBLIC_KEY",
      "secretKey": "env:LANGFUSE_SECRET_KEY"
    },
    "tools": {
      "enabled": true,
      "logInput": ["bash", "edit", "read"],
      "logOutput": ["*"]
    },
    "flowArgs": ["ticket_id", "project_name"]
  },
  "providers": {
    "anthropic": {
      "apiKey": "env:ANTHROPIC_API_KEY"
    }
  }
}
```

This configuration:
- Sends all traces to Langfuse with user ID and team tag
- Groups custom metadata under the `app.` prefix (e.g., `app.flow_id`, `app.agent_id`, `app.ticket_id`)
- Logs input for `bash`, `edit`, and `read` tools; logs output for all tools
- Extracts `ticket_id` and `project_name` from flow args into trace metadata
- Does not use provider-level metadata (recommended when using Langfuse)
