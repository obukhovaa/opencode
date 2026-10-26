# Configuration

OpenCode reads `.opencode.json` from the first of these that exists:

1. `./.opencode.json` (project directory)
2. `$XDG_CONFIG_HOME/opencode/.opencode.json`
3. `$HOME/.opencode.json`

The JSON Schema for the file is [`opencode-schema.json`](../opencode-schema.json); point your editor at it for completion and validation.

Any string value can reference the environment with `${VAR}` or `${VAR:-default}`, so secrets stay out of the file (`"apiKey": "${MY_KIMI_KEY}"`). An unset `${VAR}` without a default fails loading; `$${VAR}` keeps the text literal. Full rules: [config-env.md](config-env.md).

## Example

```json
{
  "data": {
    "directory": ".opencode"
  },
  "providers": {
    "anthropic": { "apiKey": "..." },
    "vertexai": {
      "project": "your-project-id",
      "location": "us-central1"
    }
  },
  "agents": {
    "coder": {
      "model": "vertexai.claude-opus-4-6",
      "maxTokens": 5000,
      "reasoningEffort": "high"
    },
    "explorer": {
      "model": "claude-4-5-sonnet[1m]",
      "maxTokens": 5000
    },
    "summarizer": {
      "model": "vertexai.gemini-3.0-flash",
      "maxTokens": 5000
    },
    "descriptor": {
      "model": "claude-4-5-sonnet[1m]",
      "maxTokens": 80
    }
  },
  "shell": {
    "path": "/bin/bash",
    "args": ["-l"]
  },
  "mcpServers": {
    "example": {
      "type": "stdio",
      "command": "path/to/mcp-server",
      "args": []
    }
  },
  "lsp": {
    "gopls": {
      "initialization": { "codelenses": { "test": true } }
    }
  },
  "sessionProvider": { "type": "sqlite" },
  "skills": { "paths": ["~/my-skills"] },
  "permission": {
    "skill": { "*": "ask" },
    "rules": {
      "bash": { "*": "ask", "git *": "allow" },
      "edit": { "*": "allow" },
      "read": { "/proc/*": "deny" }
    }
  },
  "webSearch": {
    "providers": {
      "tavily": {
        "baseUrl": "https://api.tavily.com/search",
        "apiKey": "env:TAVILY_API_KEY",
        "description": "Web search via Tavily"
      }
    }
  },
  "webFetch": { "maxOutputBytes": 51200 },
  "autoCompact": true,
  "debug": false
}
```

## Where each section is documented

