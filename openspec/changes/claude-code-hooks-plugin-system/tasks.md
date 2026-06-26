## 1. New `internal/hooks/` package — scaffolding and types

- [x] 1.1 Create `internal/hooks/events.go` with: canonical event-name string constants (`EventPreToolUse = "PreToolUse"`, `EventPostToolUse = "PostToolUse"`), and the per-event JSON request/response Go structs (`PreToolUseInput`, `PreToolUseOutput`, `PostToolUseInput`, `PostToolUseOutput`) with `encoding/json` tags matching the exact field names documented in `specs/hook-runtime/spec.md` (snake_case input, camelCase output).
- [x] 1.2 Create `internal/hooks/config.go` defining the settings.json hook block: `type Settings struct { Hooks map[string][]MatcherGroup \`json:"hooks"\` }`, `MatcherGroup`, `HookEntry` (with `type`, `command`, `args`, `timeout`, `shell` fields). Include a `LoadSettings(paths []string) ([]MatcherGroup, error)` that opens each path, decodes the `hooks` block from each, and CONCATENATES groups across files (preserving file order) — matching D4 / spec requirement 1.
- [x] 1.3 Create `internal/hooks/matcher.go`: `type Matcher func(toolName string) bool`, a constructor `CompileMatcher(raw string) (Matcher, error)` that implements the three-mode detection (empty/`*` → match-all; only `[A-Za-z0-9_, |]` → exact/list; else → `regexp.MustCompile`). Use a precompiled `^[A-Za-z0-9_, |]+$` regex to detect the "simple" form.
- [x] 1.4 Create `internal/hooks/runner.go`: `type Runner` with `Run(ctx, eventName, payloadJSON, entries []HookEntry) []Result` that for each entry: spawns subprocess (shell-form if `args` empty, exec-form otherwise), writes JSON to stdin and closes, captures stdout (1 MiB cap) + stderr (64 KiB cap), enforces timeout (default 600s, per-entry override) with SIGTERM then SIGKILL after 2s grace, returns per-entry `Result{ExitCode int, Stdout, Stderr []byte, Err error, Duration time.Duration}`. Set `OPENCODE_PROJECT_DIR` + `CLAUDE_PROJECT_DIR` env vars; inherit parent env otherwise.
- [x] 1.5 Create `internal/hooks/registry.go`: `type Registry` exposing `func Load(settingsPaths []string) (*Registry, error)` and the synchronous decision API `func (r *Registry) RunPreTool(ctx, sessionID, cwd, toolName string, toolInput map[string]any) PreToolDecision` + `func (r *Registry) RunPostTool(ctx, sessionID, cwd, toolName string, toolInput map[string]any, toolOutput string) PostToolDecision`. Registry holds `settingsPaths` (re-read each call per D3) — no cache. `PreToolDecision` carries: `Block bool`, `BlockReason string`, `UpdatedInput map[string]any` (nil if unchanged), `AdditionalContext string`. `PostToolDecision`: `UpdatedOutput *string` (nil if unchanged), `AdditionalContext string`, `BlockReason string`.
- [x] 1.6 Implement decision precedence per D6 inside `RunPreTool`: iterate matched hooks in concatenation order; thread each hook's `updatedInput` into the next hook's `tool_input`; if any hook returns `permissionDecision: "deny"` or exits 2, set `Block=true` with the first such reason and continue running remaining hooks (their `updatedInput` still applies if we end up not blocking — but Block wins overall); accumulate `additionalContext` into a `\n`-joined buffer.
- [x] 1.7 Implement the same precedence rules for `RunPostTool` (no allow/deny — only `updatedToolOutput` + `additionalContext` chain). Document that block-via-exit-2 for PostToolUse replaces output with the stderr text.
- [x] 1.8 Wrap every subprocess invocation in `defer recover()` so a panic in JSON-parsing logic, env setup, etc., never reaches the agent loop. Log panics at ERROR with stack trace; treat as non-blocking.

## 2. Settings file discovery

- [x] 2.1 Add `internal/hooks/paths.go` with `func DefaultSettingsPaths(projectRoot string) []string` returning `[~/.config/opencode/settings.json, <projectRoot>/.opencode/settings.json, <projectRoot>/.opencode/settings.local.json]` in user → project → local order. Use `os.UserConfigDir()` and gracefully skip a path if the user-config dir can't be resolved (rare; log debug, continue).
- [x] 2.2 In `LoadSettings`, treat a missing file as "no hooks from this scope" (not an error). Treat a malformed file as an error logged at WARN — load other scopes successfully so one broken file doesn't kill the rest.

## 3. Wire registry into the agent factory

