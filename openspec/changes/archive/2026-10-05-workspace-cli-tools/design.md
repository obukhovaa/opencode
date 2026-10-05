# Design — workspace-defined CLI tools

## Context

See `proposal.md — Why` for motivation. Facts that shape the approach:

- **Tool plumbing is name-keyed and open.** `NewToolSet` (`internal/llm/agent/tools.go`) builds the per-agent toolset; builtins are gated with `reg.IsToolEnabled`, cron/heartbeat with `IsToolExplicitlyEnabled` (default-deny), MCP tools stream in from `MCPRegistry.LoadTools` and are filtered by name. Permission evaluation (`internal/permission/evaluate.go`) accepts any tool name as a key: agent[tool] → global[tool] → agent["*"] → global["*"] → ask. `deferredTools` matches by name, case-insensitively, and non-builtin names are announced through the runtime delta path. `OrderTools` places `IsBaseline()==false` tools in the name-sorted tail for cache stability.
- **The permission request path is generic.** `permission.Service.Request` takes `{SessionID, ToolName, Action, Description, Params, Path}`; "allow always" grants are keyed on `ToolName+Action+Dir(Path)`; MCP passes `config.WorkingDirectory()` as Path. The TUI dialog has a generic branch for unknown tools.
- **Discovery precedents.** Skills (`internal/skill`) walk `.opencode/skills`, `.agents/skills`, `.claude/skills` from the working dir up to the git root, then the global dirs, then `skills.paths`; first wins, losers and failures are recorded as diagnostics and one WARN. Agent markdown uses yaml.v3 (non-strict). Config map keys are lowercased by viper.
- **Output handling precedent.** `tools.PersistLargeOutput(content, label, source, maxBytes)` spills to the scratch dir and returns a head+tail preview; bash, MCP and webfetch use it with a 50KB default; negative disables.
- **mcp-go v0.43.2** (already a dependency) ships `server.NewMCPServer`, `AddTool`, `ServeStdio`; `cmd/acp.go` is the template for a stdio command (stderr logging, `--cwd`, `config.Load`).
- **Runtime facts.** Pods run `opencode serve --auto-approve`, so `ask` resolves to allow there; daemons rely on `router.permissionMode`. The runtime image has python3 without pip. Laptops run Claude Code without opencode (`docs/LOCAL-FLOW-RUNNER.md`); the brew tap `obukhovaa/tap/opencode` exists. Workspace config is a single `.agents.opencode.json` with `$VAR` placeholders rendered by `agent.sh`; opencode itself does no env expansion of config.
- **The snow test ground.** `snow sql` (3.28.0) takes `-q`, `-f FILE`, `-i`, `-D` templating, `-c <connection>` or `-x` with inline credentials (`--account`, `--password`, `--role`…), `--config-file`, `--format JSON|CSV`, `--enhanced-exit-codes` (2 parameter error, 5 query error), `--local-only` (restricts `!source`/`!load` to local files). It has no statement-type filter; the connection's role is the write boundary. Connections `dcs`, `logging`, `piano_dev` are rendered at boot into `~/.snowflake/config.toml`.

## Goals / Non-Goals

**Goals:**
- One declarative definition per CLI that is simultaneously a native opencode tool and an MCP tool, with one policy implementation behind both.
- The security boundary is the argument vector, the environment, the working directory, the time budget and the output cap of one process — never a parsed shell string.
- Zero behaviour change for workspaces without manifests; an agent's toolset is unchanged until it names a CLI tool.
- No per-subcommand schema maintenance in the default mode; the binary's own `--help` and skills carry the interface knowledge.
- Auditable from the repository alone: manifests are files, grants are tool names in agent frontmatter, `opencode tools list --strict` is CI-ready.

