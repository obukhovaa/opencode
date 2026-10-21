# Tasks

## 1. Deny list and filter

- [x] 1.1 Add `ToolEnvConfig{Deny []string}` as `Config.ToolEnv` (`toolEnv`), declare it in `cmd/schema/main.go`, regenerate `opencode-schema.json`; verify `make schema-check` and `go test ./cmd/schema/...` pass and a viper round-trip test under `internal/config` reads `toolEnv.deny`
- [x] 1.2 Add `internal/toolenv` with the built-in names, `DenyList()` (built-ins ∪ config ∪ `OPENCODE_TOOL_ENV_DENY`), `Filter(env, patterns)` and `Environ()`; verify table tests cover exact, glob, malformed glob, case sensitivity, trimming/empty entries, nil config and duplicate `KEY=` entries

## 2. Spawn sites

- [x] 2.1 Persistent shell, background bash, `monitor` and the TUI `!` shell use `toolenv.Environ()`; verify a shell test that runs `env` with `OPENCODE_SERVER_PASSWORD` and a config-denied name set sees neither, and that an undenied variable still passes
- [x] 2.2 CLI tools build from the filtered parent (`exec.go`, `help.go`); verify `BuildEnv` tests: `inherit`, `env.pass` and `${env.NAME}` expansion drop a denied name while an `env.set` literal is kept
- [x] 2.3 Hooks, stdio MCP servers, LSP client and installers use the filtered environment with their explicit `env` appended; verify a hooks runner test that a hook's `env` output lacks the denied name and still has `OPENCODE_PROJECT_DIR`
- [x] 2.4 Document the deny list in `README.md` (config key and `OPENCODE_TOOL_ENV_DENY`), `docs/hooks.md` and `docs/cli-tools.md`; verify the documented names match `internal/toolenv`

## 3. Own-process hardening

- [x] 3.1 On Linux, at startup, set `PR_SET_DUMPABLE=0` when any variable in opencode's environment matches the deny list, with a no-op on other platforms; verify a Linux-tagged test that a child cannot read the parent's `/proc/<pid>/environ` after hardening, and that the decision function returns false with no denied variable present

## 4. Integration

- [ ] 4.1 Add `scripts/test/tool-env-scrub.sh` that drives the bash tool against a built binary with denied variables set and asserts they are absent from `env`; verify `make test-e2e` runs it green
- [ ] 4.2 Run `make test`; verify no failures beyond those already present on `main`

## Workflow follow-up

- Archive the change into `openspec/specs/` after review and merge.
