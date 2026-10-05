# workspace-cli-tools Specification

## Purpose
Lets a workspace declare a host CLI as a first-class opencode tool through a manifest file: discovery and validation of manifests, argv-only execution with argument, environment, working-directory, time and output confinement, an exploration (`argv`) and a lock-down (`structured`) input mode, agent gating and permission layering, deferral, an audit listing, and a stdio MCP bridge that serves the same manifests to Claude Code and other MCP clients.
## Requirements
### Requirement: Manifests are discovered from workspace and global tool directories

The system SHALL load CLI tool manifests from files named `<name>.yaml`, `<name>.yml` or `<name>.json` found by a non-recursive scan of, in precedence order: `.opencode/tools` and `.agents/tools` in the working directory and each of its ancestors up to the git worktree root; `~/.config/opencode/tools` and `~/.agents/tools`; each directory listed in the top-level `cliTools.paths` configuration (with `~` expanded and relative entries resolved against the working directory). The first manifest found for a name SHALL win; later ones SHALL be recorded as shadowed. The feature SHALL be disabled, loading nothing, when `cliTools.disabled` is `true` or the environment variable `OPENCODE_DISABLE_CLI_TOOLS` is `true`. The `cliTools` object SHALL appear in the generated configuration JSON schema.

#### Scenario: Project manifest wins over global

- **WHEN** `.agents/tools/snow.yaml` exists in the working directory and `~/.agents/tools/snow.yaml` also exists
- **THEN** the project manifest defines the `snow` tool and the global one is reported as shadowed

#### Scenario: Feature disabled

- **WHEN** `cliTools.disabled` is `true`
- **THEN** no CLI tool is loaded, no manifest is read, and agent toolsets are identical to a workspace without manifests

#### Scenario: No manifests, no change

- **WHEN** no tool directory contains a manifest
- **THEN** every agent's toolset is byte-identical to the behaviour before this capability existed

### Requirement: Manifests are validated strictly and invalid ones never load

A manifest SHALL declare `name` (matching `^[a-z0-9][a-z0-9_-]{0,63}$` and equal to the file basename), `description` (non-empty, at most 4096 bytes) and `command` (non-empty). The loader SHALL reject unknown fields, names equal to a built-in tool name or to `toolsearch` / `struct_output`, a `mode` other than `argv` or `structured`, a `grant` other than `explicit` or `implicit`, invalid glob patterns, a `cwd` that resolves outside the working directory, a `maxTimeout` smaller than `timeout`, a structured manifest without `argv` or whose template references an undeclared parameter, and a `permission` value that is neither an action string nor a pattern map. A rejected manifest SHALL NOT produce a tool; the reason SHALL be available in the loader diagnostics and summarised in one startup warning.

#### Scenario: Unknown field is rejected

- **WHEN** a manifest contains a field `alow:` (misspelt `allow`)
- **THEN** the manifest is rejected with a diagnostic naming the unknown field and no tool with that name exists

#### Scenario: Reserved name is rejected

- **WHEN** a manifest is named `bash.yaml` with `name: bash`
- **THEN** it is rejected as a reserved name and the built-in `bash` tool is unaffected

#### Scenario: Basename mismatch is rejected

- **WHEN** a file `snowflake.yaml` declares `name: snow`
- **THEN** the manifest is rejected with a diagnostic naming both values

### Requirement: argv mode exposes a verbatim argument vector

A manifest in `argv` mode (the default) SHALL produce a tool whose input schema has a required `args` array of strings, an optional `timeout` integer (seconds), and, when the manifest sets `stdin: true`, an optional `stdin` string. The tool description SHALL state that arguments are passed verbatim as separate entries and that no shell interprets them. The tool SHALL NOT enumerate the binary's subcommands or flags in its schema.

#### Scenario: Arguments reach the process unchanged

- **WHEN** the model calls the tool with `args: ["sql", "-q", "select 'a && b'"]`
- **THEN** the binary receives exactly three arguments after the manifest's `prefixArgs`, the third being the literal string `select 'a && b'`

#### Scenario: stdin is delivered when enabled

- **WHEN** the manifest sets `stdin: true` and the call passes `stdin: "select 1;"`
- **THEN** the process reads `select 1;` from its standard input

### Requirement: structured mode renders declared parameters onto a fixed template

