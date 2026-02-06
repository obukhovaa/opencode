> [!NOTE]  
> Fork of now archived https://github.com/opencode-ai/opencode
> The focus is changed towards headless experience oriented towards autonomous agents.

# ⌬ OpenCode

## Overview

OpenCode is a Go-based CLI application that brings AI assistance to your terminal. It provides a TUI (Terminal User Interface) for interacting with various AI models to help with coding tasks, debugging, and more.

<p>For a quick video overview, check out
<a href="https://www.youtube.com/watch?v=P8luPmEa1QI"><img width="25" src="https://upload.wikimedia.org/wikipedia/commons/0/09/YouTube_full-color_icon_%282017%29.svg"> OpenCode + Gemini 2.5 Pro: BYE Claude Code! I'm SWITCHING To the FASTEST AI Coder!</a></p>

<a href="https://www.youtube.com/watch?v=P8luPmEa1QI"><img width="550" src="https://i3.ytimg.com/vi/P8luPmEa1QI/maxresdefault.jpg"></a><p>

## Features

- **Interactive TUI**: Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) for a smooth terminal experience
- **Non-interactive mode**: Could be running in headless mode to build autonomous agents 
- **Multiple AI Providers**: Support for OpenAI, Anthropic Claude, Google Gemini and AWS Bedrock 
- **Session Management**: Save and manage multiple conversation sessions
- **Tool Integration**: AI can execute commands, search files, and modify code
- **Agent Skills**: Define reusable instructions that agents can discover and load on-demand
- **Vim-like Editor**: Integrated editor with text input capabilities
- **Persistent Storage**: SQLite database for storing conversations and sessions
- **LSP Integration**: Language Server Protocol support for code intelligence
- **File Change Tracking**: Track and visualize file changes during sessions
- **External Editor Support**: Open your preferred editor for composing messages
- **Named Arguments for Custom Commands**: Create powerful custom commands with multiple named placeholders

## Installation

### Using the Install Script

```bash
# Install the latest version
curl -fsSL https://raw.githubusercontent.com/opencode-ai/opencode/refs/heads/main/install | bash

# Install a specific version
curl -fsSL https://raw.githubusercontent.com/opencode-ai/opencode/refs/heads/main/install | VERSION=0.1.0 bash
```

### Using Homebrew (macOS and Linux)

```bash
brew install opencode-ai/tap/opencode
```

### Using AUR (Arch Linux)

```bash
# Using yay
yay -S opencode-ai-bin

# Using paru
paru -S opencode-ai-bin
```

### Using Go

```bash
go install github.com/opencode-ai/opencode@latest
```

## Configuration

OpenCode looks for configuration in the following locations:

- `$HOME/.opencode.json`
- `$XDG_CONFIG_HOME/opencode/.opencode.json`
- `./.opencode.json` (local directory)

### Auto Compact Feature

OpenCode includes an auto compact feature that automatically summarizes your conversation when it approaches the model's context window limit. When enabled (default setting), this feature:

- Monitors token usage during your conversation
- Automatically triggers summarization when usage reaches 95% of the model's context window
- Creates a new session with the summary, allowing you to continue your work without losing context
- Helps prevent "out of context" errors that can occur with long conversations

You can enable or disable this feature in your configuration file:

```json
{
  "autoCompact": true // default is true
}
```

### Session Storage Providers

OpenCode supports multiple database backends for session storage, allowing you to choose between local SQLite (default) or remote MySQL database.

#### SQLite (Default)

SQLite provides zero-configuration, file-based storage ideal for individual developers:

```json
{
  "sessionProvider": {
    "type": "sqlite"
  }
}
```

Sessions are stored in the `data.directory` location (default: `~/.opencode/`).

#### MySQL (Team Collaboration)

MySQL enables centralized session storage for teams sharing session history across multiple machines:

**Using Environment Variable (Simplest):**

