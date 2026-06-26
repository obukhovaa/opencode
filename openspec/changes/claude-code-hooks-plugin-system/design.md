# Design — claude-code-hooks-plugin-system

## Context

Our Go opencode fork has no extension surface for external processes to inspect or mutate tool calls. The motivating use case is **RTK** (https://github.com/rtk-ai/rtk) — a Rust binary that compacts noisy command output (`cargo test` 200 lines → 20 lines) by rewriting Bash invocations before they run. RTK works against Claude Code today via `PreToolUse` hook + Bash matcher; against dax opencode via the `tool.execute.before` TS plugin hook.

The dax extensibility model is **TypeScript plugin modules loaded with Bun's dynamic `import()`** — 18+ hooks on the agent lifecycle, in-process, sharing Bun-specific APIs (`Bun.$`, npm). Faithfully mirroring it requires either embedding Bun as a sidecar or embedding a JS engine in Go. Both add significant binary and operational weight.

The Claude Code model is **POSIX subprocesses + JSON over stdin/stdout** — declared in `settings.json`, matched by tool name (regex or exact), with exit codes that double as decision signals (0 = process JSON, 2 = block + stderr). Plugins are language-neutral: RTK is Rust, others might be Python, shell, or Go. No runtime dependency beyond a POSIX shell.

We pick Claude Code's model because (a) it's already what RTK natively supports; (b) it has zero language-runtime constraints; (c) it's drop-in for any user migrating from Claude Code; (d) it composes cleanly with our existing Go agent loop without any new in-process abstraction (just `os/exec`).

Existing in-tree patterns we'll mirror:

- **`bridge.BridgeSender`** late-injection (`internal/llm/agent/factory.go:84`) — interface satisfied at runtime to break import cycles.
- **`flow.InteractiveHook`** (`internal/flow/interactive.go:36`) — a single-method interface with a no-op default, set on a service after construction.
- **`pubsub.Broker[T]`** (`internal/pubsub/broker.go:10`) — generic typed event broker. We'll NOT use this for hooks (hooks are synchronous decisions, not fire-and-forget events), but it's the relevant precedent for "service-internal events with multiple subscribers."
- **Context keys** (`internal/llm/tools/tools.go:39-51`) — `SessionIDContextKey`, `MessageIDContextKey` etc. are already threaded into every tool call; we'll reuse these to populate the hook's JSON `session_id`.

The agent's tool-dispatch chokepoint is single: `internal/llm/agent/agent.go:834` — `e.tool.Run(permCtx, tools.ToolCall{...})`. PreToolUse fires immediately before this line; PostToolUse fires immediately after the result is captured at lines 849-851.

## Goals / Non-Goals

**Goals:**

- A configured RTK installation (per its documented `rtk init` flow against Claude Code) loads in our fork without modification and rewrites Bash invocations during agent runs.
- POSIX-subprocess plugin contract: any language, any binary, JSON in/out over stdio.
- Drop-in compat with the Claude Code `settings.json` `hooks` block for the events implemented in v1 (`PreToolUse`, `PostToolUse`).
- Settings reload on every event — operators iterating on hooks don't need session restart.
- Hook failures (timeout, crash, malformed output, command-not-found) NEVER crash the agent loop or stall tool dispatch.
- Extensible: adding more event names (`SessionStart`, `UserPromptSubmit`, `Stop`, `PreCompact`, etc.) or hook types (`http`, `mcp_tool`, `prompt`, `agent`) in v2 requires only new call-site insertions + new dispatcher branches, NOT re-architecting the hook registry.

**Non-Goals:**

- TypeScript / JS plugins. No Bun, no V8, no goja, no `Bun.$`. Plugins are arbitrary executables.
- Full Claude Code hook-event surface in v1. Only `PreToolUse` and `PostToolUse` ship; the other ~25 events are scoped out and live as a follow-up.
- `http`, `mcp_tool`, `prompt`, `agent` hook types. v1 ships `command` only.
- `if`-rule filtering (the Bash-aware sub-matcher Claude Code uses). v1 matcher is tool-name-only.
- Plugin marketplace, install command (`opencode plugin install X`), version pinning, lockfile. Plugins are installed by whatever means the plugin author documents (e.g. RTK ships its own installer); we only consume their declared `command` path.
- Async hooks (`"async": true`), `disableAllHooks`, `terminalSequence`, `CLAUDE_ENV_FILE`.
- TUI plugin surface (the dax `TuiPlugin` API). Out of scope; not relevant to RTK.
- Skill/agent-frontmatter hooks (`hooks:` block in YAML frontmatter). v1 only loads from settings.json.
- A native Go plugin SDK (typed Go interface for in-process plugins). Subprocess-only is the v1 contract; a Go SDK can be added later as a thin wrapper that converts Go callbacks into the same JSON protocol.

## Decisions

### D1: Subprocess-per-event over long-running plugin daemons

Each hook fire spawns a fresh subprocess. No long-running daemon, no socket, no shared process state across events.

**Why:**

- Matches Claude Code semantics exactly — RTK and other Claude-Code-targeted plugins are written assuming per-event spawn.
- Zero state-management complexity in the runtime. No "is the plugin process still alive?" check, no socket reconnect logic, no plugin-side IPC framework.
- Per-call latency is bounded by `os/exec` overhead (~1-5ms on darwin/linux). RTK's documented overhead is "<10ms" — well within typical LLM-call wall-clock.
- Crash isolation: a plugin segfault terminates that one subprocess; the agent loop is unaffected. A long-running daemon's crash would block the next tool call until reconnect.

**Rejected alternative:** A long-running plugin process speaking gRPC (Hashicorp `go-plugin` style). Better latency for high-frequency hooks, but: (a) requires plugin authors to implement a specific protobuf service, breaking the "any executable" promise; (b) loses bytewise compat with Claude Code's stdio JSON contract; (c) adds reconnect + supervision machinery to the runtime.

### D2: Claude Code's JSON schema is the canonical event contract, not opencode-dax's

The JSON keys (`session_id`, `tool_input`, `hookSpecificOutput.permissionDecision`, etc.) match Claude Code's published schema. We do NOT emit dax-style `tool.execute.before` event names or input shapes.

**Why:**

- RTK already speaks this protocol. Mirroring it gives us zero-config RTK integration.
- Claude Code is the larger plugin ecosystem; more authors target it than dax.
- The two schemas are very similar (dax's `tool.execute.before` takes `{tool, sessionID, callID}` returning `{args}` — almost the same shape with different names). Picking one avoids serving both and trying to translate.

**Rejected alternative:** Define our own event names + map both dax and Claude Code conventions onto them. Cleaner separation of concerns in theory, but doubles the API surface, doubles the documentation burden, and means every plugin must check which "host" it's talking to.

**Compat note for dax-coming users:** A future change can add `tool.execute.before` / `tool.execute.after` as aliases for `PreToolUse` / `PostToolUse` if there's demand. The internal registry is keyed by canonical event name (Claude Code's), so aliasing is straightforward.

### D3: Hooks load once with the rest of `.opencode.json`, not re-read per event

Hooks are part of the `Config` struct and load with the rest of `.opencode.json` at process startup, via the existing viper-based loader. No per-event re-read, no file watcher, no separate cache.

**Why:**

- Aligns with the rest of `.opencode.json`. Operators already know that editing the config file requires a restart — extending that contract to hooks keeps the mental model uniform. Running a per-event reload for one field would have authors second-guessing which fields are live vs cached.
- One config loader, one cache, one source of truth. The earlier (pre-revision) design ran a parallel "read on every event" mechanism alongside viper; that was an extra moving part with extra failure modes (e.g. malformed file at runtime vs at startup).
- Edit-then-run iteration during plugin development is still cheap — running `opencode` is a fresh process for non-interactive flows, and TUI users can `q` + restart in well under a second.

**Divergence from Claude Code (documented):** Claude Code re-reads `settings.json` on every event so authors can edit-and-test inside a long-lived session. Our fork does NOT do this. Documented in `docs/hooks.md`.

**Rejected alternative:** Watch `.opencode.json` with `fsnotify` and invalidate the cache on change. Better dev-iteration UX, but introduces a long-lived watcher goroutine, cross-platform path quirks, and the ambiguous question of "what other config fields should also live-reload?" If we add live reload later, it should cover the whole config, not just hooks.

### D4: Hooks live inside `.opencode.json`, not a new settings file

The hooks block lives under a top-level `hooks` key inside the existing `.opencode.json` file. Two scopes — global (`$HOME/.opencode.json` etc.) and project (`<workingDir>/.opencode.json`) — matching the existing config's discovery rules exactly. Viper deep-merges maps and replaces arrays.

**Why:**

- One file to manage. Avoiding a parallel `settings.json` means users don't track two config files, two merge precedences, two reload behaviors. The cognitive cost of "where does this setting live?" stays low.
- Existing scope rules apply unchanged. Operators already know how `.opencode.json` merges (global then project, viper deep-merge); hooks inherit that behavior verbatim. There's nothing new to learn about merge semantics.
- The JSON SHAPE of the `hooks` block stays identical to Claude Code's, so a user copy-pastes the `hooks` object from Claude Code's `settings.json` directly into `.opencode.json`. The PATH changes; the contents do not.

**Divergence from Claude Code (documented):** Claude Code's per-event arrays CONCATENATE across user / project / project-local scopes. Our viper merge REPLACES arrays — project `hooks.PreToolUse` overrides global `hooks.PreToolUse` outright. Operators wanting "user hook always runs plus project hook adds to it" must merge the array manually in the project config. Documented in `docs/hooks.md`.

**Rejected alternative:** A new `settings.json` file (the v1 draft of this design). Mirrors Claude Code's exact scope model and array-concatenation, but at the cost of introducing parallel loader infrastructure and a second file users have to track. The user explicitly chose the simpler "one config file" approach for this revision.

### D5: Matcher syntax is content-detected; exact/list comparison is case-insensitive to bridge naming conventions

If the matcher string contains only `[A-Za-z0-9_, |]` it's parsed as an exact name or a separator-delimited list. Comparison against the tool name lowercases both sides — opencode's canonical tool names are lowercase (`bash`, `edit`, `write`, …), Claude Code documents matchers in PascalCase (`Bash`, `Edit`, …), and we want both forms to work without authors having to translate. Anything outside the simple character set is compiled as a Go RE2 regex and stays case-sensitive unless the author opts in via the `(?i)` inline flag.

**Why:**

- opencode's tool registration is lowercase (`BashToolName = "bash"`, `EditToolName = "edit"`, etc., per `internal/llm/tools/*.go`). A case-sensitive matcher would mean every Claude-Code-authored config breaks on first paste (`"Bash"` would never match the real tool name `bash`). Operators would silently get no hook firing and waste time debugging.
- Case-insensitivity for the simple form is unambiguous: the domain is "tool name", which is always one identifier word, no acronym overloading, no risk of collision. There's no `Bash` tool AND a `bash` tool — they're the same.
- Regex is left case-sensitive because case-folding a regex automatically would mean (a) overriding the author's explicit `(?-i)` flag with hidden behavior, and (b) RE2 has Unicode case-folding semantics that change matching for non-ASCII patterns. The standard `(?i)` flag is the documented escape hatch and matches operator expectations from every other regex tool.
- Direct parity with Claude Code's syntax. Plugin authors copy-paste matchers between hosts; what they pasted works.
- Go's `regexp` package (RE2) differs from Claude Code's V8 regex in lookahead/lookbehind support. Plugin authors targeting both should stay on the common subset (anchors, character classes, `.*`, `?`, `+`).

**Rejected alternative:** require operators to translate Claude Code's `"Bash"` → `"bash"` manually. Breaks the copy-paste promise that motivates the whole change. Operators who don't know about the naming divergence get a silent "hook never fires" failure mode.

**Rejected alternative:** treat the entire matcher (including regex) as case-insensitive. Surprises authors who explicitly wrote a case-sensitive regex; introduces ambiguity in MCP tool name matching (`mcp__MEMORY__create_entities` vs `mcp__memory__create_entities` — server names CAN have mixed case).

### D6: Decision precedence within a single PreToolUse event

When multiple hooks fire for the same `PreToolUse` event and multiple return decisions:

1. **Deny wins.** If ANY hook returns `permissionDecision: "deny"` (or exits 2), the tool MUST NOT execute. The reason / stderr from the first denying hook is used.
2. **Allow stacks under deny.** A hook returning `permissionDecision: "allow"` does NOT override a later `deny`; deny always wins.
3. **`updatedInput` is applied in order.** Each hook receives the OUTPUT of the previous hook's `updatedInput` (if any) as its own `tool_input`. This mirrors Claude Code's documented "each receives the previous's mutated output" behavior.
4. **`additionalContext` accumulates.** Each hook's `additionalContext` is appended to a single buffer that gets handed to the agent.

**Why:**

- Deny-wins is the safe-by-default precedence. A user adds a corporate-security hook that blocks `rm -rf`; an experimental project hook adding `--dry-run` cannot accidentally override the block.
- Sequential `updatedInput` chaining lets multiple plugins compose (e.g. RTK rewrites + an audit-logger appends a comment), which is how Claude Code documents the behavior.

**Rejected alternative:** First-match-wins (only the first matching hook runs). Simpler but kills composition — a user could only run one PreToolUse hook per tool, which defeats most plugin layering.

**PostToolUse multi-hook precedence (analogue of D6 for PostToolUse):**

1. **`updatedToolOutput` chains.** Each hook receives the previous hook's `updatedToolOutput` (if any) as its own `tool_output`. The last hook's value is what reaches the conversation history.
2. **First-block-wins on exit 2.** When multiple PostToolUse hooks exit 2, the FIRST hook's stderr becomes `BlockReason` (matching PreToolUse's first-deny-wins). Later exit-2 hooks do NOT overwrite the reason. `BlockReason` takes precedence over any `UpdatedOutput` set later in the chain — exit-2 is the dominant signal.
3. **`additionalContext` accumulates** across hooks in the same `\n`-joined order as PreToolUse.
4. **Empty-string vs absent `updatedToolOutput`:** `UpdatedToolOutput` is decoded as `*string` so a hook can fully suppress noisy output by emitting `{"updatedToolOutput": ""}` (a real replacement with empty), distinguishable from omitting the field (no change).