**Non-Goals:**
- Deriving a tool schema from `--help` output (no harness ships this; `--help` grammars are not machine-readable).
- OS-level sandboxing (Seatbelt / bubblewrap) of the child process — a separate change; this design keeps the argv/env/cwd confinement independent of it so a sandbox can wrap it later.
- Inline manifest definitions inside `.opencode.json` (viper lowercases nested keys; files are the unit of audit). `cliTools.paths` covers "manifests elsewhere".
- Replacing bash: the bash tool, its safe-read-only prefix list and CD-4933 semantics are untouched (the prefix-list hole is flagged in GENAI-411 as a separate follow-up).
- A Python re-implementation of the policy engine for laptops without opencode — rejected below.

## Decisions

### 1. Manifest = one file per tool, YAML/JSON, basename == name

`.agents/tools/<name>.yaml` (or `.yml` / `.json`; JSON is a YAML subset, so a future stdlib-only consumer can read JSON manifests without a YAML parser). The basename must equal `name`, exactly as skills require the directory name to match — a grep for the tool name finds its definition. Name grammar `^[a-z0-9][a-z0-9_-]{0,63}$` (lowercase because viper lowercases `tools:`/`deferredTools` keys in JSON config); builtin tool names, `toolsearch` and `struct_output` are reserved. yaml.v3 is used **strictly** (`KnownFields(true)`): a typo in a security field must not be silently ignored.

Fields (defaults in brackets):

```yaml
name: snow
description: >-                   # required, ≤ 4 KiB; what the model reads
command: snow                     # required: PATH lookup, absolute, or workspace-relative path
mode: argv                        # [argv] | structured
prefixArgs: []                    # trusted, always prepended, hidden from the model
args:
  allow: ["sql *", "--help", "sql --help", "connection list*"]   # [] = any
  deny:  ["-x", "--temporary-connection", "-f", "--filename*", "--config-file*", "connection add*"]
stdin: false                      # argv mode: expose a `stdin` string parameter
env:
  inherit: true                   # [true]; false = only PATH, HOME, TMPDIR and `pass` survive
  pass: []                        # names kept when inherit is false
  set: {}                         # literal KEY: value, `${env.NAME}` expands from the parent env
cwd: ""                           # [working dir]; relative paths must stay inside it
timeout: 2m                       # [2m] per-call default (Go duration)
maxTimeout: 10m                   # [10m] cap for the per-call `timeout` parameter
maxOutputBytes: 51200             # [50 KiB]; negative = unbounded
help:                             # optional: run once at load, append to the description
  args: ["--help"]
  maxBytes: 4096
grant: explicit                   # [explicit] | implicit
permission:                       # default per-call action (string or pattern map)
  "*": ask
  "sql *": allow
parameters: {}                    # structured mode: JSON-Schema properties
required: []
argv: []                          # structured mode: template (see D4)
```

*Alternatives:* a `TOOL.md` with frontmatter (skills style) — the body has no role here, and a tool is configuration, not prose; a directory per tool — nothing else to put in it; manifests inside `.opencode.json` — rejected as a non-goal.

### 2. Two input modes; `argv` is the default and never enumerates subcommands

**argv** exposes `{args: string[], stdin?: string, timeout?: integer}`. The description states that arguments are passed verbatim as separate argv entries and that no shell is involved. The author's `--help` capture (D6) and the workspace's skills (e.g. `piano-snowflake-cli`) carry the usage knowledge, which is exactly how Claude Code, Codex and the "CLIs over MCP" practice work today — the schema cannot go stale because it does not describe the CLI.

**structured** exposes the manifest's own `parameters` / `required` as the input schema and renders them onto a fixed `argv` template. This is the lock-down mode: the model never sees a flag, so `snow_dcs(query)` cannot pick another connection, add `-x`, or point at a file. It is deliberately small (D4) — a manifest author who finds themselves modelling a whole CLI should write a skill instead.

Both modes run through the same policy (D3) on the final model-influenced argument vector.

*Alternative:* a single mode with optional positional/flag mapping — the two needs (exploration vs lock-down) have opposite defaults and mixing them produces unreadable manifests.