| Key | Guide |
|-----|-------|
| `providers` | [Providers & models](providers.md) |
| `agents`, `agentPaths` | [Agents](agents.md) |
| `permission` | [Permissions](#permissions) below, [tool permissions](tool-permissions.md) |
| `autoCompact` | [Compaction](compaction.md) |
| `shell`, `toolEnv`, `webFetch` | [Built-in tools](tools.md) |
| `maxTurns`, `toolCallTimeoutSeconds` | [Agent fields](agents.md#agent-fields) (top-level values are the defaults) |
| `mcpServers` | [MCP servers](mcp.md) |
| `cliTools` | [Workspace CLI tools](cli-tools.md) |
| `lsp`, `disableLSPDownload` | [LSP](lsp.md) |
| `skills` | [Skills](skills.md) |
| `contextPaths`, `contextDiscovery` | [Context files](context.md) |
| `flowPaths` | [Flows](flows.md) |
| `hooks` | [Hooks](hooks.md) |
| `router` | [Chat bridge](bridge.md) |
| `backgroundTasks` | [Background tasks](background-tasks.md) |
| `memory` | [Project memory](memory.md) |
| `telemetry` | [Telemetry & Langfuse](telemetry.md) |
| `sessionProvider` | [Session providers](session-providers.md) |
| `structOutputSchemaDelivery` | [Structured output](structured-output.md) |
| `tui` | [TUI](tui.md) |

## Permissions

Each tool call resolves to `allow` (run immediately), `deny` (blocked) or `ask` (prompt the user; the default). Resolution order:

1. **Agent tool disable**: `agents.coder.tools.bash = false` → deny
2. **Agent-specific**: `agents.coder.permission.bash.{"git *": "allow"}`
3. **Global**: `permission.rules.bash = "ask"` or `permission.skill.internal-* = deny`
4. **Default**: ask

Each key takes either a single action or an object of glob patterns:

```json
{
  "permission": {
    "skill": { "*": "ask", "internal-*": "allow" },
    "rules": {
      "bash": { "*": "ask", "git *": "allow", "rm -rf *": "deny" },
      "edit": { "*": "allow", "*.env": "deny" },
      "read": { "*": "allow" },
      "task": { "*": "allow", "explorer": "allow" }
    }
  }
}
```

| Key | Pattern matches | Example |
|-----|-----------------|---------|
| `skill` | Skill name | `{"internal-*": "allow", "*": "ask"}` |
| `bash` | Command | `{"*": "ask", "git *": "allow"}` |
| `edit` | File path | `{"*": "deny", "src/**/*.go": "allow"}` |
| `read` | File path | `{"*": "allow", "*.env": "deny"}` |
| `task` | Subagent name | `{"*": "allow", "explorer": "allow"}` |
| `<cli tool name>` | Argument string of a [workspace CLI tool](cli-tools.md) | `{"*": "ask", "sql *": "allow"}` |

Non-interactive runs (`-p`, flows) auto-approve every `ask`; in the TUI, [auto-approve](tui.md#auto-approve) does the same per session. `deny` always holds.

## Environment variables

Provider credentials (`ANTHROPIC_API_KEY`, `VERTEXAI_PROJECT`, `AWS_REGION`, …) are listed in [providers.md](providers.md#credentials).

| Variable | Default | Purpose |
|----------|---------|---------|
| `LANGFUSE_PUBLIC_KEY` | | Langfuse public key ([guide](telemetry.md)) |
| `LANGFUSE_SECRET_KEY` | | Langfuse secret key |
| `LANGFUSE_BASE_URL` | `https://cloud.langfuse.com` | Langfuse host URL |
| `LANGFUSE_TRACING_ENVIRONMENT` | | Langfuse trace environment when `telemetry.langfuse.environment` is unset ([guide](telemetry.md#trace-environment)) |
| `OPENCODE_USER_ID` | | User ID for telemetry (overrides config) |
| `SHELL` | | Default shell |
| `OPENCODE_SESSION_PROVIDER_TYPE` | `sqlite` | Session storage backend (`sqlite` or `mysql`) |
| `OPENCODE_MYSQL_DSN` | | MySQL connection string |
| `OPENCODE_FILE_OP_TIMEOUT` | `180` | Timeout in seconds for glob/grep file operations |
| `OPENCODE_PROVIDER_STREAM_INACTIVITY_TIMEOUT` | `300` | Seconds to wait for next SSE event before treating stream as stalled |
| `OPENCODE_MAX_REPEAT_CALLS` | `3` | Max identical consecutive tool calls before loop detection triggers |
| `OPENCODE_SERVER_PASSWORD` | | HTTP Basic Auth password for `opencode serve` ([guide](server.md)) |
| `AGENTS_MEMORY_HOME` | `~/.agents/memory` | Project-memory store root for this launch; overrides `memory.directory` ([guide](memory.md)) |
| `OPENCODE_TOOL_ENV_DENY` | | Comma-separated extra variable names (or globs) hidden from every process OpenCode starts ([details](tools.md#tool-environment)) |
| `OPENCODE_CLI_TOOLS_TIMEOUT` / `_MAX_TIMEOUT` / `_MAX_OUTPUT_BYTES` | | Limits for workspace CLI tools that a manifest leaves unset ([guide](cli-tools.md)) |
| `OPENCODE_DEV_DEBUG` | `false` | Enable development debug logging |
| `OPENCODE_DISABLE_LSP_DOWNLOAD` | `false` | Disable automatic LSP binary downloads |
| `OPENCODE_DISABLE_CLAUDE_SKILLS` | `false` | Disable `.claude/skills/` discovery |
| `OPENCODE_DISABLE_CRON` | | Disable cron scheduling entirely ([guide](cron-and-heartbeat.md)) |