### D7: Hooks live in a new `internal/hooks/` package, depended on by `internal/llm/agent/`

```
internal/hooks/
  registry.go    // type Registry, Load(scopes []string) Registry, Run(eventName, payload) Decision
  runner.go      // subprocess spawn, stdin write, stdout/stderr capture, timeout
  matcher.go     // matcher compilation + match
  events.go      // canonical event-name constants, JSON shapes per event
  config.go      // settings.json schema loader (additive — does NOT modify config.Config)
```

`internal/llm/agent/factory.go` gains a `WithHookRegistry(*hooks.Registry)` option (matching the existing `WithBridgeSender` pattern). The registry is constructed once in `cmd/serve.go` and `cmd/root.go` after config load, passed into the factory.

**Why a new package, not inside `internal/llm/agent/`:**

- The hook system has no dependency on the agent — it's a pure subprocess dispatcher. Putting it under `agent/` creates an unnecessary directional dependency and makes the future case of "fire `SessionStart` hooks from somewhere outside the agent" awkward.
- Mirrors the layout of `internal/flow/`, `internal/bridge/`, `internal/permission/` — siblings under `internal/`, each owning its own lifecycle.

**Why config loading is local to the package, not in `internal/config/`:**

- `settings.json` is a NEW file shape orthogonal to `.opencode.json`. Adding it to `internal/config/config.go` would either (a) require `config.Config` to grow a `Hooks` map directly (couples config to hook internals) or (b) add a separate loader inside `config/` that nothing else uses. Keeping the loader in `internal/hooks/config.go` is the right scope.
- The settings file format is partly Claude-Code-spec-defined (we don't fully control it). Isolating it lets us track upstream changes without churning the central `Config` struct.

### D8: PreToolUse short-circuits BEFORE the existing permission check

When `PreToolUse` returns `permissionDecision: "allow"` or `"deny"`, the runtime SKIPS the standard `permissionService.Request` call. When it returns `"ask"` or no decision, the standard permission flow runs as today.

**Why:**

- Matches Claude Code semantics — the hook is documented as a permission override, not an additional gate.
- A configured RTK or security-policy hook should be the authoritative voice for those tool calls; making it advisory means operators would also need to update the in-app permission config, doubling configuration.

**Side effect:** A `deny` hook bypasses the auto-approve session list. This is intentional — a denial from policy should not be silently allowed because the agent happened to be in an auto-approved session. The reason is surfaced to the agent so it understands why the call failed.

## Risks / Trade-offs

[Per-call subprocess spawn cost] → `os/exec` overhead is 1-5ms per spawn on darwin/linux. Acceptable for tool-call frequency (typically <20 tool calls per agent turn, ~100ms aggregate). If a deployment shows hot-loop hook activity (1000s of calls/sec), we'd need a daemon mode, but that's not a v1 concern.

[Malicious / runaway hooks] → Hooks run with the user's privileges and shell. A hostile plugin could rm files, exfiltrate data, run a fork bomb. This is the same trust model Claude Code documents — installing a hook is equivalent to installing a shell script that runs on your machine. The 600s default timeout + 2s SIGKILL grace bounds runaway processes; size caps on stdout/stderr bound memory. We will NOT sandbox (no seccomp, no namespaces, no chroot) — that's a security feature scoped to a future change.

[Plugin authors targeting Claude Code's full event surface get partial fire-coverage] → Plugins relying on `SessionStart`, `UserPromptSubmit`, `Stop`, etc., will silently not-fire in our fork because we only implement `PreToolUse` and `PostToolUse` in v1. Claude Code documents the same "events not configured don't fire" behavior, so plugins should already degrade gracefully. We'll add events incrementally per demand.

[Regex divergence from V8] → Go's RE2 doesn't support lookahead/lookbehind. Most matcher patterns we see in the wild (Claude Code docs, RTK config) use only the common subset, so this matters for a long tail of plugins, not RTK. Documented as a known divergence.

[Stdio JSON size caps could truncate legitimate output] → 1 MiB stdout / 64 KiB stderr. RTK's redactor returns ~20 lines (~few KiB) — well within. A plugin emitting truly large content (e.g. a full code-quality report) would need to write to a file and inject a pointer via `additionalContext`. Documented in the spec.

[Hook latency directly slows tool calls] → A 1-second hook adds 1 second per matching tool call. For high-frequency tools (`Read`, `Grep`), this could compound. Mitigation: matchers default to fire only on the tools the plugin needs (RTK only on `Bash`). v2 can add `async` for fire-and-forget hooks.

[Concurrent tool calls and shared hook state] → If a plugin maintains state in a file or uses global system state, parallel tool calls could race. v1 says nothing about parallelism — Claude Code agent loop is sequential-per-turn, and our agent loop also dispatches tools sequentially within a turn. If the fork ever adds parallel tool dispatch, hooks need re-evaluation.

[`updatedInput` schema mismatch with the tool's parameter schema] → A poorly-written hook could return an `updatedInput` that doesn't conform to the tool's expected parameter schema (e.g., adding a non-existent field). The tool will then either ignore it or error. We do NOT validate `updatedInput` against the tool schema in v1 — that's the plugin author's responsibility and matches Claude Code. A future change could add schema validation with a `permissionDecision: "deny"` fallback on mismatch.

[Hook order across settings scopes is observable behavior] → User → project → local concatenation order is documented in the spec, but changing it later would break authors who happened to rely on it. We commit to it.

## Migration Plan

This change is purely additive. No migration needed. Users who don't configure a `hooks` block get exactly today's behavior.

Rollback strategy: ship behind an implicit "is `hooks` block present" gate. If a critical bug surfaces, operators delete the `hooks` block from their settings to disable the runtime entirely without redeploying.

Acceptance test gate before merge:
1. Install RTK locally per its docs (`rtk init -g`).
2. Run a real flow (`opencode -F some-flow`) that invokes `cargo test` or `git status` via Bash.
3. Confirm via debug logs that the `PreToolUse` hook fires, the `tool_input.command` is rewritten to include `rtk`, and the resulting tool output is RTK-compacted.
4. Run the same flow with the `hooks` block deleted; confirm behavior is identical to before this change.

## Open Questions

None blocking. v2 questions tracked here for visibility:

- Should we ship a `opencode hooks list` debug command (parity with Claude Code's `/hooks` slash command)? Likely yes in v2; doesn't change v1 architecture.
- Do we want hook output (stdout/stderr/decision) surfaced on the `/event` SSE stream for orchestrators? Likely yes for operability; needs spec follow-up.
- Should `additionalContext` be limited in length, or de-duplicated across hooks firing on the same event? Defer until we see real plugins using it.