- [x] 3.1 Extend `internal/llm/agent/factory.go::NewAgentFactory` parameters with `*hooks.Registry` (or a setter `SetHookRegistry` mirroring `SetBridgeSender`). Default is nil — when nil, all hook code paths must short-circuit to "no-op, run tool normally."
- [x] 3.2 In `cmd/serve.go` and `cmd/root.go`, after `config.Load` and `app.New`, construct the registry via `hooks.Load(hooks.DefaultSettingsPaths(cwd))`. On load error, log WARN and proceed with `nil` registry (hooks effectively disabled — better than blocking opencode startup over a malformed user file).
- [x] 3.3 Pass the registry from `app.App` into the agent factory via `factory.SetHookRegistry(reg)`. Confirm there are no import cycles between `internal/hooks/` and `internal/llm/agent/`; if any arise, lift the necessary type to a third package (`internal/hooks/types`).

## 4. PreToolUse integration

- [x] 4.1 In `internal/llm/agent/agent.go::streamAndHandleEvents`, around the existing `e.tool.Run(permCtx, tools.ToolCall{...})` site at line ~834 (verify line# at implementation time), insert: extract `sessionID`/`cwd`/`toolName`/`toolInput` from current scope and context, call `factory.Hooks().RunPreTool(...)` IF non-nil registry.
- [x] 4.2 Apply `PreToolDecision`:
  - If `Block`: synthesize a tool result whose content is `BlockReason`; skip the `e.tool.Run` call entirely; emit the result via the same path a regular tool result takes; continue the agent loop.
  - Else if `UpdatedInput != nil`: replace `ToolCall.Input` with `UpdatedInput` (serialize via `json.Marshal` to keep the parameter map compatible with what the BaseTool expects).
  - Append `AdditionalContext` (if non-empty) to wherever the agent's "scratch context for next turn" lives; if no such buffer exists yet, route it as a synthesized assistant-visible content piece (simplest: append to the tool result's content as `\n\n[hook context: <ctx>]`).
- [x] 4.3 Confirm the existing permission check still runs IF and ONLY IF the hook returned no decision (i.e., `Block=false` AND no explicit allow override). Per D8: hook `allow` skips the permission check; hook `deny`/`Block` short-circuits before permissions; missing decision falls through to the existing permission flow.

## 5. PostToolUse integration

- [x] 5.1 In `internal/llm/agent/agent.go`, after the `toolResult, toolErr = res.resp, res.err` capture at line ~849-851, insert: IF `toolErr == nil` AND the registry is non-nil, call `factory.Hooks().RunPostTool(...)` with the result's content as `tool_output`.
- [x] 5.2 Apply `PostToolDecision`:
  - `UpdatedOutput != nil`: replace `toolResult.Content` (or the equivalent field) with `*UpdatedOutput` before the existing `record(...)` calls (lines ~864/879/892).
  - `AdditionalContext` non-empty: append to the result content as documented above (same convention as PreToolUse).
  - `BlockReason` non-empty (exit-2 path): replace tool output with the block reason text.
- [x] 5.3 Verify the conversation history that gets sent to the LLM next turn observes the mutated content, not the original. Inspect `msgHistory` flow in `processGeneration` and confirm `record(...)` writes the mutated value to the persistent message store.

## 6. Tests — registry / runner / matcher

- [x] 6.1 Create `internal/hooks/matcher_test.go` table-driven: exact name match, pipe-list match, comma-list with whitespace, `*` wildcard, empty string wildcard, regex match (`^mcp__.*`), regex that uses RE2-only features (succeeds), regex that uses lookahead (must return CompileMatcher error).
- [x] 6.2 Create `internal/hooks/runner_test.go`: spawn a real subprocess (use `os.Executable()` + a test-helper subprocess pattern, or a tempfile sh script) to exercise (a) success with valid JSON stdout, (b) exit 2 with stderr, (c) exit 1 with stderr (non-blocking), (d) timeout enforced — process killed within timeout+5s, (e) command-not-found returns spawn error result. Skip on `runtime.GOOS == "windows"` for v1.
- [x] 6.3 Create `internal/hooks/registry_test.go` for `LoadSettings`: (a) all three files present, hooks from each concatenate in correct order; (b) only user-scope present, only project, only local; (c) malformed JSON in one file → that scope is skipped with WARN, others load; (d) missing file is silent.
- [x] 6.4 Create `internal/hooks/decision_test.go` exercising `RunPreTool` precedence (D6): (a) two hooks chain `updatedInput` — second sees first's output as `tool_input`; (b) one hook returns `deny`, second returns `allow` → Block=true with first hook's reason; (c) all hooks succeed with no decision → Block=false, no UpdatedInput, no context; (d) one hook crashes (exit 1) — second still runs; (e) one hook times out — second still runs.
- [x] 6.5 Create `internal/hooks/post_decision_test.go` for `RunPostTool`: (a) chained `updatedToolOutput`; (b) exit-2 replaces output with stderr; (c) `tool_output` correctly serialized from a string with embedded newlines + JSON-escapable characters.