```bash
export OPENCODE_SESSION_PROVIDER_TYPE=mysql
export OPENCODE_MYSQL_DSN="user:password@tcp(localhost:3306)/opencode?parseTime=true"
```

**Using Configuration File:**

```json
{
  "sessionProvider": {
    "type": "mysql",
    "mysql": {
      "dsn": "user:password@tcp(localhost:3306)/opencode?parseTime=true"
    }
  }
}
```

Alternatively, you can specify individual connection parameters instead of DSN:

```json
{
  "sessionProvider": {
    "type": "mysql",
    "mysql": {
      "host": "localhost",
      "port": 3306,
      "database": "opencode",
      "username": "opencode_user",
      "password": "secure_password"
    }
  }
}
```

Optional connection pool settings (with defaults shown):

```json
{
  "sessionProvider": {
    "type": "mysql",
    "mysql": {
      "dsn": "...",
      "maxConnections": 10,
      "maxIdleConnections": 5,
      "connectionTimeout": 30
    }
  }
}
```

#### Project Scoping

Sessions are automatically scoped by **project** to ensure isolation:

- **Git Repository**: Uses the Git remote origin URL as project ID
  - Example: `https://github.com/opencode-ai/opencode.git` → `github.com/opencode-ai/opencode`
- **Directory Name**: Falls back to the base directory name if not a Git repository
  - Example: `/Users/john/projects/my-app` → `my-app`

This ensures that:
- Teams working on the same Git repository share sessions (when using MySQL)
- Different projects remain isolated even in shared databases
- You only see sessions relevant to your current project

#### MySQL Setup Guide

**Option 1: Using Docker Compose (Recommended for Testing)**

A `docker-compose.yml` file is provided for quick MySQL setup:

```bash
# Start MySQL container
docker-compose up -d

# Configure OpenCode to use MySQL
export OPENCODE_SESSION_PROVIDER_TYPE=mysql
export OPENCODE_MYSQL_DSN="opencode_user:secure_password@tcp(localhost:3306)/opencode?parseTime=true"

# Run OpenCode (migrations run automatically)
opencode
```

**Option 2: Manual MySQL Setup**

1. **Create Database:**
   ```sql
   CREATE DATABASE opencode CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
   CREATE USER 'opencode_user'@'%' IDENTIFIED BY 'secure_password';
   GRANT ALL PRIVILEGES ON opencode.* TO 'opencode_user'@'%';
   FLUSH PRIVILEGES;
   ```

2. **Configure OpenCode:**
   Set environment variables or update your `~/.opencode.json` configuration file.

3. **Run OpenCode:**
   Migrations will run automatically on first connection.

#### Troubleshooting

**Connection Errors:**
- Verify MySQL is running: `mysql -h localhost -u opencode_user -p`
- Check firewall rules allow connections to MySQL port
- Ensure credentials are correct in configuration

**Migration Errors:**
- Check MySQL user has sufficient privileges (CREATE, ALTER, INDEX)
- Verify database exists and is accessible
- Check logs for detailed error messages

### Agent Skills

OpenCode supports reusable instruction sets called "skills" that agents can discover and load on-demand. Skills provide specialized knowledge and step-by-step guidance for specific tasks.

#### Creating a Skill

Create a folder with your skill name and add a `SKILL.md` file:

```bash
mkdir -p .opencode/skills/git-release
```

Create `.opencode/skills/git-release/SKILL.md`:

```markdown
---
name: git-release
description: Create consistent releases and changelogs
license: MIT
---

## What I do

- Draft release notes from merged PRs
- Propose a version bump
- Provide a copy-pasteable `gh release create` command

## When to use me

Use this when you are preparing a tagged release.
Ask clarifying questions if the target versioning scheme is unclear.
```

#### Skill Discovery Locations

Skills are discovered from multiple locations:

**Project-level:**
- `.opencode/skills/<name>/SKILL.md`
- `.opencode/skill/<name>/SKILL.md`
- `.claude/skills/<name>/SKILL.md` (Claude-compatible)