### 3. Policy: argv-only exec, deny-then-allow globs, model-visible rejections

- The process is started with `exec.CommandContext(path, prefixArgs + modelArgs...)`; no shell is ever involved, so `&&`, `;`, `|`, `$(…)`, `>` are plain bytes inside one argument.
- `args.deny` is evaluated first: each pattern is matched against **every single argument** and against the space-joined argument string; one hit rejects the call. `args.allow`, when non-empty, must match the joined string. Patterns use the existing `permission.MatchWildcard` (`*` only), so authors learn one glob dialect. The joined-string form exists for ordering constraints (`sql -c dcs *`); the per-argument form is the one that is robust to reordering and is what the docs recommend for escape hatches (`-x`, `-f`, `--config-file*`).
- A policy rejection returns a tool error response ("rejected by the tool's argument policy: `-x` matches deny pattern `-x`") instead of `ErrorPermissionDenied`. The run continues; the model can fix the call. This is the behaviour the harness wanted from bash and could not have (CD-4933).
- Arguments containing NUL are rejected; nothing else is rewritten.
- `env`: by default the child inherits the parent environment (MCP servers do the same). `inherit: false` keeps only `PATH`, `HOME`, `TMPDIR` plus `pass`. `set` values accept the `${env.NAME}` token (same shell-free token style as `context.paths`); an unknown token leaves the value empty and is reported in diagnostics.
- `cwd` resolves against the working directory and must stay inside it (symlink-resolved); the default is the working directory.
- `timeout`: the per-call parameter is clamped to `maxTimeout`; on expiry the process **group** is killed (`task.SetProcessGroupAttr`, as bash does) and the result is an error naming the timeout.
- Output: stdout, then a labelled stderr block, then `exit status N`; the whole is passed through `tools.PersistLargeOutput(out, name, "clitool", maxOutputBytes)`. Exit ≠ 0 or a start failure (binary not found) is an error response; the exit code is also in the response metadata.

*Alternative considered:* tokenising a shell-like command string and validating it (what bash patterns pretend to do) — this is the failure mode every CVE in the research memo exploits.

### 4. Structured template grammar — small on purpose

`argv` is a list whose entries are:
- a literal string;
- a string containing `{param}` placeholders, substituted from the call's arguments (string / number / integer / boolean formatted with `strconv`); a placeholder that is the **whole** entry and refers to an array parameter expands to one entry per element;
- a nested list: an **optional group** emitted only when every placeholder it references is present (and not boolean `false`); otherwise the group is dropped. `["--format", "{format}"]` is the idiom for an optional flag.

Substitution is per-entry, so a value can never split into several argv entries or escape its slot. Unknown parameters in the call are rejected; `required` is enforced; `enum` and `type` are validated against the declared schema (a dependency-free subset: type, enum, minimum/maximum, minLength/maxLength, pattern). The rendered argv then goes through D3 like any other.

*Alternative:* a full templating language — invites logic into manifests; the point of structured mode is that the argv is fixed.

### 5. Gating, grant mode and permission layering

- `grant: explicit` (default): the tool is in the toolset only when the agent names it — `IsToolExplicitlyEnabled` for `tools:` maps (a bare `"*"` does not count; a specific wildcard such as `snow*` does) or `IsToolAllowlisted` for `allowTools`. This inverts the MCP default that forces every workspace type to carry `"<server>*": false`. `grant: implicit` restores "enabled unless denied" for teams that want a TUI-wide helper.
- Per-call permission: a new `permission.EvaluateToolPermissionWithDefault(tool, input, agentPerms, globalPerms, toolDefault)` inserts the manifest's `permission` as a layer **after** the tool-specific agent/global rules and **before** the `"*"` wildcards: agent[tool] → global[tool] → manifest → agent["*"] → global["*"] → ask. A manifest default is more specific than an agent's blanket `"*"` but must never beat a rule written for that tool. `input` is the space-joined model-influenced argv (the string a human would type after the binary), so `permission.snow: {"sql *": allow}` reads naturally and matches what the TUI shows.
- `ActionDeny` keeps the harness-wide meaning (`ErrorPermissionDenied`, the run ends) because it expresses an agent-level prohibition, not a malformed call. `ActionAsk` goes through `permissions.Request` with `Action: "execute"`, `Path: config.WorkingDirectory()`, and params `{command, args, cwd}` so the dialog shows the exact process.
- `deferredTools` needs nothing new: the wrapper is wrapped by `maybeDefer` like MCP tools, `IsBaseline()` is false, and the non-builtin announcement path covers it.

