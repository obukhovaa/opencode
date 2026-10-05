## 1. Configuration contract

- [x] 1.1 Add `CLIToolsConfig { Paths []string; Disabled bool }` to `internal/config/config.go` as top-level `cliTools`, documented next to `Skills`.
- [x] 1.2 Declare `cliTools` in `cmd/schema/main.go` and regenerate `opencode-schema.json` (`make schema`).
- [x] 1.3 Viper round-trip unit test for `cliTools` (`paths`, `disabled`) under `internal/config/`.

## 2. Manifest package `internal/clitool`

- [x] 2.1 Manifest types with defaults (`mode`, `grant`, timeouts, `maxOutputBytes`, `env.inherit`, `help.maxBytes`) and strict yaml.v3 decoding (`KnownFields`), JSON accepted as YAML.
- [x] 2.2 Validation: name grammar + basename match, reserved names (builtins, `toolsearch`, `struct_output`), mode/grant enums, glob syntax, `cwd` inside the working directory, `maxTimeout >= timeout`, permission value shape, structured template references only declared parameters.
- [x] 2.3 Discovery: project dirs up to the git root, global dirs, `cliTools.paths`; first-wins with shadowed/invalid diagnostics; `cliTools.disabled` and `OPENCODE_DISABLE_CLI_TOOLS`; per-process cache with `Invalidate()`; one aggregated startup WARN.
- [x] 2.4 Policy: deny (per-argument and joined) then allow (joined) using `permission.MatchWildcard`; NUL rejection; typed `PolicyError` with argument and pattern.
- [x] 2.5 Structured rendering: placeholder substitution per entry, array expansion, optional groups, parameter validation (type, enum, min/max, minLength/maxLength, pattern, required, unknown).
- [x] 2.6 Executor: binary resolution (PATH / absolute / workspace-relative), `exec.CommandContext` with argv, env building (`inherit`, `pass`, `set` with `${env.NAME}`), cwd, stdin, timeout clamp, process-group kill, stdout/stderr capture, exit code, `ExecResult`.
- [x] 2.7 `--help` capture at load (10 s, byte cap, truncation marker) appended to the description.
- [x] 2.8 Unit tests for 2.1–2.7 using `/bin/echo`, `/bin/sh`-free fixtures (`env`, `printf`, a test helper binary via `go test` re-exec) covering every spec scenario in the policy, template, env and timeout requirements.

## 3. Native tool and permission layering

- [x] 3.1 `permission.EvaluateToolPermissionWithDefault(tool, input, agentPerms, globalPerms, toolDefault)` + registry helper `EvaluatePermissionWithDefault`; unit tests for the layer order.
- [x] 3.2 `internal/llm/tools/clitool.go`: `BaseTool` wrapper (`Info` from manifest, argv/structured schemas, `Run` = render → policy → permission → exec → format/spill via `PersistLargeOutput`, metadata with exit code and duration, `IsBaseline()==false`, `AllowParallelism` true).
- [x] 3.3 Register in `NewToolSet` next to MCP tools: `grant: explicit` → `IsToolExplicitlyEnabled` / allowlist, `implicit` → `IsToolEnabled`; wrap with `maybeDefer`; warn on MCP name collision when the toolset resolves.
- [x] 3.4 Toolset tests: explicit vs implicit gating, `"*": true` does not grant explicit tools, allowTools path, deferral wrapper applied, no manifests → unchanged toolset.

## 4. CLI commands

- [x] 4.1 `cmd/tools.go`: `opencode tools` group with `--cwd`, `--debug`; `list [--json] [--agent] [--strict]` output per spec.
- [x] 4.2 `opencode tools serve [--only]`: mcp-go stdio server, tools from manifests, `tools/call` through the shared policy/executor, deny → `isError`, stderr logging.
- [x] 4.3 Tests: `list --json` against a temp workspace (valid, invalid, shadowed, NOT FOUND); `serve` handshake + `tools/list` + `tools/call` over an in-process stdio pipe, including a policy rejection.

## 5. Documentation and schema hygiene

- [x] 5.1 `docs/cli-tools.md`: manifest reference, modes, policy semantics and the two boundaries (what the model may ask vs what the process can do), grant and permission layering, deferral, `tools list|serve`, Claude Code `.mcp.json` recipe, the `snow` manifests as worked examples.
- [x] 5.2 Cross-links in `docs/tool-permissions.md` and `docs/deferred-tools.md`; AGENTS.md docs-index row and a short "CLI tools" paragraph in the agent-configuration section; README config reference entry for `cliTools`.
- [x] 5.3 `make test` lint covers `cmd/tools.go` (go fmt + go vet over `./...`); no new schema enum, so no probe entry is needed.

## 6. End-to-end verification

- [x] 6.1 `scripts/test/cli_tools.sh` (executable): builds the binary, writes a workspace with an `echo`-backed argv manifest and a structured manifest, asserts `tools list --json`, `--strict` failure on a broken manifest, and a `tools serve` JSON-RPC round trip including a denied argument.
- [x] 6.2 Manual proving run with the real `snow` 3.28.0 and the local `dcs` connection: `snow.yaml` (argv) and `snow_dcs.yaml` (structured) in a scratch workspace; `opencode tools list`, a served `tools/call` running `select 1`, a denied `-x`, and a structured call with an invalid `format`; record the transcript in the GENAI-411 issue.
- [x] 6.3 `make test` green (53 packages, race); `make schema-check` green; `./scripts/check_hidden_chars.sh` flags only the pre-existing generated `internal/lsp/protocol/tsprotocol.go`.

## 7. Downstream follow-ups (outside this repository, tracked in GENAI-411)

- [ ] 7.1 piano-developer MR: `.agents/tools/snow.yaml`, `snow_dcs.yaml`, `snow_logging.yaml`; `.gitignore` re-include; CI `yaml-configs-parse` + `opencode tools list --strict` job; `piano-snowflake-explorer` / `piano-incident-investigator` switched to the tools with `bash` withheld; `.agents/mcp/servers.json` `cli-tools` entry (`opencode tools serve`) and `docs/LOCAL-MCP.md`; `sync_claude_agents.py` mapping of granted CLI tools to `mcpServers: [cli-tools]`; `piano-snowflake-cli` skill updated to name the tools.
- [ ] 7.2 c2-agent: bump the opencode pin to the release carrying this change.
- [ ] 7.3 scenario-builder: follow-up to !180 replacing `"snow *": allow` bash maps with the structured tools once 7.2 is live.