**Global:**
- `~/.config/opencode/skills/<name>/SKILL.md`
- `~/.config/opencode/skill/<name>/SKILL.md`
- `~/.claude/skills/<name>/SKILL.md` (Claude-compatible)

**Custom paths:**
```json
{
  "skills": {
    "paths": [
      "~/my-skills",
      "./project-skills"
    ]
  }
}
```

#### Skill Naming Rules

Skill names must:
- Be 1-64 characters
- Use lowercase alphanumeric with single hyphens
- Match pattern: `^[a-z0-9]+(-[a-z0-9]+)*$`
- Match the directory name containing `SKILL.md`

Examples: `git-release`, `docker-build`, `pr-review`

#### Skill Permissions

Control which skills agents can access using pattern-based permissions:

```json
{
  "permission": {
    "skill": {
      "*": "allow",
      "internal-*": "deny",
      "experimental-*": "ask"
    }
  }
}
```

**Permission actions:**
- `allow`: Skill loads immediately
- `deny`: Skill hidden from agent, access rejected
- `ask`: User prompted for approval (default)

**Patterns support wildcards:**
- `internal-*` matches `internal-docs`, `internal-tools`, etc.
- `*-test` matches `unit-test`, `integration-test`, etc.
- `*` matches all skills

#### Agent-Specific Permissions

Override permissions for specific agents:

```json
{
  "permission": {
    "skill": {
      "internal-*": "deny"
    }
  },
  "agents": {
    "coder": {
      "model": "claude-3-5-sonnet-20241022",
      "permission": {
        "skill": {
          "internal-*": "allow"
        }
      }
    },
    "summarizer": {
      "model": "claude-3-5-haiku-20241022",
      "tools": {
        "skill": false
      }
    }
  }
}
```

**Built-in agents:**
- `coder`: Main coding agent
- `task`: Task planning agent
- `summarizer`: Session summarization agent
- `title`: Session title generation agent

#### How Agents Use Skills

Agents see available skills in the `skill` tool description:

```xml
<available_skills>
  <skill>
    <name>git-release</name>
    <description>Create consistent releases and changelogs</description>
  </skill>
</available_skills>
```

When an agent needs specialized guidance, it loads the skill:

```
Agent: I'll load the git-release skill to help with this.
[Calls skill tool with {"name": "git-release"}]
[Receives full skill instructions]
Agent: Based on the git-release skill, I'll...
```

#### Disabling Claude Skills

To disable automatic discovery of `.claude/skills/`:

```bash
export OPENCODE_DISABLE_CLAUDE_SKILLS=true
```

For more details on creating and managing skills, see the [Skills Guide](docs/skills.md).

### Environment Variables

You can configure OpenCode using environment variables:

| Environment Variable              | Purpose                                                                          |
| --------------------------------- | -------------------------------------------------------------------------------- |
| `ANTHROPIC_API_KEY`               | For Claude models                                                                |
| `OPENAI_API_KEY`                  | For OpenAI models                                                                |
| `GEMINI_API_KEY`                  | For Google Gemini models                                                         |
| `VERTEXAI_PROJECT`                | For Google Cloud VertexAI (Gemini, Anthropic)                                    |
| `VERTEXAI_LOCATION`               | For Google Cloud VertexAI (Gemini, Anthropic)                                    |
| `VERTEXAI_LOCATION_COUNT`         | For Google Cloud VertexAI (Anthropic) to use for token count endpoint            |
| `AWS_ACCESS_KEY_ID`               | For AWS Bedrock (Claude)                                                         |
| `AWS_SECRET_ACCESS_KEY`           | For AWS Bedrock (Claude)                                                         |
| `AWS_REGION`                      | For AWS Bedrock (Claude)                                                         |
| `LOCAL_ENDPOINT`                  | For self-hosted models                                                           |
| `SHELL`                           | Default shell to use (if not specified in config)                                |
| `OPENCODE_SESSION_PROVIDER_TYPE`  | Session storage provider: `sqlite` (default) or `mysql`                          |
| `OPENCODE_MYSQL_DSN`              | MySQL connection string (e.g., `user:pass@tcp(host:port)/dbname`)                |
| `OPENCODE_DISABLE_CLAUDE_SKILLS`  | Set to `true` to disable `.claude/skills/` discovery                             |
| -------------------------------------------------------------------------------------------------------------------- |

