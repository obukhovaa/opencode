# Proposal

## Why

Every process opencode starts inherits opencode's full environment. In a server deployment
that environment carries the engine's own credentials: `OPENCODE_SERVER_PASSWORD` (the
HTTP API's Basic password), `OPENCODE_BRIDGE_REGISTRAR_PASSWORD` and the session store's
`OPENCODE_MYSQL_DSN`, often alongside the same values under the deployment's own names.
An agent with the `bash` tool can run `env` and read them, and with the API password it can
list, read and prompt every session on any opencode server that shares it. The model needs
none of these values to do its work, so it should not be able to see them.

## What Changes

- Introduce a **tool environment deny list**: environment variable names (exact or glob) that
  opencode removes from the environment of every child process it starts. opencode itself
  keeps reading them, so nothing it needs is lost.
- The list always contains the engine's own secrets: `OPENCODE_SERVER_PASSWORD`,
  `OPENCODE_BRIDGE_REGISTRAR_PASSWORD`, `OPENCODE_MYSQL_DSN`.
- Operators extend it with a new top-level `.opencode.json` key `toolEnv.deny` (list of
  names or globs) and with the environment variable `OPENCODE_TOOL_ENV_DENY`
  (comma-separated). The three sources are unioned; nothing can remove a built-in entry.
- Apply it to every spawn site: the persistent shell behind the `bash` tool and the TUI `!`
  mode, `bash` with `run_in_background`, the `monitor` tool, workspace CLI tools (including
  `env.inherit: true`, `env.pass` and `${env.NAME}` expansion in `env.set`), hooks, stdio
  MCP servers and LSP servers and installers. Values an operator writes explicitly into a
  child's own config (an MCP server's `env`, an LSP server's `env`, a literal in a CLI
  tool's `env.set`) are still passed: that is a deliberate per-child grant.
- On Linux, when the deny list matched at least one variable in opencode's own environment,
  opencode marks itself non-dumpable so a same-user child cannot read the values back from
  `/proc/<opencode pid>/environ`.
- **BREAKING** (narrow): a hook, MCP server or tool script that read
  `OPENCODE_SERVER_PASSWORD`, `OPENCODE_BRIDGE_REGISTRAR_PASSWORD` or `OPENCODE_MYSQL_DSN`
  from its inherited environment no longer sees it. Pass it through the child's own `env`
  config instead.

## Capabilities

### New Capabilities
- `tool-process-environment`: which environment variables opencode passes to the processes
  it starts, how the deny list is assembled and configured, and how opencode keeps its own
  environment from being read back by those processes.

### Modified Capabilities
- `shell-process-isolation`: the "caller-supplied environment is preserved" guarantee now
  excludes names on the tool environment deny list.
- `workspace-cli-tools`: the parent environment a CLI tool inherits, passes or expands from
  is the filtered tool environment.
- `hook-runtime`: hooks inherit the filtered tool environment rather than the agent's full
  environment.

## Impact

- New package for the deny list and filtered environment; call sites in
  `internal/llm/tools/shell`, `internal/llm/tools` (background bash, monitor),
  `internal/clitool`, `internal/hooks`, `internal/llm/agent` (stdio MCP), `internal/lsp`,
  `internal/tui/components/chat` (TUI shell), plus a Linux-only startup hook.
- `Config` gains `toolEnv` (`ToolEnvConfig{Deny []string}`); `cmd/schema/main.go` and
  `opencode-schema.json` are regenerated.
- Docs: `README.md` (config + environment variables), `docs/hooks.md`, `docs/cli-tools.md`.
- Deployments that export the session DSN or API password under their own names (for
  example a `MYSQL_DSN` used to template `.opencode.json`) add those names via
  `OPENCODE_TOOL_ENV_DENY` or `toolEnv.deny`.
