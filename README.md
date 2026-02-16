> [!NOTE]
> Fork of now archived https://github.com/opencode-ai/opencode
> The focus is changed towards headless experience oriented towards autonomous agents.

# ⌬ OpenCode

OpenCode is a CLI tool that brings AI assistance to your terminal. It provides both a TUI (Terminal User Interface) and a headless non-interactive mode for scripting and autonomous agents.

## Features

- **Interactive TUI** built with [Bubble Tea](https://github.com/charmbracelet/bubbletea)
- **Non-interactive mode** for headless automation and autonomous agents
- **Multiple AI providers**: OpenAI, Anthropic, Google Gemini, AWS Bedrock, VertexAI, and self-hosted
- **Tool integration**: file operations, shell commands, code search, LSP code intelligence
- **MCP support**: extend capabilities via Model Context Protocol servers
- **Agent skills**: reusable instruction sets loaded on-demand ([guide](docs/skills.md))
- **Custom commands**: predefined prompts with named arguments ([guide](docs/custom-commands.md))
- **Session management** with SQLite or MySQL storage ([guide](docs/session-providers.md))
- **LSP integration** with auto-install for 30+ language servers ([guide](docs/lsp.md))
- **File change tracking** during sessions

## Installation

### Install Script

```bash
curl -fsSL https://raw.githubusercontent.com/opencode-ai/opencode/refs/heads/main/install | bash

# Specific version
curl -fsSL https://raw.githubusercontent.com/opencode-ai/opencode/refs/heads/main/install | VERSION=0.1.0 bash
```

### Homebrew

```bash
brew install opencode-ai/tap/opencode
```

### AUR (Arch Linux)

```bash
yay -S opencode-ai-bin
```

### Go

```bash
go install github.com/obukhovaa/opencode@latest
```

## Usage

```bash
opencode                        # Start TUI
opencode -d                     # Debug mode
opencode -c /path/to/project    # Set working directory
opencode -a hivemind            # Start with a specific agent
opencode -s <session-id>        # Resume or create a session
opencode -s <session-id> -D     # Delete session and start fresh
```

### Non-Interactive Mode

```bash
opencode -p "Explain context in Go"           # Single prompt
opencode -p "Explain context in Go" -f json   # JSON output
opencode -p "Explain context in Go" -q        # Quiet (no spinner)
```

All permissions are auto-approved in non-interactive mode.

### Command-Line Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--help` | `-h` | Display help |
| `--debug` | `-d` | Enable debug mode |
| `--cwd` | `-c` | Set working directory |
| `--prompt` | `-p` | Non-interactive single prompt |
| `--agent` | `-a` | Agent ID to use (e.g. `coder`, `hivemind`) |
| `--session` | `-s` | Session ID to resume or create |
| `--delete` | `-D` | Delete the session specified by `--session` before starting |
| `--output-format` | `-f` | Output format: `text` (default), `json` |
| `--quiet` | `-q` | Hide spinner in non-interactive mode |

## Configuration

OpenCode looks for `.opencode.json` in:

1. `./.opencode.json` (project directory)
2. `$XDG_CONFIG_HOME/opencode/.opencode.json`
3. `$HOME/.opencode.json`

### Full Config Example

```json
{
  "data": {
    "directory": ".opencode"
  },
  "providers": {
    "openai": { "apiKey": "..." },
    "anthropic": { "apiKey": "..." },
    "gemini": { "apiKey": "..." },
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
      "edit": { "*": "allow" }
    }
  },
  "autoCompact": true,
  "debug": false
}
```

### Agents

Each built-in agent can be customized:

| Agent | Mode | Purpose |
|-------|------|---------|
| `coder` | agent | Main coding agent (all tools) |
| `hivemind` | agent | Supervisory agent for coordinating subagents |
| `explorer` | subagent | Fast codebase exploration (read-only tools) |
| `workhorse` | subagent | Autonomous coding subagent (all tools) |
| `summarizer` | subagent | Session summarization |
| `descriptor` | subagent | Session title generation |

**Agent fields:**

| Field | Description |
|-------|-------------|
| `model` | Model ID to use |
| `maxTokens` | Maximum response tokens |
| `reasoningEffort` | `low`, `medium`, `high` (default), `max` |
| `mode` | `agent` (primary, switchable via tab) or `subagent` (invoked via task tool) |
| `name` | Display name for the agent |
| `description` | Short description of agent's purpose |
| `permission` | Agent-specific permission overrides (supports granular glob patterns) |
| `tools` | Enable/disable specific tools (e.g., `{"skill": false}`) |
| `color` | Badge color for subagent indication in TUI |

#### Custom Agents via Markdown

Define custom agents as markdown files with YAML frontmatter. Discovery locations (merge priority, lowest to highest):

1. `~/.config/opencode/agents/*.md` — Global agents
2. `~/.agents/types/*.md` — Global agents
3. `.opencode/agents/*.md` — Project agents
4. `.agents/types/*.md` — Project agents
5. `.opencode.json` config — Highest priority

Example `.opencode/agents/reviewer.md`:

```markdown
---
name: Code Reviewer
description: Reviews code for quality and best practices
mode: subagent
model: vertexai.claude-opus-4-6
color: info
tools:
  bash: false
  write: false
---

You are a code review specialist...
```

The file basename (without `.md`) becomes the agent ID. Custom agents default to `subagent` mode.


### Auto Compact

When enabled (default), automatically summarizes conversations approaching the context window limit (95%) and continues in a new session.

```json
{ "autoCompact": true }
```

### Shell

Override the default shell (falls back to `$SHELL` or `/bin/bash`):

```json
{
  "shell": {
    "path": "/bin/zsh",
    "args": ["-l"]
  }
}
```

### MCP Servers

```json
{
  "mcpServers": {
    "stdio-example": {
      "type": "stdio",
      "command": "path/to/server",
      "env": [],
      "args": []
    },
    "sse-example": {
      "type": "sse",
      "url": "https://example.org/mcp",
      "headers": { "Authorization": "Bearer token" }
    },
    "http-example": {
      "type": "http",
      "url": "https://example.com/mcp",
      "headers": { "Authorization": "Bearer token" }
    }
  }
}
```

### LSP

OpenCode auto-detects and starts LSP servers for your project's languages. Over 30 servers are built-in with auto-install support. See the [full LSP guide](docs/lsp.md) for details.

```json
{
  "lsp": {
    "gopls": {
      "env": { "GOFLAGS": "-mod=vendor" },
      "initialization": { "codelenses": { "test": true } }
    },
    "typescript": { "disabled": true },
    "my-lsp": {
      "command": "my-lsp-server",
      "args": ["--stdio"],
      "extensions": [".custom"]
    }
  },
  "disableLSPDownload": false
}
```

Disable auto-download of LSP binaries via config (`"disableLSPDownload": true`) or env var (`OPENCODE_DISABLE_LSP_DOWNLOAD=true`).

### Self-Hosted Models

**Local endpoint:**

```bash
export LOCAL_ENDPOINT=http://localhost:1235/v1
export LOCAL_ENDPOINT_API_KEY=secret
```

```json
{
  "agents": {
    "coder": {
      "model": "local.granite-3.3-2b-instruct@q8_0"
    }
  }
}
```

**LiteLLM proxy:**

```json
{
  "providers": {
    "vertexai": {
      "apiKey": "litellm-api-key",
      "baseURL": "https://localhost/vertex_ai",
      "headers": {
        "x-litellm-api-key": "litellm-api-key"
      }
    }
  }
}
```

### Environment Variables

| Variable | Purpose |
|----------|---------|
| `ANTHROPIC_API_KEY` | Anthropic Claude models |
| `OPENAI_API_KEY` | OpenAI models |
| `GEMINI_API_KEY` | Google Gemini models |
| `VERTEXAI_PROJECT` | Google Cloud VertexAI |
| `VERTEXAI_LOCATION` | Google Cloud VertexAI |
| `VERTEXAI_LOCATION_COUNT` | VertexAI token count endpoint |
| `AWS_ACCESS_KEY_ID` | AWS Bedrock |
| `AWS_SECRET_ACCESS_KEY` | AWS Bedrock |
| `AWS_REGION` | AWS Bedrock |
| `LOCAL_ENDPOINT` | Self-hosted model endpoint |
| `LOCAL_ENDPOINT_API_KEY` | Self-hosted model API key |
| `SHELL` | Default shell |
| `OPENCODE_SESSION_PROVIDER_TYPE` | `sqlite` (default) or `mysql` |
| `OPENCODE_MYSQL_DSN` | MySQL connection string |
| `OPENCODE_DISABLE_CLAUDE_SKILLS` | Disable `.claude/skills/` discovery |
| `OPENCODE_DISABLE_LSP_DOWNLOAD` | Disable auto-install of LSP servers |

## Supported Models

| Provider | Models |
|----------|--------|
| **OpenAI** | GPT-5, O3 Mini, O4 Mini |
| **Anthropic** | Claude 4.5 Sonnet (1M), Claude 4.6 Opus (1M) |
| **Google Gemini** | Gemini 3.0 Pro, Gemini 3.0 Flash |
| **AWS Bedrock** | Claude 4.5 Sonnet |
| **VertexAI** | Gemini 3.0 Pro, Gemini 3.0 Flash, Claude 4.5 Sonnet (1M), Claude 4.6 Opus (1M) |
| **Local** | Any OpenAI-compatible API |

## Tools

### File & Code

| Tool | Description |
|------|-------------|
| `glob` | Find files by pattern |
| `grep` | Search file contents |
| `ls` | List directory contents |
| `view` | View file contents |
| `view_image` | View image files as base64 |
| `write` | Write to files |
| `edit` | Edit files |
| `multiedit` | Multiple edits in one file |
| `patch` | Apply patches to files |
| `lsp` | Code intelligence (go-to-definition, references, hover, etc.) |
| `delete` | Delete file or directory |

### System & Search

| Tool | Description |
|------|-------------|
| `bash` | Execute shell commands |
| `fetch` | Fetch data from URLs |
| `sourcegraph` | Search public repositories |
| `task` | Run sub-tasks with a subagent (supports `subagent_type` and `task_id` for resumption) |
| `skill` | Load agent skills on-demand |

## Keyboard Shortcuts

### Global

| Shortcut | Action |
|----------|--------|
| `Ctrl+C` | Quit |
| `Ctrl+?` / `?` | Toggle help |
| `Ctrl+L` | View logs |
| `Ctrl+A` | Switch session |
| `Ctrl+N` | New session |
| `Ctrl+P` | Prune session |
| `Ctrl+K` | Command dialog |
| `Ctrl+O` | Model selection |
| `Ctrl+X` | Cancel generation |
| `Tab` | Switch primary agent |
| `Esc` | Close dialog / exit mode |

### Editor

| Shortcut | Action |
|----------|--------|
| `i` | Focus editor |
| `Ctrl+S` / `Enter` | Send message |
| `Ctrl+E` | Open external editor |
| `Esc` | Blur editor |

### Dialogs

| Shortcut | Action |
|----------|--------|
| `↑`/`k`, `↓`/`j` | Navigate items |
| `←`/`h`, `→`/`l` | Switch tabs/providers |
| `Enter` | Select |
| `a` / `A` / `d` | Allow / Allow for session / Deny (permissions) |

## Extended Documentation

| Topic | Link |
|-------|------|
| Skills | [docs/skills.md](docs/skills.md) |
| Custom Commands | [docs/custom-commands.md](docs/custom-commands.md) |
| Session Providers | [docs/session-providers.md](docs/session-providers.md) |
| LSP Servers | [docs/lsp.md](docs/lsp.md) |

## Development

### Prerequisites

- Go 1.24.0 or higher

### Building from Source

```bash
git clone https://github.com/obukhovaa/opencode.git
cd opencode
make build
```

### Release
```bash
make release SCOPE=patch
# or
make release SCOPE=minor
```

## Acknowledgments

- [@isaacphi](https://github.com/isaacphi) — [mcp-language-server](https://github.com/isaacphi/mcp-language-server), foundation for the LSP client
- [@adamdottv](https://github.com/adamdottv) — Design direction and UI/UX architecture

## License

MIT — see [LICENSE](LICENSE).

## Contributing

1. Fork the repository
2. Create a feature branch
3. Commit your changes
4. Open a Pull Request