### Shell Configuration

OpenCode allows you to configure the shell used by the bash tool. By default, it uses the shell specified in the `SHELL` environment variable, or falls back to `/bin/bash` if not set.

You can override this in your configuration file:

```json
{
  "shell": {
    "path": "/bin/zsh",
    "args": ["-l"]
  }
}
```

This is useful if you want to use a different shell than your default system shell, or if you need to pass specific arguments to the shell.

### Configuration File Structure

```json
{
  "data": {
    "directory": ".opencode"
  },
  "providers": {
    "openai": {
      "apiKey": "your-api-key",
      "disabled": false
    },
    "anthropic": {
      "apiKey": "your-api-key",
      "disabled": false
    },
    "gemini": {
      "apiKey": "your-api-key",
      "disabled": false
    },
    "vertexai": {
      "project": "your-project-id",
      "location": "us-central1",
      "disabled": false
    }
  },
  "agents": {
    "coder": {
      "model": "claude-4-5-sonnet[1m]",
      "maxTokens": 5000
    },
    "task": {
      "model": "claude-4-5-sonnet[1m]",
      "maxTokens": 5000
    },
    "title": {
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
      "env": [],
      "args": []
    }
  },
  "lsp": {
    "go": {
      "disabled": false,
      "command": "gopls"
    }
  },
  "debug": false,
  "debugLSP": false,
  "autoCompact": true
}
```

## Supported AI Models

OpenCode supports a variety of AI models from different providers:

### OpenAI

- GPT-5
- GPT-4o family (gpt-4o, gpt-4o-mini)
- O1 family (o1, o1-pro, o1-mini)
- O3 Mini
- O4 Mini

### Anthropic

- Claude 4.5 Sonnet (200K and 1M context)
- Claude 3.5 Sonnet
- Claude 3.5 Haiku

### Google Gemini

- Gemini 3.0 Pro
- Gemini 3.0 Flash
- Gemini 2.0 Flash Thinking

### AWS Bedrock

- Claude 4.5 Sonnet

### Google Cloud VertexAI

- Gemini 3.0 Pro
- Gemini 3.0 Flash
- Claude 4.5 Sonnet (200K and 1M context)

### Local/Self-Hosted

- Any OpenAI-compatible API endpoint

## Usage

```bash
# Start OpenCode
opencode

# Start with debug logging
opencode -d

# Start with a specific working directory
opencode -c /path/to/project
```

## Non-interactive Prompt Mode

You can run OpenCode in non-interactive mode by passing a prompt directly as a command-line argument. This is useful for scripting, automation, or when you want a quick answer without launching the full TUI.

```bash
# Run a single prompt and print the AI's response to the terminal
opencode -p "Explain the use of context in Go"

# Get response in JSON format
opencode -p "Explain the use of context in Go" -f json

# Run without showing the spinner (useful for scripts)
opencode -p "Explain the use of context in Go" -q
```

In this mode, OpenCode will process your prompt, print the result to standard output, and then exit. All permissions are auto-approved for the session.

By default, a spinner animation is displayed while the model is processing your query. You can disable this spinner with the `-q` or `--quiet` flag, which is particularly useful when running OpenCode from scripts or automated workflows.

### Output Formats

OpenCode supports the following output formats in non-interactive mode:

| Format | Description                     |
| ------ | ------------------------------- |
| `text` | Plain text output (default)     |
| `json` | Output wrapped in a JSON object |

The output format is implemented as a strongly-typed `OutputFormat` in the codebase, ensuring type safety and validation when processing outputs.

