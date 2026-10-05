# Workspace CLI tools

A workspace can turn a host command-line program into a **first-class opencode tool** with one YAML file. The manifest names the binary, describes it for the model, confines what may be passed to it, and the tool then behaves like any built-in: agents grant it by name in `tools:` / `allowTools`, per-call permission rules apply under the tool's own key, `deferredTools` can withhold its schema, flow steps reach it through the step's agent, and it shows up as a tool card in the TUI, the bridge and telemetry.

The same manifests are served over stdio MCP by `opencode tools serve`, so Claude Code and any other MCP client use them unchanged — one definition, one policy implementation.

Why this exists, and why not `bash` with a permission pattern or a hand-written MCP server, is in `openspec/changes/workspace-cli-tools/proposal.md`. The short version: a bash permission map is a glob over a shell *string* and is not a security boundary (`"snow *": allow` admits `snow … && rm -rf …`); an MCP server per CLI re-creates a hand-maintained schema that drifts from the binary. A manifest runs the binary **argv-only, with no shell**, and does not describe the CLI's subcommands at all — the binary's `--help` and the workspace's skills do that.

## Quick start

`.agents/tools/snow.yaml`:

```yaml
name: snow
description: >-
  Snowflake CLI. Query with `sql -c <connection> --format JSON -q "<sql>"`;
  load the piano-snowflake-cli skill for conduct. Connections: dcs, logging.
command: snow
args:
  allow: ["sql *", "--help", "sql --help", "connection list*"]
  deny:  ["-x", "--temporary-connection", "-f", "--filename*", "-i", "--stdin",
          "--config-file*", "--password*", "--private-key*", "connection add*"]
permission:
  "*": ask
  "sql *": allow
  "--help": allow
help:
  args: ["sql", "--help"]
  maxBytes: 3000
```

Grant it to an agent (`.agents/types/snowflake-explorer.md`):

```yaml
tools:
  snow: true
deferredTools:
  snow: true          # optional: withhold the schema until the model searches for it
permission:
  snow:
    "*": ask          # optional: tighten the manifest's default for this agent
    "sql *": allow
```

Audit what the workspace resolves:

```
$ opencode tools list
Scanned: /workspace/.agents/tools

snow  (argv, grant: explicit)
  file:     /workspace/.agents/tools/snow.yaml
  command:  snow -> /home/agent/.local/bin/snow
  policy:   deny 10, allow 4; stdin: false; env: inherit
  limits:   timeout 2m0s (max 10m0s); output cap 51200 bytes; cwd /workspace
  help:     captured 2998 bytes into the description
  default permission: {"*":"ask","--help":"allow","sql *":"allow"}
```

The model then calls `snow` with `{"args": ["sql", "-c", "dcs", "--format", "JSON", "-q", "select 1"]}`.

## Where manifests live

Scanned non-recursively for `*.yaml`, `*.yml`, `*.json` (JSON is a YAML subset), in precedence order — the first file for a name wins and later ones are reported as *shadowed*:

1. `.opencode/tools/` and `.agents/tools/` in the working directory and each ancestor up to the git worktree root
2. `~/.config/opencode/tools/` and `~/.agents/tools/`
3. every directory in `cliTools.paths` (`.opencode.json`; `~` and relative paths as in `skills.paths`)

The file basename must equal the manifest's `name`, so a grep for the tool name finds its definition. The set is loaded once per process (restart to pick up edits).

```json
{
  "cliTools": {
    "paths": ["./team/tools"],
    "disabled": false,
    "timeout": "2m",
    "maxTimeout": "10m",
    "maxOutputBytes": 51200
  }
}
```

