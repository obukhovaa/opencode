# Workspace-defined CLI tools

## Why

AI3-12128 retires the deprecated Snowflake MCP server in favour of the Snowflake CLI (`snow sql -c <connection>`), and the migration MRs (scenario-builder!180, piano-developer!211/!212, the team repos) reach the CLI the only way the harness allows today: through the `bash` tool. That trade costs more than the MCP did:

- **Security.** The bash permission map is a glob over the command *string*. `"*": deny` + `"snow *": allow` admits `snow … && rm -rf …`, and `bash`'s safe-read-only prefix list (`env`, `printenv`, `timeout`, `nohup`, …) skips the permission check entirely, so `env bash -c …` or `timeout 5 <anything>` runs on every type that now advertises bash (reproduced in !180's description against `internal/permission/evaluate.go`). Every harness that used string-matched shell allow-lists as a boundary has shipped a bypass in 2025 (Claude Code CVE-2025-54795/55284, Gemini CLI, Cursor CVE-2025-54131/62354); Claude Code's own docs now say a `Bash(...)` rule "isn't a security boundary".
- **Lost tool semantics.** A CLI behind bash has no entry in agent frontmatter `tools:`, cannot be `deferredTools`-withheld, has no per-tool permission key, no tool card in the TUI / bridge, and no name a workspace validator can pin — scenario-builder's validator now pins bash maps instead of a tool. The old per-tenant grant (`snowflake-dcs*` vs `snowflake-logging*`) dissolves: bash reaches every connection the pod renders.
- **Reliability.** A refused tool call ends the run mid-step (CD-4933), so customer-facing types that had bash withheld on purpose must now advertise it.
- **Claude Code parity.** The harness auto-installs MCP servers for Claude Code (`scripts/mcp/install.sh`); a CLI reached through `Bash(snow *)` has no install / grant story, and the agent-mirror converter already diverged between workspaces on how to map `"*": deny` + `"snow *": allow`.

Two options were weighed (GENAI-411). A workspace gateway script registered as an MCP server (the `.agents/mcp/slack/server.py` precedent) needs no opencode change but re-creates "an MCP of a CLI": a hand-maintained schema per CLI that drifts from the binary, stdlib-only Python in the runtime image, and no shared policy or audit surface. The surveyed harnesses that solved this (Gemini CLI `discoveryCommand`/`callCommand`, Amp toolboxes, sst/opencode custom tools, Copilot custom tools) all chose a **declared manifest wrapping an executable, run argv-only**, with `--help` and skills carrying the knowledge the schema does not. That is what this change builds, natively in opencode and served over MCP for everything else.

## What Changes

- **Manifests.** A workspace declares a CLI tool as one YAML (or JSON) file under `.agents/tools/<name>.yaml` (also `.opencode/tools/`, the global `~/.config/opencode/tools` and `~/.agents/tools`, and `cliTools.paths`). A manifest names the binary, a description, an optional fixed argument prefix, an argument policy (allow / deny globs), environment and working-directory confinement, timeouts, an output cap, an optional `--help` capture, its grant mode and a default per-call permission. Two input modes: `argv` (default; the model passes `args: string[]`, the schema never enumerates subcommands) and `structured` (declared JSON-Schema parameters rendered onto a fixed argv template, for hot paths that must be locked down, e.g. a per-tenant `snow_dcs(query)`).
- **First-class tools.** Each valid manifest becomes a native tool with the manifest's name: it is gated by agent `tools:` / `allowTools`, obeys `permission.<name>` globs on the argument string, can be deferred via `deferredTools`, reaches flow steps through the step's agent, renders in the TUI / bridge like any tool and appears in telemetry under its own name. `grant: explicit` (default) means an agent must name the tool to receive it — no more `"<server>*": false` on every type.
- **Execution without a shell.** The binary is started with an argument vector (no `sh -c`), so operators, substitutions, pipes and redirects are never interpreted. Policy violations return a model-visible error (the model corrects itself; the run does not end, unlike a permission deny). Output is capped and spilled to a scratch file like bash / MCP / webfetch; a non-zero exit is an error result carrying the exit code.
- **Audit and Claude Code bridge.** `opencode tools list` prints every resolved manifest (file, binary path or NOT FOUND, mode, grant, policy, which agents hold it) and exits non-zero in `--strict` mode on an invalid manifest, so CI can lint workspaces. `opencode tools serve` exposes the same manifests over stdio MCP with the same policy engine, so Claude Code (`.mcp.json`) and any MCP client consume them unchanged — one definition, one policy implementation, no per-client re-authoring.
- **Config contract.** New top-level `cliTools` object (`paths`, `disabled`) in `.opencode.json`, declared in `cmd/schema/main.go`, regenerated into `opencode-schema.json`, documented in `docs/cli-tools.md` and the AGENTS.md index.

## Capabilities

### New Capabilities

- `workspace-cli-tools`: declarative workspace manifests that wrap a host CLI as a first-class opencode tool — discovery and validation, argv-only execution with argument / environment / cwd / time / output confinement, argv and structured input modes, gating and permission layering, deferral, the `opencode tools list|serve` commands and the MCP bridge.

### Modified Capabilities

<!-- none — deferred-tools, tool gating and permission evaluation already apply by tool name; CLI tools reuse them without changing their requirements. -->

## Impact

- New package `internal/clitool` (manifest types, loader, discovery, policy, argv rendering, executor, help capture, diagnostics) and `internal/llm/tools/clitool.go`-style wrapper registered from `internal/llm/agent/tools.go` next to MCP tools.
- `internal/permission`: one additional evaluation entry point that accepts a tool-supplied default layer (agent/global tool rules → manifest default → agent/global `*` → ask).
- New cobra command group `cmd/tools.go` (`opencode tools list`, `opencode tools serve`) using the `mcp-go` server package already in `go.mod` (no new dependency).
- `internal/config/config.go` (`CLIToolsConfig`), `cmd/schema/main.go` + `opencode-schema.json`, `docs/cli-tools.md`, `docs/tool-permissions.md`, `docs/deferred-tools.md`, `AGENTS.md`, `README.md`; e2e script `scripts/test/cli_tools.sh`.
- Behaviour change: none for workspaces without manifests. Manifests are opt-in and `grant: explicit` by default, so an existing agent's toolset is byte-identical until it names a CLI tool.
- Downstream (tracked in GENAI-411, separate MRs): piano-developer ships `.agents/tools/snow*.yaml`, moves `piano-snowflake-explorer` / `piano-incident-investigator` from `bash` + `"snow *"` to the `snow` tool, registers `opencode tools serve` in the Claude Code MCP catalogue and teaches `sync_claude_agents.py` the mapping; c2-agent bumps the opencode pin at the next release. The Snowflake binary, connections and credentials are unchanged.