A manifest in `structured` mode SHALL expose its `parameters` (JSON-Schema properties) and `required` as the tool input schema and SHALL render the call onto its `argv` template. Template entries are literal strings, strings with `{param}` placeholders substituted per entry (a whole-entry placeholder for an array parameter expands to one entry per element), or nested lists forming optional groups emitted only when every placeholder they reference is present and not boolean `false`. The system SHALL reject calls with unknown parameters, missing required parameters, or values violating the declared `type`, `enum`, `minimum`/`maximum`, `minLength`/`maxLength` or `pattern`. A substituted value SHALL occupy exactly one argument and SHALL NOT be able to add, split or remove arguments.

#### Scenario: Optional group dropped

- **WHEN** the template is `["sql", "-c", "dcs", ["--format", "{format}"], "-q", "{query}"]` and the call provides only `query`
- **THEN** the rendered arguments are `sql -c dcs -q <query>` with no `--format` entry

#### Scenario: Value cannot escape its slot

- **WHEN** the call provides `query: "select 1; -x --password p"`
- **THEN** the whole string is one argument following `-q`, and no `-x` or `--password` argument exists in the vector

#### Scenario: Enum violation is rejected

- **WHEN** `format` is declared with `enum: [JSON, CSV]` and the call provides `format: "TABLE"`
- **THEN** the call returns an error response naming the parameter and the allowed values, and no process is started

### Requirement: The process is executed without a shell under the manifest's confinement

The system SHALL start the manifest's `command` directly with an argument vector consisting of `prefixArgs` followed by the model-influenced arguments; no shell SHALL interpret the vector. The working directory SHALL be the manifest `cwd` resolved inside the working directory (default: the working directory). The child environment SHALL be the parent environment when `env.inherit` is true; otherwise only `PATH`, `HOME`, `TMPDIR` and the names in `env.pass`; `env.set` entries SHALL be added last, with `${env.NAME}` tokens expanded from the parent environment. The call SHALL be bounded by the call's `timeout` clamped to `maxTimeout` (built-in defaults 2 and 10 minutes, inherited as the limits requirement describes when the manifest sets neither); on expiry the process group SHALL be killed and the result SHALL be an error naming the timeout.

#### Scenario: Shell operators are inert

- **WHEN** the model passes an argument `x; rm -rf /tmp/y` in argv mode
- **THEN** the binary receives that string as a single argument and no second process is started by the system

#### Scenario: Restricted environment

- **WHEN** the manifest sets `env.inherit: false` and `env.pass: ["SNOWFLAKE_HOME"]`
- **THEN** the child sees `PATH`, `HOME`, `TMPDIR`, `SNOWFLAKE_HOME` and nothing else from the parent environment

#### Scenario: Timeout kills the process group

- **WHEN** the call's timeout elapses before the process exits
- **THEN** the process and its children are terminated and the model receives an error response stating the timeout

### Requirement: Argument policy is enforced before any process starts, and violations are model-visible

Before starting the process, the system SHALL evaluate the manifest's `args.deny` patterns against every single model-influenced argument and against the space-joined argument string, then, when `args.allow` is non-empty, require the joined string to match at least one allow pattern. Patterns use the same `*` wildcard syntax as tool permissions and SHALL be matched case-insensitively. A violation SHALL return an error response naming the offending argument and pattern, SHALL NOT start the process, and SHALL NOT end the agent's run. Arguments containing a NUL byte SHALL be rejected the same way.

#### Scenario: Escape hatch denied anywhere in the vector

- **WHEN** `args.deny` contains `-x` and the model passes `["sql", "-q", "select 1", "-x"]`
- **THEN** the call is rejected with an error naming `-x`, the process is not started, and the agent continues its turn

#### Scenario: Allow prefix enforced

- **WHEN** `args.allow` is `["sql *"]` and the model passes `["connection", "list"]`
- **THEN** the call is rejected and no process is started

#### Scenario: Structured output also policed

- **WHEN** a structured manifest declares `args.deny: ["-f"]` and a template bug renders a `-f` entry
- **THEN** the call is rejected before execution

### Requirement: Output is captured, capped and reported with the exit status

The result SHALL contain the process's standard output, followed by a labelled standard-error block when non-empty, followed by a line stating the exit status. Content beyond the tool's effective `maxOutputBytes` (built-in default 51200, inherited as the limits requirement describes; negative disables) SHALL be spilled to the scratch directory and replaced by a head+tail preview naming the file, using the same mechanism as bash and MCP tools. A non-zero exit, a failure to start the process (including a binary that cannot be found) and a timeout SHALL be returned as error responses that still carry the captured output; the exit code SHALL be present in the response metadata.