`timeout`, `maxTimeout` and `maxOutputBytes` are the limits every manifest inherits for the fields it leaves unset — see [Limits](#limits-one-set-of-knobs-for-both-surfaces).

`cliTools.disabled: true` or `OPENCODE_DISABLE_CLI_TOOLS=true` turns the feature off entirely. A workspace without manifests is unaffected: agent toolsets are byte-identical to the behaviour before this feature existed.

## Manifest reference

```yaml
name: snow                        # required; ^[a-z0-9][a-z0-9_-]{0,63}$; equals the file basename;
                                  # built-in tool names are reserved
description: >-                   # required, ≤ 4 KiB — what the model reads
command: snow                     # required: a PATH lookup, an absolute path, or a
                                  # workspace-relative path ("./bin/x")
mode: argv                        # argv (default) | structured — see below
prefixArgs: []                    # trusted, always prepended, never shown to the model
args:
  allow: []                       # globs; when non-empty the joined argv must match one
  deny: []                        # globs; matched against EACH argument and the joined argv
stdin: false                      # argv mode: expose a `stdin` string parameter
env:
  inherit: true                   # false = only PATH, HOME, TMPDIR and `pass` survive
  pass: []                        # names kept when inherit is false
  set: {}                         # KEY: value added last; `${env.NAME}` expands from the parent env
cwd: ""                           # relative to the working directory; must stay inside it
timeout: 2m                       # per-call default (Go duration or seconds); unset = inherited (see Limits)
maxTimeout: 10m                   # cap for the per-call `timeout` parameter; unset = inherited
maxOutputBytes: 51200             # context cap; -1 = unbounded; unset = inherited
help:                             # optional one-off capture appended to the description
  args: ["--help"]
  maxBytes: 4096
grant: explicit                   # explicit (default) | implicit — see "Granting"
permission: {}                    # default per-call action: a string or a pattern map
parameters: {}                    # structured mode: JSON-Schema properties
required: []                      # structured mode
argv: []                          # structured mode: the template
```

Loading is **strict**: an unknown field (`alow:`), a reserved name, a `cwd` outside the working directory, `maxTimeout < timeout`, a template that references an undeclared parameter — each fails the manifest, and a failed manifest never becomes a tool (fail closed). `opencode tools list` prints every reason; `--strict` exits 1 for CI.

A manifest whose binary cannot be found still loads: calls return a clear "command not found" error so the model can report the wiring defect, and the listing shows `NOT FOUND`.

## Two input modes

### `argv` (default) — exploration, no schema to maintain

The tool exposes:

| Parameter | Type | Meaning |
|---|---|---|
| `args` | `string[]` (required) | one argv entry per element, passed verbatim |
| `timeout` | integer | seconds, clamped to `maxTimeout` |
| `stdin` | string | only when the manifest sets `stdin: true` |

The schema deliberately says nothing about the binary's subcommands or flags, so it can never go stale. The model learns the interface from the description, the optional captured `--help` block, the workspace's skills and step prompts — and it can always call the tool with `["--help"]` itself (allow it in the policy).

### `structured` — lock-down for hot paths

The manifest declares its own parameters and a fixed argv template; the model never sees a flag:

```yaml
name: snow_dcs
description: Run one read-only SQL query against the DCS tenant (JSON rows).
command: snow
mode: structured
parameters:
  query:  {type: string, minLength: 1, description: "One SQL statement; add a LIMIT."}
  format: {type: string, enum: [JSON, JSON_EXT, CSV], description: "Default JSON."}
required: [query]
argv:
  - sql
  - "-c"
  - dcs
  - ["--format", "{format}"]
  - --enhanced-exit-codes
  - ["--enable-templating", "NONE"]      # ← invalid: a group needs a placeholder (see rules)
  - -q
  - "{query}"
```

(Write the fixed flags as plain entries — `--enable-templating`, `NONE` — and keep groups for optional parameters.)

Template rules:

- a string entry may embed `{param}` placeholders; the substituted value occupies exactly that one argument, so it can never add, split or remove arguments (`query: "select 1; -x"` is one argument after `-q`);
- a placeholder for an **array** parameter must be the whole entry and expands to one argument per element;
- a nested list is an **optional group**, emitted only when every placeholder it references is present and not boolean `false` — the idiom for optional flags (`["--format", "{format}"]`);
- a **boolean** parameter must be the whole entry inside a group; it emits nothing and only guards the group (`["--verbose", "{verbose}"]`).

Supported schema subset: `type` (string, integer, number, boolean, array of strings), `enum`, `minimum` / `maximum`, `minLength` / `maxLength`, `pattern`, `required`. Unknown parameters are rejected. The rendered argv then goes through the same `args` policy as argv mode (belt and braces: `args.deny: ["-f"]` catches a template bug).

Structured mode is how a *per-tenant* grant is expressed: `snow_dcs` and `snow_logging` are two manifests, and an agent is granted one or both by name — the lock the MCP's per-server grant used to give.

## What runs, and the two boundaries

The process is started directly with `prefixArgs + args` — `exec`, no `sh -c`. `&&`, `;`, `|`, `$(…)` and `>` are plain bytes inside one argument. There are two distinct boundaries, and `docs/tool-permissions.md` keeps them apart:

**What the process can do** (the manifest's hard policy, not overridable by any agent):

- `args.deny` is checked first, every pattern against every single argument *and* against the space-joined vector; one hit rejects the call. Per-argument form is the robust one for escape hatches (`-x`, `-f`, `--config-file*`): it does not care about ordering. The joined form expresses ordering (`sql -c dcs *`) and can be fooled by argument *content* (a SQL text containing ` -f `), so it fails closed — prefer per-argument patterns, or structured mode.
- `args.allow`, when non-empty, must match the joined vector.
- Arguments containing NUL are rejected.
- `env`: inherited by default (as MCP servers do); `inherit: false` keeps PATH, HOME, TMPDIR plus `pass`; `set` adds literals last.
- `cwd` is confined to the working directory; `timeout` kills the whole process group on expiry.

A policy rejection is returned **to the model as a tool error** — the run continues and the model fixes the call. This is deliberately different from a permission denial, which ends the run (CD-4933 semantics unchanged for bash).

**What the model may ask for** (the permission layer, same vocabulary as every other tool):

```
agent.permission.<tool>  →  global permission.rules.<tool>  →  manifest `permission`
   →  agent.permission["*"]  →  global rules["*"]  →  ask
```

The manifest's default sits below rules written for the tool and above the blanket wildcards. The pattern input is the space-joined argument string (what the TUI shows), so `permission.snow: {"sql *": allow}` reads naturally. On `--auto-approve` pods `ask` resolves to allow, so **the hard policy is what protects a pod**; `permission` matters for the TUI, daemons and the MCP bridge.

## Limits: one set of knobs for both surfaces

Three limits bound every call: the per-call **timeout** (built-in 2 m), the **maxTimeout** cap on the call's `timeout` parameter (built-in 10 m) and the **maxOutputBytes** context cap (built-in 50 KiB). A manifest may set each one; for the fields it leaves out, every manifest inherits one resolved value, in this precedence:

| Layer | Where | Example |
|---|---|---|
| 1. manifest field | `timeout`, `maxTimeout`, `maxOutputBytes` in the YAML | `timeout: 5m` |
| 2. environment | `OPENCODE_CLI_TOOLS_TIMEOUT`, `OPENCODE_CLI_TOOLS_MAX_TIMEOUT`, `OPENCODE_CLI_TOOLS_MAX_OUTPUT_BYTES` | `OPENCODE_CLI_TOOLS_MAX_OUTPUT_BYTES=-1` |
| 3. config | `cliTools.timeout`, `cliTools.maxTimeout`, `cliTools.maxOutputBytes` in `.opencode.json` | `"timeout": "90s"` |
| 4. built-in | compiled defaults | 2m / 10m / 51200 |

Durations are Go durations (`"90s"`, `"2m"`) or a number of seconds; `maxOutputBytes` is bytes, with a negative value meaning unbounded (the same convention as `webFetch.maxOutputBytes` and an MCP server's `callToolMaxOutputBytes`). The knobs are resolved once wherever manifests are loaded, so the native tools in an agent's toolset and `opencode tools serve` (which loads the `.opencode.json` of its `--cwd` and sees the same environment) apply identical limits: tune a pod with one environment variable, a laptop with one config line, and the two surfaces agree.

- A manifest field always wins: a manifest that says `timeout: 5m` keeps it under `cliTools.timeout: "30s"`.
- A manifest `timeout` above the inherited `maxTimeout` raises the cap to the manifest's value, and a manifest `maxTimeout` below the inherited `timeout` lowers the timeout to it, so a global knob can never make a valid manifest fail to load. Only a manifest whose own `timeout` and `maxTimeout` contradict each other is rejected.
- A knob that does not parse or is not positive is ignored with a warning and the next layer applies; a resolved `maxTimeout` below the resolved `timeout` is raised to it. `opencode tools list` prints the warnings and the resolved `defaults` with the layer each came from (`builtin`, `config`, `env`); `--strict` exits 1 on a warning.

## Output

Standard output, then a `--- stderr ---` block when non-empty, then `exit status N`. Over the tool's effective `maxOutputBytes` (see Limits; built-in 50 KiB) the text is spilled to the scratch directory and replaced by a head+tail preview naming the file, exactly like bash, MCP and webfetch output. A non-zero exit, a timeout and a missing binary are error results that still carry the captured output; the exit code and duration ride in the response metadata.

## Granting and deferral

`grant: explicit` (default): the agent must **name** the tool — `tools: {snow: true}`, a specific wildcard (`snow*: true`), or an `allowTools` entry. A bare `"*": true` does not grant it, exactly as for the cron tools. This is the opposite of the MCP default and means no type has to carry `"<server>*": false` for a tool it never asked for; a grep for the tool name across `.agents/types/` is the audit.

`grant: implicit` restores "enabled unless denied" for a helper that every agent in a TUI should have.

`deferredTools: {snow: true}` works with no further configuration (`docs/deferred-tools.md`): the schema — and the captured help block — stay out of the request until the model searches for the tool. Pair `help:` with deferral; without it the help text is paid on every request.

## `opencode tools list` and `serve`

```
opencode tools list [--cwd DIR] [--json] [--agent ID] [--strict]
opencode tools serve [--cwd DIR] [--only a,b]
```

`list` prints the resolved default limits with the layer each came from, any ignored limit setting, every resolved manifest (file, binary or `NOT FOUND`, mode, grant, policy counts, env mode, effective limits, default permission, structured parameters) and the diagnostics (invalid and shadowed files). `--agent` adds whether that agent holds each tool and whether it is deferred. `--strict` exits 1 on any invalid manifest or ignored limit setting — put it in the workspace CI next to the skill-frontmatter lint.

`serve` runs a stdio MCP server whose tools are the manifests, with identical names, descriptions and schemas. Each call goes through the same `Prepare → policy → exec` path under the same limits (the manifest's fields, then `OPENCODE_CLI_TOOLS_*`, then the `cliTools` block of the `--cwd` workspace's `.opencode.json`). There is no agent and no human loop in this mode: the hard policy is enforced; a manifest `permission` that resolves to `deny` returns an error result; `ask` and `allow` execute, because the MCP client owns the prompt. `grant` does not apply. Logs go to stderr.

Claude Code, `.mcp.json`:

```json
{
  "mcpServers": {
    "cli": { "command": "opencode", "args": ["tools", "serve", "--cwd", "."] }
  }
}
```

The tools appear as `mcp__cli__snow` etc., so Claude Code's own `permissions.allow` / `deny` rules and tool search apply to them. Since the server is opencode itself, laptops need the binary (`brew install obukhovaa/tap/opencode`).

## Writing a good manifest

- Name the escape hatches in `args.deny` first: inline credentials (`-x`, `--password*`, `--token*`), file execution (`-f`, `--filename*`, `-i`), alternate config (`--config-file*`), and administrative subcommands (`connection add*`, `auth *`).
- Keep `description` about *when and how* to use the tool; put conduct (flags to always pass, limits, cancellation) in a skill and name the skill in the description.
- Use structured mode for anything a customer-facing or unattended agent runs; use argv mode for an explorer that needs the whole CLI.
- `env.inherit: false` for binaries you do not fully trust; list exactly what they need in `pass`.
- Run `opencode tools list --strict` in CI, and `opencode tools list --agent <type>` when you change a type's grants.