## Command-line Flags

| Flag              | Short | Description                                         |
| ----------------- | ----- | --------------------------------------------------- |
| `--help`          | `-h`  | Display help information                            |
| `--debug`         | `-d`  | Enable debug mode                                   |
| `--cwd`           | `-c`  | Set current working directory                       |
| `--prompt`        | `-p`  | Run a single prompt in non-interactive mode         |
| `--output-format` | `-f`  | Output format for non-interactive mode (text, json) |
| `--quiet`         | `-q`  | Hide spinner in non-interactive mode                |

## Keyboard Shortcuts

### Global Shortcuts

| Shortcut | Action                                                  |
| -------- | ------------------------------------------------------- |
| `Ctrl+C` | Quit application                                        |
| `Ctrl+?` | Toggle help dialog                                      |
| `?`      | Toggle help dialog (when not in editing mode)           |
| `Ctrl+L` | View logs                                               |
| `Ctrl+A` | Switch session                                          |
| `Ctrl+P` | Prune session                                           | 
| `Ctrl+K` | Command dialog                                          |
| `Ctrl+O` | Toggle model selection dialog                           |
| `Esc`    | Close current overlay/dialog or return to previous mode |

### Chat Page Shortcuts

| Shortcut | Action                                  |
| -------- | --------------------------------------- |
| `Ctrl+N` | Create new session                      |
| `Ctrl+X` | Cancel current operation/generation     |
| `i`      | Focus editor (when not in writing mode) |
| `Esc`    | Exit writing mode and focus messages    |

### Editor Shortcuts

| Shortcut            | Action                                    |
| ------------------- | ----------------------------------------- |
| `Ctrl+S`            | Send message (when editor is focused)     |
| `Enter` or `Ctrl+S` | Send message (when editor is not focused) |
| `Ctrl+E`            | Open external editor                      |
| `Esc`               | Blur editor and focus messages            |

### Session Dialog Shortcuts

| Shortcut   | Action           |
| ---------- | ---------------- |
| `↑` or `k` | Previous session |
| `↓` or `j` | Next session     |
| `Enter`    | Select session   |
| `Esc`      | Close dialog     |

### Model Dialog Shortcuts

| Shortcut   | Action            |
| ---------- | ----------------- |
| `↑` or `k` | Move up           |
| `↓` or `j` | Move down         |
| `←` or `h` | Previous provider |
| `→` or `l` | Next provider     |
| `Esc`      | Close dialog      |

### Permission Dialog Shortcuts

| Shortcut                | Action                       |
| ----------------------- | ---------------------------- |
| `←` or `left`           | Switch options left          |
| `→` or `right` or `tab` | Switch options right         |
| `Enter` or `space`      | Confirm selection            |
| `a`                     | Allow permission             |
| `A`                     | Allow permission for session |
| `d`                     | Deny permission              |

### Logs Page Shortcuts

| Shortcut           | Action              |
| ------------------ | ------------------- |
| `Backspace` or `q` | Return to chat page |

## AI Assistant Tools

OpenCode's AI assistant has access to various tools to help with coding tasks:

### File and Code Tools

| Tool          | Description                 | Parameters                                                                               |
| ------------- | --------------------------- | ---------------------------------------------------------------------------------------- |
| `glob`        | Find files by pattern       | `pattern` (required), `path` (optional)                                                  |
| `grep`        | Search file contents        | `pattern` (required), `path` (optional), `include` (optional), `literal_text` (optional) |
| `ls`          | List directory contents     | `path` (optional), `ignore` (optional array of patterns)                                 |
| `view`        | View file contents          | `file_path` (required), `offset` (optional), `limit` (optional)                          |
| `view_image`  | View image files as base64  | `file_path` (required)                                                                   |
| `write`       | Write to files              | `file_path` (required), `content` (required)                                             |
| `edit`        | Edit files                  | Various parameters for file editing                                                      |
| `patch`       | Apply patches to files      | `file_path` (required), `diff` (required)                                                |
| `diagnostics` | Get diagnostics information | `file_path` (optional)                                                                   |

