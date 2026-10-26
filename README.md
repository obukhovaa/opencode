> [!NOTE]
> Fork of now archived https://github.com/opencode-ai/opencode
> The focus is changed towards headless experience oriented towards autonomous agents.

# ⌬ OpenCode

OpenCode brings AI coding agents to your terminal. Run it as an interactive TUI, as a headless CLI for scripts and autonomous agents, as an HTTP API server, or as an ACP server for editors.

## Features

- **Four ways to run**: interactive [TUI](docs/tui.md), headless `-p` mode, [HTTP REST API with SSE](docs/server.md), and [ACP](docs/server.md#acp-mode) for Zed, JetBrains and AionUI
- **Flows**: deterministic multi-step agent workflows defined in YAML ([guide](docs/flows.md))
- **Agents and subagents**: built-in and custom agents, defined in JSON or markdown ([guide](docs/agents.md))
- **Chat bridge**: Telegram, Slack and Mattermost adapters with interactive questions and multi-reviewer fan-out ([guide](docs/bridge.md))
- **Many providers**: Anthropic, OpenAI, Gemini, AWS Bedrock, VertexAI, YandexCloud, Kimi and self-hosted ([guide](docs/providers.md))
- **Tools**: files, shell, search, LSP, web fetch ([guide](docs/tools.md)), [MCP servers](docs/mcp.md), and host CLIs wrapped as [workspace tools](docs/cli-tools.md)
- **Tool control**: allow-lists, granular permissions ([guide](docs/tool-permissions.md)) and on-demand [deferred tools](docs/deferred-tools.md)
- **Structured output**: enforce an agent's final answer with a JSON schema ([guide](docs/structured-output.md))
- **Skills, commands and hooks**: [skills](docs/skills.md), [custom commands](docs/custom-commands.md) and Claude-Code-compatible [hooks](docs/hooks.md)
- **Long-running sessions**: [auto-compaction](docs/compaction.md), [background tasks](docs/background-tasks.md), [cron jobs](docs/cron-and-heartbeat.md) and [project memory](docs/memory.md)
- **Observability**: Langfuse tracing of LLM calls, tools, tokens and cost ([guide](docs/telemetry.md))
- **Storage**: sessions in SQLite or MySQL ([guide](docs/session-providers.md))

## Installation

```bash
# Install script (add VERSION=0.3.0 before bash for a specific version)
curl -fsSL https://raw.githubusercontent.com/obukhovaa/opencode/refs/heads/main/install | bash

# Homebrew
brew install obukhovaa/tap/opencode

# Go
go install github.com/obukhovaa/opencode@latest
```

## Usage

```bash
opencode                                     # Start the TUI
opencode -a hivemind                         # Start with a specific agent
opencode -s <session-id>                     # Resume or create a session

opencode -p "Explain context in Go"          # Single prompt, non-interactive
opencode -p "Refactor this module" -f json -q -t 5m

opencode -p "Review it" -F review-code -A hash=93706ee   # Run a flow with args

opencode serve --port 8080                   # HTTP API server (docs/server.md)
opencode acp                                 # ACP server over stdio
```

All permissions are auto-approved in non-interactive mode.

### Command-line flags

| Flag | Short | Description |
|------|-------|-------------|
| `--help` | `-h` | Display help |
| `--version` | `-v` | Print the version |
| `--debug` | `-d` | Enable debug mode |
| `--cwd` | `-c` | Set working directory |
| `--prompt` | `-p` | Non-interactive single prompt |
| `--agent` | `-a` | Agent ID to use (e.g. `coder`, `hivemind`) |
| `--session` | `-s` | Session ID to resume or create |
| `--delete` | `-D` | Delete the session specified by `--session` before starting |
| `--output-format` | `-f` | Output format: `text` (default), `json` |
| `--quiet` | `-q` | Hide spinner in non-interactive mode |
| `--timeout` | `-t` | Timeout for non-interactive mode (e.g. `10s`, `30m`, `1h`) |
| `--max-turns` | | Maximum tool-use turns per request (overrides agent config) |
| `--auto-approve` | | Start TUI with auto-approve enabled (skip permission dialogs) |
| `--flow` | `-F` | Flow ID to execute ([guide](docs/flows.md)) |
| `--arg` | `-A` | Flow argument as `key=value` (repeatable) |
| `--args-file` | | JSON file with flow arguments |
| `--project-id` | `-P` | Custom project ID to group sessions (overrides detected Git/basename) |

`opencode serve`, `opencode acp` and `opencode tools` have their own flags: see `--help`, [docs/server.md](docs/server.md) and [docs/cli-tools.md](docs/cli-tools.md).

## Configuration

OpenCode reads `.opencode.json` from the project directory, then `$XDG_CONFIG_HOME/opencode/`, then `$HOME`. A minimal config:

```json
{
  "providers": {
    "anthropic": { "apiKey": "..." }
  },
  "agents": {
    "coder": { "model": "claude-4-5-sonnet[1m]", "maxTokens": 5000 }
  }
}
```

See [docs/configuration.md](docs/configuration.md) for a full example, permissions and environment variables. Your editor can validate the file against [`opencode-schema.json`](opencode-schema.json).

## Documentation

| Topic | Guide |
|-------|-------|
| **Getting set up** | |
| Configuration, permissions, environment variables | [docs/configuration.md](docs/configuration.md) |
| `${VAR}` environment references in config values | [docs/config-env.md](docs/config-env.md) |
| Providers and supported models | [docs/providers.md](docs/providers.md) |
| TUI keyboard shortcuts and auto-approve | [docs/tui.md](docs/tui.md) |
| **Agents** | |
| Built-in and custom agents | [docs/agents.md](docs/agents.md) |
| Context files (`AGENTS.md`, `CLAUDE.md`) | [docs/context.md](docs/context.md) |
| Skills | [docs/skills.md](docs/skills.md) |
| Custom commands | [docs/custom-commands.md](docs/custom-commands.md) |
| Compaction and self-compaction | [docs/compaction.md](docs/compaction.md) |
| Project memory | [docs/memory.md](docs/memory.md) |
| **Tools** | |
| Built-in tools, shell, tool environment | [docs/tools.md](docs/tools.md) |
| Tool permissions (`tools` vs `allowTools`) | [docs/tool-permissions.md](docs/tool-permissions.md) |
| MCP servers | [docs/mcp.md](docs/mcp.md) |
| Workspace CLI tools | [docs/cli-tools.md](docs/cli-tools.md) |
| Deferred tools and tool search | [docs/deferred-tools.md](docs/deferred-tools.md) |
| Structured output | [docs/structured-output.md](docs/structured-output.md) |
| LSP servers | [docs/lsp.md](docs/lsp.md) |
| Hooks (Claude-Code-compatible) | [docs/hooks.md](docs/hooks.md) |
| **Automation** | |
| Flows | [docs/flows.md](docs/flows.md) |
| Background tasks | [docs/background-tasks.md](docs/background-tasks.md) |
| Cron and heartbeat | [docs/cron-and-heartbeat.md](docs/cron-and-heartbeat.md) |
| **Running as a service** | |
| HTTP server and ACP mode | [docs/server.md](docs/server.md) |
| Chat bridge (Slack, Telegram, Mattermost) | [docs/bridge.md](docs/bridge.md) |
| Session storage (SQLite, MySQL) | [docs/session-providers.md](docs/session-providers.md) |
| Telemetry and Langfuse | [docs/telemetry.md](docs/telemetry.md) |
| **Contributing** | |
| Building, testing, Docker, releases | [docs/development.md](docs/development.md) |

## Development

```bash
git clone https://github.com/obukhovaa/opencode.git
cd opencode
make build    # build
make test     # lint and run all tests
```

Docker images, end-to-end tests and releases are covered in [docs/development.md](docs/development.md).

## Contributing

1. Fork the repository
2. Create a feature branch
3. Commit your changes
4. Open a Pull Request

## Acknowledgments

- [@isaacphi](https://github.com/isaacphi) — [mcp-language-server](https://github.com/isaacphi/mcp-language-server), foundation for the LSP client
- [@adamdottv](https://github.com/adamdottv) — Design direction and UI/UX architecture
- [@kujtimiihoxha](https://github.com/kujtimiihoxha) – Original OpenCode implementation

## License

MIT — see [LICENSE](LICENSE).
