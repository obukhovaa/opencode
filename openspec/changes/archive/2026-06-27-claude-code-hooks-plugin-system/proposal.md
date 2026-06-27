## Why

External tools like [RTK](https://github.com/rtk-ai/rtk) (a Rust binary that rewrites Bash commands such as `git status` → `rtk git status` to reduce token-heavy output) cannot integrate with our Go opencode fork today — we have no extension surface for third-party processes to inspect or mutate tool calls. Piano's CD-4682 motivates this directly: RTK would cut LLM-cost on noisy build/test output, but requires a `PreToolUse`-style hook the fork doesn't expose.

Dax opencode (TypeScript) and Claude Code each ship plugin/hook systems, but both impose unwanted runtime dependencies (Bun + TS for dax; a specific shell environment for Claude Code's full surface). We want **the Claude Code hook contract** (POSIX subprocesses, JSON over stdin/stdout, exit codes for decisions) implemented natively in Go — so any plugin authored for Claude Code (RTK first, others later) drops in unchanged, without Bun or a JS engine in our process. RTK works against Claude Code today via `PreToolUse` + Bash matcher + JSON `updatedInput`; mirroring that contract gets us drop-in compatibility for free.

## What Changes

- **New `hooks` block inside the existing `.opencode.json` config.** A top-level `hooks: {<EventName>: [{matcher, hooks: [{type, command, args, timeout, …}]}]}` map is added to `.opencode.json`. The JSON shape inside `hooks` matches Claude Code's `settings.json` `hooks` block byte-for-byte, so users copy-paste the `hooks` object out of a Claude Code config into `.opencode.json`. Scope rules follow the existing `.opencode.json` discovery: global (`$HOME/.opencode.json` etc.) plus project (`<workingDir>/.opencode.json`), merged via viper's deep-merge (maps deep-merged, arrays REPLACED — project array overrides global array outright). No new settings file is introduced.
- **`PreToolUse` and `PostToolUse` events** instrumented in `internal/llm/agent/agent.go` at the existing tool-dispatch chokepoints (around the `tool.Run` call and result capture). Hook subprocesses receive the same JSON shape Claude Code defines (`{session_id, cwd, hook_event_name, tool_name, tool_input, …}`) and can return `{hookSpecificOutput: {permissionDecision, updatedInput, additionalContext, updatedToolOutput}}`.
- **Subprocess hook runner** (`internal/hooks/`): POSIX-only spawn (`sh -c` on darwin/linux), JSON in on stdin, JSON out on stdout (parsed when exit 0), `stderr` surfaced when exit 2 (block). Timeout per hook with a 600s default (Claude Code parity). Env carries `OPENCODE_PROJECT_DIR` + `CLAUDE_PROJECT_DIR` alias so Claude-Code-targeted plugins (RTK) work without any plugin-side change.
- **Matcher syntax**: exact name (`bash`), pipe/comma lists (`edit|write`), and Go regex (`^mcp__.*`). Empty / missing / `*` matchers match all. Exact + list forms are case-insensitive so PascalCase matchers from Claude Code configs (`Bash`, `Edit|Write`) also match opencode's lowercase tool names without translation; regex stays case-sensitive with the standard `(?i)` opt-in.
- **Decision semantics**: exit 0 + valid JSON → process the decision; exit 2 → block (stderr passed to agent context); any other non-zero → non-blocking error logged.
- **Config reload**: hooks load once at process startup, alongside the rest of `.opencode.json`. Editing the config requires a restart, matching the existing contract for every other field in `.opencode.json`. This is a deliberate divergence from Claude Code's per-event reload; rationale documented in design.md.
- **OUT OF SCOPE for this change** (v1 is scoped to what RTK needs + a clean extension point): `http`-type hooks, `mcp_tool`-type hooks, `prompt`-type hooks, `agent`-type hooks, `if`-rule filtering, async hooks, `disableAllHooks`, plugin-shipped hooks (`hooks/hooks.json` from a plugin package), skill/agent-frontmatter hooks, `CLAUDE_ENV_FILE` env injection, `terminalSequence`, the `MessageDisplay` / `Notification` / `Stop` / `SessionStart` / `UserPromptSubmit` / `PreCompact` / `FileChanged` / `ConfigChange` events, MCP elicitation events, TUI integration. These remain available as follow-up changes that extend the same hook registry without re-architecting it.
- **NOT a TypeScript / JS / Bun runtime.** No `import()`, no embedded V8, no `Bun.$`. Plugins are arbitrary executables (any language) speaking JSON over stdio.

## Capabilities

### New Capabilities

- `hook-runtime`: settings-driven, POSIX-subprocess hook dispatcher mirroring the Claude Code hook contract. Owns the `hooks` config block, matcher engine, subprocess execution, JSON decision parsing, and the call-site integration into the agent's tool-dispatch loop. Scoped initially to `PreToolUse` / `PostToolUse` with `command`-type hooks; structured so additional events and hook types are additive.

### Modified Capabilities

(None. The agent loop and config loader gain hook integration but their existing requirements don't change — no current spec covers tool-dispatch lifecycle.)

## Impact

**Code**
- New: `internal/hooks/` package — `Registry`, `Runner`, `Matcher`, JSON event schema. Public types `MatcherGroup` and `HookEntry` are referenced from `internal/config/config.go` (same pattern as `bridge.Config` referenced by `Config.Router`).
- Modified: `internal/config/config.go` — add `Hooks map[string][]hooks.MatcherGroup` field to top-level `Config` (alongside existing `Router`, `Permission`, `Agents`). Loaded by the existing viper-based loader from `.opencode.json` files in the existing global + project scopes; no loader changes needed beyond declaring the field.
- Modified: `internal/llm/agent/agent.go` — invoke `hooks.Run("PreToolUse", …)` immediately before `e.tool.Run(...)` (current line ~834); invoke `hooks.Run("PostToolUse", …)` immediately after result capture (current lines ~849–851). Mutate `ToolCall.Input` from `updatedInput`; mutate `toolResult.Content` from `updatedToolOutput`; honor `permissionDecision: "deny"` and exit-2 blocking by short-circuiting with the hook's reason.
- New: example config snippet at `docs/examples/opencode-json-hooks.json` showing RTK-style Bash rewrite inside `.opencode.json`.

**APIs**
- No HTTP API change. The hook system is server-internal; nothing leaks onto `POST /flow`, `/event`, ACP, or other public endpoints.

**Configuration**
- New `hooks` block as a top-level key inside the existing `.opencode.json`. Backward-compatible: omitting it leaves agent behavior unchanged. No new file path conventions — `.opencode.json` discovery (global + project, viper-merged) covers it.

**Behavior changes for operators**
- A configured `PreToolUse` hook on `Bash` will see and may rewrite every `Bash` invocation. Slow hooks slow the agent loop; the default 600s timeout is generous so authors don't accidentally cap themselves.
- A `PostToolUse` hook can rewrite tool output before the LLM sees it (RTK's primary mode for compacting `cargo test` failure logs).
- Exit-2 blocking means a misbehaving hook can stop tool execution. The agent surfaces hook stderr to the model so the failure mode is debuggable inside the loop.

**Plugin compatibility**
- RTK's existing Claude Code config block (per its docs) is expected to load verbatim. The integration test for this change is "install RTK via its documented `rtk init` flow, run a Bash-heavy flow against our fork, confirm RTK's rewrite fires."
- Future Claude-Code-authored plugins targeting `PreToolUse` / `PostToolUse` work without modification. Plugins relying on events / hook types not yet implemented degrade by silently not-firing, which matches Claude Code's behavior when an event isn't configured.