### Other Tools

| Tool          | Description                            | Parameters                                                                                |
| ------------- | -------------------------------------- | ----------------------------------------------------------------------------------------- |
| `bash`        | Execute shell commands                 | `command` (required), `timeout` (optional)                                                |
| `fetch`       | Fetch data from URLs                   | `url` (required), `format` (required), `timeout` (optional)                               |
| `sourcegraph` | Search code across public repositories | `query` (required), `count` (optional), `context_window` (optional), `timeout` (optional) |
| `agent`       | Run sub-tasks with the AI agent        | `prompt` (required)                                                                       |

## Architecture

OpenCode is built with a modular architecture:

- **cmd**: Command-line interface using Cobra
- **internal/app**: Core application services
- **internal/config**: Configuration management
- **internal/db**: Database operations and migrations
- **internal/llm**: LLM providers and tools integration
- **internal/tui**: Terminal UI components and layouts
- **internal/logging**: Logging infrastructure
- **internal/message**: Message handling
- **internal/session**: Session management
- **internal/lsp**: Language Server Protocol integration

## Custom Commands

OpenCode supports custom commands that can be created by users to quickly send predefined prompts to the AI assistant.

### Creating Custom Commands

Custom commands are predefined prompts stored as Markdown files in one of three locations:

1. **User Commands** (prefixed with `user:`):

   ```
   $XDG_CONFIG_HOME/opencode/commands/
   ```

   (typically `~/.config/opencode/commands/` on Linux/macOS)

   or

   ```
   $HOME/.opencode/commands/
   ```

2. **Project Commands** (prefixed with `project:`):

   ```
   <PROJECT DIR>/.opencode/commands/
   ```

Each `.md` file in these directories becomes a custom command. The file name (without extension) becomes the command ID.

For example, creating a file at `~/.config/opencode/commands/prime-context.md` with content:

```markdown
RUN git ls-files
READ README.md
```

This creates a command called `user:prime-context`.

### Command Arguments

OpenCode supports named arguments in custom commands using placeholders in the format `$NAME` (where NAME consists of uppercase letters, numbers, and underscores, and must start with a letter).

For example:

```markdown
# Fetch Context for Issue $ISSUE_NUMBER

RUN gh issue view $ISSUE_NUMBER --json title,body,comments
RUN git grep --author="$AUTHOR_NAME" -n .
RUN grep -R "$SEARCH_PATTERN" $DIRECTORY
```

When you run a command with arguments, OpenCode will prompt you to enter values for each unique placeholder. Named arguments provide several benefits:

- Clear identification of what each argument represents
- Ability to use the same argument multiple times
- Better organization for commands with multiple inputs

### Organizing Commands

You can organize commands in subdirectories:

```
~/.config/opencode/commands/git/commit.md
```

This creates a command with ID `user:git:commit`.

### Using Custom Commands

1. Press `Ctrl+K` to open the command dialog
2. Select your custom command (prefixed with either `user:` or `project:`)
3. Press Enter to execute the command

The content of the command file will be sent as a message to the AI assistant.

### Built-in Commands

OpenCode includes several built-in commands:

| Command            | Description                                                                                         |
| ------------------ | --------------------------------------------------------------------------------------------------- |
| Initialize Project | Creates or updates the OpenCode.md memory file with project-specific information                    |
| Compact Session    | Manually triggers the summarization of the current session, creating a new session with the summary |

## MCP (Model Context Protocol)

OpenCode implements the Model Context Protocol (MCP) to extend its capabilities through external tools. MCP provides a standardized way for the AI assistant to interact with external services and tools.

### MCP Features

- **External Tool Integration**: Connect to external tools and services via a standardized protocol
- **Tool Discovery**: Automatically discover available tools from MCP servers
- **Multiple Connection Types**:
  - **Stdio**: Communicate with tools via standard input/output
  - **SSE**: Communicate with tools via Server-Sent Events