### 6. `--help` capture is opt-in and happens at load

When `help:` is present and the binary resolves, the loader runs `command help.args` once per process with a 10 s timeout and appends the first `maxBytes` of stdout+stderr as a fenced block to the description (with a truncation marker). The description therefore tracks the installed binary, not a hand-copied snapshot. It is opt-in because a description is paid on every request unless the tool is deferred; the docs recommend `help:` together with `deferredTools`, or none at all (the model can run `<tool> --help` itself in argv mode).

### 7. Missing binary: load the tool, fail the call

A manifest whose `command` does not resolve still produces a tool; each call returns an error naming the missing binary. The harness's own guidance ("a missing `snow` is a wiring defect to report") needs the model to see that, and `opencode tools list` marks the manifest `NOT FOUND`. The `--help` capture is skipped. Invalid manifests (schema errors, reserved names, bad patterns, cwd escaping the working dir) never load — fail closed — and are reported in `tools list` and as one aggregated WARN at startup.

### 8. Discovery order and shadowing

Project (`.opencode/tools`, `.agents/tools`, from the working dir up to the git root), then global (`~/.config/opencode/tools`, `~/.agents/tools`), then `cliTools.paths` (`~` and relative paths as in `skills.paths`); first definition of a name wins, later ones are recorded as shadowed. A non-recursive scan of `*.yaml|*.yml|*.json`. `cliTools.disabled: true` turns the feature off; `OPENCODE_DISABLE_CLI_TOOLS=true` is the env equivalent for CI images. The resolved set is cached per process (like skills) with `Invalidate()` for tests.

### 9. `opencode tools list|serve` — audit and the MCP bridge

- `list [--json] [--agent <id>] [--strict]`: per manifest — name, file, mode, command → resolved path or NOT FOUND, grant, deny/allow counts, default permission, and with `--agent` whether that agent holds it and under which rule; then the diagnostics (invalid, shadowed). `--strict` exits 1 on any invalid manifest so a workspace CI job can lint `.agents/tools/` without running an agent.
- `serve [--only a,b]`: a stdio MCP server built with `mcp-go`'s `server` package; one MCP tool per manifest with the identical input schema and description; `tools/call` runs the same loader → policy → executor path. Without an agent there is no `tools:` gating and no human in the loop, so: hard policy is enforced, a manifest `permission` that resolves to `deny` returns an `isError` result, `ask` and `allow` execute (the MCP client — Claude Code — owns the human prompt via its own `mcp__<server>__<tool>` rules), `grant` is ignored. Logs go to stderr; stdout is protocol only (as `cmd/acp.go`).
- Why opencode and not a Python shim for laptops: the memo's single strongest finding is that policy engines drift; two implementations of deny globs, env scrubbing and template rendering would re-create the audit problem this change removes. Laptops get `brew install obukhovaa/tap/opencode` (already documented in c2-agent's HITL manual) and a `cli-tools` entry in the harness's MCP catalogue; the harness MR owns that wiring.

### 10. Snow manifests (the proving ground, shipped by the harness MR)