#### Scenario: Non-zero exit is an error with output

- **WHEN** the process exits with status 5 after printing a message to stderr
- **THEN** the model receives an error response containing the stderr text and `exit status 5`

#### Scenario: Oversized output is spilled

- **WHEN** the process prints more than `maxOutputBytes`
- **THEN** the response is a preview naming a file in the scratch directory that holds the complete output

#### Scenario: Missing binary

- **WHEN** the manifest's `command` cannot be resolved at call time
- **THEN** the tool exists, the call returns an error naming the missing command, and no run-ending permission error is raised

### Requirement: Limits are configured once and applied by both surfaces

The limits a manifest leaves unset — `timeout`, `maxTimeout` and `maxOutputBytes` — SHALL be inherited from one resolved set of defaults, layered as: the environment variables `OPENCODE_CLI_TOOLS_TIMEOUT`, `OPENCODE_CLI_TOOLS_MAX_TIMEOUT` and `OPENCODE_CLI_TOOLS_MAX_OUTPUT_BYTES`, over the `cliTools.timeout`, `cliTools.maxTimeout` and `cliTools.maxOutputBytes` keys of `.opencode.json`, over the built-in values (2 minutes, 10 minutes, 51200 bytes). Durations SHALL accept a Go duration or a number of seconds; a negative output cap SHALL mean unbounded. A field the manifest sets SHALL always win. A manifest `timeout` above the inherited `maxTimeout` SHALL raise the cap to it, and a manifest `maxTimeout` below the inherited `timeout` SHALL lower the timeout to it, so that a global knob cannot invalidate a manifest; only a manifest whose own `timeout` and `maxTimeout` contradict SHALL be rejected. A knob value that does not parse or is not positive SHALL be ignored with a warning and the next layer SHALL apply; a resolved `maxTimeout` below the resolved `timeout` SHALL be raised to it with a warning. The resolved defaults SHALL be applied identically wherever manifests are loaded: an agent's native toolset and `opencode tools serve`. The three keys SHALL appear in the generated configuration JSON schema.

#### Scenario: Config knob reaches both surfaces

- **WHEN** `.opencode.json` sets `cliTools.maxOutputBytes: 1024` and a manifest sets no `maxOutputBytes`
- **THEN** a native call and a served `tools/call` of that tool both cap the output at 1024 bytes and spill the rest to a file

#### Scenario: Environment overrides config

- **WHEN** `cliTools.timeout` is `"1s"` and `OPENCODE_CLI_TOOLS_TIMEOUT=2s` is set
- **THEN** a call that runs longer is killed after 2 seconds, and `opencode tools list` reports the timeout default as `2s` from `env`

#### Scenario: Manifest field wins

- **WHEN** `cliTools.timeout` is `"1s"` and a manifest sets `timeout: 10s`
- **THEN** a 2-second call of that tool completes normally

#### Scenario: Invalid knob is ignored, not fatal

- **WHEN** `OPENCODE_CLI_TOOLS_TIMEOUT=soon` is set
- **THEN** every manifest still loads with the next layer's timeout, `opencode tools list` prints a warning naming the variable, and `--strict` exits 1

### Requirement: CLI tools are gated like built-in tools, explicit by default

A CLI tool SHALL be present in an agent's toolset only when the agent's configuration grants it: with `grant: explicit` (default) the agent must name the tool (an exact key or a specific wildcard in `tools:`, or an `allowTools` entry); a bare `"*": true` SHALL NOT grant it. With `grant: implicit` the tool SHALL follow the ordinary deny-list semantics of `tools:`. The tool SHALL be orderable and cacheable like external tools (listed after built-ins, sorted by name) and SHALL be deferrable through `deferredTools` with no additional configuration. Flow steps SHALL reach CLI tools through the step's agent grants without new step fields.

#### Scenario: Explicit grant required

- **WHEN** a manifest has `grant: explicit` and an agent's `tools` map is `{"*": true}`
- **THEN** the agent's toolset does not contain the tool

#### Scenario: Named grant

- **WHEN** the agent's `tools` map contains `"snow": true`
- **THEN** the agent's toolset contains the `snow` tool