## 7. Integration tests — agent loop

- [x] 7.1 Create `internal/llm/agent/agent_hooks_test.go` (or wherever the existing agent tests live) covering the end-to-end PreToolUse path: stub a tool (`Bash`-like) + a hook command (tempfile sh script that rewrites command); construct an agent with a registry that points at a settings.json fixture; invoke the agent's tool dispatch; assert the stubbed tool received the REWRITTEN input.
- [x] 7.2 Add a sibling test: hook returns `permissionDecision: "deny"` — assert the stubbed tool's Run method was NEVER called and the agent's tool result contains the reason.
- [x] 7.3 Add a PostToolUse end-to-end test: stub a tool that returns a 1000-line string; hook truncates to 20 lines via `updatedToolOutput`; assert the conversation history (`msgHistory` or equivalent) contains the 20-line version.
- [x] 7.4 Add a "no hooks configured" test confirming agent behavior is identical (byte-equal) to a build without the registry — guards against accidental code-path divergence when settings.json is absent.

## 8. RTK acceptance gate

- [x] 8.1 Build the fork: `go build ./...`. Confirm no new dependencies beyond the standard library entered `go.mod` (no JS engine, no IPC framework).
- [ ] 8.2 Install RTK locally per its README (`brew install rtk-ai/tap/rtk` or equivalent). Run `rtk init` and let it produce a Claude Code `settings.json` snippet.
- [ ] 8.3 Copy the snippet's `hooks` block verbatim into `<project>/.opencode/settings.json`.
- [ ] 8.4 Run a flow that exercises `Bash` heavily (e.g. a test-run flow). Confirm via debug logs that the `PreToolUse` hook fires for each `Bash` call and the resulting `tool_input.command` is rewritten with RTK's prefix.
- [ ] 8.5 Confirm the agent's conversation receives the RTK-compacted output rather than raw command output (verifies PostToolUse if RTK uses it, or that the rewritten Bash command itself produces compacted output).
- [ ] 8.6 Remove the `hooks` block from settings.json and re-run the same flow. Confirm behavior reverts cleanly to pre-hook behavior (no errors, no leftover state).

## 9. Documentation

- [x] 9.1 Create `docs/hooks.md` documenting: settings.json layout, event names supported in v1 (PreToolUse / PostToolUse only — list explicitly which Claude Code events are NOT yet supported), matcher syntax with examples, exit-code semantics, env vars, the RTK-style example.
- [x] 9.2 Add a "Hooks" section to the main `README.md` (or wherever extensibility lives today) cross-linking to `docs/hooks.md`.
- [x] 9.3 Add an example settings file at `docs/examples/hooks-settings.json` showing a minimal RTK-style Bash rewrite hook plus a redacting `PostToolUse` hook.
- [x] 9.4 Cross-link from `docs/flows.md` (where appropriate, e.g. near the existing tool-section) so flow authors can discover that hooks affect tool calls inside flow steps too.

## 10. Audit for hidden assumptions

- [x] 10.1 grep `internal/llm/agent/` for any direct calls to a tool's `Run` method outside `streamAndHandleEvents` — there should be none, but if any exist, decide whether they need hook integration (probably yes for parity).
- [x] 10.2 grep `internal/flow/` for any direct tool invocation that bypasses the agent — flow steps go through `agent.Run` via the agent service, which in turn dispatches tools through the instrumented path, so this should be fine; confirm by reading.
- [x] 10.3 Confirm `cfg.PermissionMode` interactions with `permissionDecision: "allow"` — a hook `allow` overrides the permission system entirely (D8); double-check no other code path (auto-approve session map, bridge router) would still consult permissions and produce a deny after the hook said allow.
- [x] 10.4 Run `make test` end-to-end. Run `go test ./internal/hooks/... -race -count=1`. Confirm all green.

## 11. Archive

- [ ] 11.1 After merge + RTK acceptance: `openspec archive claude-code-hooks-plugin-system` to move `openspec/changes/claude-code-hooks-plugin-system/specs/hook-runtime/` to `openspec/specs/hook-runtime/`.
- [ ] 11.2 Verify the docs cross-link in `docs/hooks.md` to the archived spec path resolves.
