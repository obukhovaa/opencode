# Built-in Tools

The tools every agent can be given, plus the config that shapes how they run: the shell, the environment child processes inherit, and the `webfetch` output cap. Which agent gets which tool is governed by `tools` / `allowTools` ([tool permissions](tool-permissions.md)) and the `permission` rules ([configuration](configuration.md#permissions)).

Beyond the built-ins, agents can call [MCP servers](mcp.md) and [workspace CLI tools](cli-tools.md), and keep rarely used tools out of context with [deferred tools](deferred-tools.md).

## File & code

| Tool | Description |
|------|-------------|
| `glob` | Find files by pattern |
| `grep` | Search file contents |
| `ls` | List directory contents |
| `read` | Read file contents |
| `view_image` | View image files as base64 |
| `write` | Write to files |
| `edit` | Edit files |
| `multiedit` | Multiple edits in one file |
| `patch` | Apply patches to files |
| `lsp` | Code intelligence (go-to-definition, references, hover, etc.; [guide](lsp.md)) |
| `delete` | Delete file or directory |

## System & search

| Tool | Description |
|------|-------------|
| `bash` | Execute shell commands ([background mode](background-tasks.md)) |
| `webfetch` | Fetch data from URLs (large pages are capped and saved to a temp file — see [Web fetch output cap](#web-fetch-output-cap)) |
| `websearch` | Search internet via configured WebSearch providers |
| `sourcegraph` | Search public repositories |
| `task` | Run sub-tasks with a subagent (supports `subagent_type` and `task_id` for resumption) |
| `skill` | Load agent skills on-demand (supports `args` for argument substitution and shell expansion; [guide](skills.md)) |
| `struct_output` | Emit structured JSON conforming to a user-supplied schema ([guide](structured-output.md)) |
| `toolsearch` | Discover and load deferred tools on demand (auto-registered only when an agent declares `deferredTools`, [guide](deferred-tools.md)) |
| `todowrite` | Create and maintain a structured task list for multi-step sessions (progress tracking for external UIs) |
| `croncreate` / `crondelete` / `cronlist` | Schedule, cancel, and list cron jobs that fire prompts via subagents ([guide](cron-and-heartbeat.md)) |
| `compact_session` | Request compaction of the agent's own session after the current turn (registered only with `selfCompact.enabled`, see [self-compaction](compaction.md#self-compaction)) |

## Shell

Override the default shell (falls back to `$SHELL` or `/bin/bash`):

```json
{
  "shell": {
    "path": "/bin/zsh",
    "args": ["-l"],
    "interactive": ["my-cli", "aws-vault"]
  }
}
```

On Unix the shell runs in its own session with no controlling terminal, so nothing
it runs can read from or write to the terminal OpenCode is drawing on. A command
that needs a terminal — `sudo`, `ssh`, an editor — therefore fails immediately
rather than prompting invisibly.

In the TUI's shell mode those commands are detected and handed the real terminal
instead: OpenCode steps aside for the duration of the run, you answer the prompt,
and the TUI comes back. `interactive` extends the built-in list (`sudo`, `ssh`,
`vim`, `less`, `psql`, `docker exec -it`, …) with your own programs; prefixing any
command with `!!` forces the same handoff without configuring it.

> **Behavior change.** Before terminal isolation, such a command prompted on
> OpenCode's own terminal — corrupting the TUI, racing it for your keystrokes, and
> hanging until the tool timeout. It now fails immediately with the program's own
> diagnostic (`sudo: no tty present …`). If you hit that from the agent's `bash`
> tool, run the command yourself; from the TUI, re-run it with `!!`.

Commands are evaluated *in* the shell, which is what makes `cd` persist between
them. The same property means a command containing `exit` ends the session: the
exit status is reported as the command's own, a replacement shell starts in the
same directory, and exported variables and shell functions are lost.

## Tool environment

Every process OpenCode starts (the `bash` tool and its background mode, `monitor`,
workspace CLI tools, hooks, stdio MCP servers, LSP servers and their helpers,
search and Git helpers, the TUI's shell mode and external editor)
inherits OpenCode's environment **minus a deny list**, so `env` run by the agent
cannot print OpenCode's own credentials. OpenCode itself still reads them.

`OPENCODE_SERVER_PASSWORD`, `OPENCODE_BRIDGE_REGISTRAR_PASSWORD` and
`OPENCODE_MYSQL_DSN` are always denied. Add your own names, or shell-style globs
over the whole name, in config or in the comma-separated `OPENCODE_TOOL_ENV_DENY`
(the two are combined; neither can remove a built-in):

```json
{
  "toolEnv": {
    "deny": ["MYSQL_DSN", "AWS_SECRET_*"]
  }
}
```

Names are case-sensitive. A value written into one child's own config (an MCP or
LSP server's `env`, a literal in a CLI tool's `env.set`) is still passed to that
child; `env.pass` and `${env.NAME}` cannot reach a denied name.

On Linux, when OpenCode starts with any denied variable set, it also marks its
process non-dumpable, so a child running as the same user cannot read the values
back from `/proc/<pid>/environ`. That also disables core dumps and same-user
debugger attach for the OpenCode process; with no denied variable set, nothing
changes.

Launch OpenCode with `exec` when a wrapper shell holds the same secrets: this
protects OpenCode's process, not a launcher left running as its parent. Secrets
rendered into readable configuration files also remain readable by that user.

> **Behavior change.** A hook or MCP server that read `OPENCODE_SERVER_PASSWORD` or
> `OPENCODE_MYSQL_DSN` from its inherited environment no longer sees it. Give an MCP
> server the value in its own `env` instead.

## Web fetch output cap

```json
{
  "webFetch": { "maxOutputBytes": 51200 }
}
```

`webfetch` converts a page (HTML → markdown by default) and returns it to the model, and a single documentation page routinely converts to hundreds of KB — enough for two or three fetches to fill a context window, and every one of them is re-sent on each later turn.

`maxOutputBytes` caps what a single fetch keeps in context (default `51200`, i.e. 50KB — the same number as the bash and MCP caps). The cap is measured after conversion, on the text that actually enters the context. Beyond it, the full converted page is written to a temp file and replaced with a head+tail preview whose header names the file, so the agent searches it with `grep` or reads it in pages with the `read` tool's `offset`/`limit` instead of carrying the page or re-fetching the URL. The `read` tool declines only a whole-file read (no `offset`/`limit`) of a file over 250KB; a paged read works at any size and returns at most 250KB per call.

Set a higher value to keep more inline, or a negative value to disable the cap entirely (unbounded — a few fetches can then overflow the context). Responses at or below the cap are returned unchanged and no file is written.

A response body over the 5MB read limit is truncated before conversion, and the reply says so rather than presenting a torn document as a complete one.