#### Scenario: Deferred CLI tool

- **WHEN** the agent's `deferredTools` contains `"snow": true` and the tool is granted
- **THEN** the tool's schema is withheld until discovered through tool search, exactly as for an MCP tool

### Requirement: Per-call permission layers the manifest default under tool-specific rules

For each call the system SHALL evaluate the action using the space-joined model-influenced argument string as the input, with the chain: agent rule for the tool name, global rule for the tool name, the manifest's `permission` default, agent `"*"`, global `"*"`, then `ask`. `deny` SHALL behave as for every other tool (the call fails with the permission-denied error); `ask` SHALL raise an interactive permission request whose parameters show the command, the arguments and the working directory; `allow` SHALL execute.

#### Scenario: Agent rule beats manifest default

- **WHEN** the manifest default is `{"*": allow}` and the agent sets `permission.snow: {"*": ask}`
- **THEN** a call raises a permission request

#### Scenario: Manifest default beats agent wildcard

- **WHEN** the agent sets `permission: {"*": deny}`, grants the tool by name, and the manifest default is `{"sql *": allow}`
- **THEN** a call whose arguments start with `sql ` executes without a prompt

#### Scenario: Pattern input is the argument string

- **WHEN** the agent sets `permission.snow: {"*": ask, "sql *": allow}` and the model calls with `args: ["sql", "-q", "select 1"]`
- **THEN** the call executes without a prompt

### Requirement: Optional `--help` capture tracks the installed binary

When a manifest declares `help` (`args`, `maxBytes` default 4096), the system SHALL run the command once per process with those arguments (10 s limit) at load time and append the first `maxBytes` of its output to the tool description inside a fenced block, with a marker when truncated. When `help` is absent, or the binary cannot be resolved, the description SHALL be the manifest text alone.

#### Scenario: Help appended

- **WHEN** `help.args` is `["--help"]` and the binary prints usage text
- **THEN** the tool description ends with that usage text in a fenced block

#### Scenario: Help skipped for a missing binary

- **WHEN** `help` is declared but the binary is not found
- **THEN** the description is the manifest text and the manifest is reported as not found in the listing

### Requirement: `opencode tools list` audits the resolved manifests

The command `opencode tools list` SHALL print the resolved default limits with the layer each came from (`builtin`, `config`, `env`) and any ignored limit setting; then, for every manifest found, its name, source file, mode, command and resolved path (or NOT FOUND), grant mode, counts of deny and allow patterns, environment mode, effective timeouts and output cap; then the diagnostics for invalid and shadowed manifests with their reasons. `--json` SHALL emit the same as JSON; `--agent <id>` SHALL add whether that agent holds each tool and whether it is deferred; `--strict` SHALL exit with status 1 when any manifest is invalid or a limit setting was ignored.

#### Scenario: Strict listing fails CI on a bad manifest

- **WHEN** one manifest in `.agents/tools` has an invalid deny pattern
- **THEN** `opencode tools list --strict` prints the diagnostic and exits with status 1

#### Scenario: Agent view

- **WHEN** `opencode tools list --agent piano-snowflake-explorer` runs in a workspace where that agent names `snow`
- **THEN** the listing marks `snow` as held by the agent

### Requirement: `opencode tools serve` exposes the manifests over stdio MCP

The command `opencode tools serve` SHALL run a Model Context Protocol server over standard input/output whose tools are the valid manifests of the working directory (optionally narrowed by `--only name,…`), each with the same name, description and input schema as the native tool. A `tools/call` SHALL run the same policy and execution path as the native tool, under the same inherited limits (see the limits requirement); argument-policy violations and a manifest default permission of `deny` SHALL return an error result; `ask` and `allow` SHALL execute. Agent gating (`grant`) SHALL not apply. Standard output SHALL carry only protocol messages; logs SHALL go to standard error.

#### Scenario: Claude Code lists and calls a tool

- **WHEN** `.mcp.json` registers `{"command": "opencode", "args": ["tools", "serve"]}` under the server name `cli`
- **THEN** the client sees a tool `snow` with the manifest schema and a call with `args: ["--help"]` returns the binary's help text

#### Scenario: Policy holds over MCP

- **WHEN** an MCP client calls the served `snow` tool with `args: ["sql", "-x"]` and `-x` is denied
- **THEN** the result is an error naming the denied argument and no process is started