- `snow.yaml` — argv mode for the explorer type: `allow: ["sql *", "--help", "* --help", "connection list*", "connection test*"]`, `deny: ["-x", "--temporary-connection", "-f", "--filename*", "-i", "--stdin", "--config-file*", "--password*", "--private-key*", "--token*", "-D", "--variable*", "connection add*", "connection set-default*", "connection remove*", "auth *", "init*"]`, `permission: {"*": ask, "sql *": allow, "--help": allow}`, `help: {args: ["sql", "--help"], maxBytes: 3000}`, `grant: explicit`, `timeout: 2m`, `maxTimeout: 10m`.
- `snow_dcs.yaml` / `snow_logging.yaml` — structured, per tenant: `parameters: {query: {type: string}, format: {enum: [JSON, JSON_EXT, CSV]}, timeout…}`, `argv: ["sql", "-c", "dcs", "--format", "{format}", "--enhanced-exit-codes", "--enable-templating", "NONE", "--local-only", "-q", "{query}"]`, `args.deny: ["-x", "-f"]` as belt and braces. This restores the per-tenant grant the MCP had and removes the four-flag ritual from the skill.
The scenario-builder runners and the incident investigator take the structured tools; the explorer takes both.

## Risks / Trade-offs

- [The joined-string glob is spoofable by argument *content*, e.g. a SQL text containing ` -f `] → per-argument matching is the documented primary form for escape hatches; joined-string patterns are for ordering; false positives fail closed. Structured mode removes the problem entirely.
- [`env.inherit: true` leaks secrets to any wrapped binary] → same as MCP servers today; manifests for untrusted binaries set `inherit: false` + `pass`; the docs say so and `tools list` shows the env mode.
- [A long `--help` capture bloats every request] → opt-in, byte-capped, recommended only with `deferredTools`.
- [Agents on `--auto-approve` pods treat `ask` as allow, so manifest `permission` adds nothing there] → the hard policy (deny/allow globs, structured templates) is what protects pods; `permission` is for TUI/daemon/MCP-client paths. Documented.
- [Name collision with an asynchronously loaded MCP tool (`<server>_<tool>`)] → builtin names are rejected at load; MCP collisions are logged when the toolset resolves and the CLI tool wins (it is the explicitly granted one). Authors avoid `<server>_` prefixes.
- [yaml.v3 strict mode breaks manifests written for a newer opencode with new fields] → the loader reports the unknown field by name; version skew is visible instead of silent.
- [Process-group kill on timeout does not stop server-side work (a Snowflake query keeps running)] → unchanged from bash; the skill's cancel-by-tag guidance stays.
- [Claude Code users must install opencode] → brew tap exists; the harness MR gates the catalogue entry on `command -v opencode` and keeps the skill path working without it.

## Migration Plan

1. Land this change in the opencode fork (feature is dormant without manifests); release `v0.22.0`.
2. c2-agent: bump the opencode pin (routine release); no other image change — `snow` is already installed and connections rendered.
3. piano-developer MR: add `.agents/tools/snow*.yaml` (+ `.gitignore` re-include, CI `yaml-configs-parse` coverage, `opencode tools list --strict` job), switch `piano-snowflake-explorer` and `piano-incident-investigator` to the tools (`tools: {snow_dcs: true, …}`, `bash` withheld again), add `cli-tools` to `.agents/mcp/servers.json`, teach `sync_claude_agents.py` to emit `mcpServers: [cli-tools]` for granted CLI tools, update `piano-snowflake-cli` to name the tools. Merge after step 2 is live.
4. scenario-builder (!180 follow-up) and team repos adopt the same manifests; the `"snow *"` bash allows and the per-type `"snow *": deny` lines go away as each type moves.
5. Rollback: delete the manifests (or `cliTools.disabled: true`) — agents fall back to whatever their `tools:` grants; no data or schema migration is involved.

## Open Questions

- Whether the harness wants a third, `logging`-only structured manifest on the investigator or keeps it on `snow_dcs` only — a manifest-authoring choice in the piano-developer MR; nothing here depends on it.