- **Security**: Permission system for controlling access to MCP tools

### Configuring MCP Servers

MCP servers are defined in the configuration file under the `mcpServers` section:

```json
{
  "mcpServers": {
    "example": {
      "type": "stdio",
      "command": "path/to/mcp-server",
      "env": [],
      "args": []
    },
    "web-example": {
      "type": "sse",
      "url": "https://example.com/mcp",
      "headers": {
        "Authorization": "Bearer token"
      }
    }
  }
}
```

### MCP Tool Usage

Once configured, MCP tools are automatically available to the AI assistant alongside built-in tools. They follow the same permission model as other tools, requiring user approval before execution.

## LSP (Language Server Protocol)

OpenCode integrates with Language Server Protocol to provide code intelligence features across multiple programming languages.

### LSP Features

- **Multi-language Support**: Connect to language servers for different programming languages
- **Diagnostics**: Receive error checking and linting information
- **File Watching**: Automatically notify language servers of file changes

### Configuring LSP

Language servers are configured in the configuration file under the `lsp` section:

```json
{
  "lsp": {
    "go": {
      "disabled": false,
      "command": "gopls"
    },
    "typescript": {
      "disabled": false,
      "command": "typescript-language-server",
      "args": ["--stdio"]
    }
  }
}
```

### LSP Integration with AI

The AI assistant can access LSP features through the `diagnostics` tool, allowing it to:

- Check for errors in your code
- Suggest fixes based on diagnostics

While the LSP client implementation supports the full LSP protocol (including completions, hover, definition, etc.), currently only diagnostics are exposed to the AI assistant.

## Using a self-hosted model provider

OpenCode can also load and use models from a self-hosted (OpenAI-like) provider.
This is useful for developers who want to experiment with custom models.

### Configuring a self-hosted provider

You can use a self-hosted model by setting the `LOCAL_ENDPOINT` and `LOCAL_ENDPOINT_API_KEY` environment variable.
This will cause OpenCode to load and use the models from the specified endpoint. When it loads model it tries to inherit settings
from predefined ones if possible.

```bash
LOCAL_ENDPOINT=http://localhost:1235/v1
LOCAL_ENDPOINT_API_KEY=secret
```

### Using LiteLLM Proxy
It is possible to use LiteLLM as a passthrough proxy by providing `baseURL` and auth header to provider configuration:
```json
{
  "providers": {
    "vertexai": {
      "apiKey": "litellm-api-key",
      "disabled": false,
      "baseURL": "https://localhost/vertex_ai"
      "headers": {
        "x-litellm-api-key": "litellm-api-key"
      }
    }
  }
}
```

### Configuring a self-hosted model

You can also configure a self-hosted model in the configuration file under the `agents` section:

```json
{
  "agents": {
    "coder": {
      "model": "local.granite-3.3-2b-instruct@q8_0",
      "reasoningEffort": "high"
    }
  }
}
```

## Development

### Prerequisites

- Go 1.24.0 or higher

### Building from Source

```bash
# Clone the repository
git clone https://github.com/opencode-ai/opencode.git
cd opencode

# Build
go build -o opencode

# Run
./opencode
```

## Acknowledgments

OpenCode gratefully acknowledges the contributions and support from these key individuals:

- [@isaacphi](https://github.com/isaacphi) - For the [mcp-language-server](https://github.com/isaacphi/mcp-language-server) project which provided the foundation for our LSP client implementation
- [@adamdottv](https://github.com/adamdottv) - For the design direction and UI/UX architecture

Special thanks to the broader open source community whose tools and libraries have made this project possible.

## License

OpenCode is licensed under the MIT License. See the [LICENSE](LICENSE) file for details.

## Contributing

Contributions are welcome! Here's how you can contribute:

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

Please make sure to update tests as appropriate and follow the existing code style.
