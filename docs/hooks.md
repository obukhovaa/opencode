# Hooks

opencode supports **Claude-Code-compatible hooks**: external processes invoked at agent lifecycle moments that can inspect, mutate, or block tool calls. The hook contract (event names, JSON-over-stdio, exit-code decisions, matcher syntax) mirrors [Claude Code's published spec](https://code.claude.com/docs/en/hooks) byte-for-byte for the events implemented here, so any plugin that documents its config as a `hooks` block — [RTK](https://github.com/rtk-ai/rtk), redactors, command rewriters — works in opencode with no plugin-side changes.

The only deliberate change vs Claude Code is **where the config lives**: the `hooks` block goes into the existing `.opencode.json` file. No new settings file to manage. Drop the `hooks` object out of a Claude Code `settings.json` straight into your `.opencode.json` and you're done.

## Status — what v1 supports

| Capability | v1 | Notes |
|---|---|---|
| `PreToolUse` event | ✅ | Inspect/mutate `tool_input`, deny tool call, override permission gate |
| `PostToolUse` event | ✅ | Mutate `tool_output` (RTK-style log compaction); only fires on successful tool calls |
| `command` hook type | ✅ | POSIX subprocess + JSON stdio + exit codes |
| Matcher syntax (exact / `\|` list / regex) | ✅ | RE2; lookahead/lookbehind unsupported (Go regex limitation) |
| `OPENCODE_PROJECT_DIR` + `CLAUDE_PROJECT_DIR` env vars | ✅ | Claude alias provided for drop-in compat |
| Timeout (default 600s, per-hook override) | ✅ | SIGTERM → SIGKILL after 2s grace; runs in own process group |
| Global + project scope merge via existing `.opencode.json` loader | ✅ | Viper deep-merge: maps deep-merged, arrays REPLACED |
| `http` / `mcp_tool` / `prompt` / `agent` hook types | ❌ | Settings entries with these types are loaded then silently skipped with WARN. Roadmap. |
| Other events (`SessionStart`, `UserPromptSubmit`, `Stop`, `PreCompact`, `FileChanged`, `ConfigChange`, `MessageDisplay`, `Notification`, MCP elicitation, etc.) | ❌ | Settings load cleanly; the events never fire. Plugins targeting them degrade gracefully — same behavior Claude Code documents for unconfigured events. |
| `if`-rule filtering inside a matcher group | ❌ | Roadmap |
| Live reload (edit `.opencode.json` mid-session) | ❌ | Restart required, matching the rest of `.opencode.json`'s contract. Claude Code reloads `settings.json` on edit; we deliberately don't. |
| `async` / `disableAllHooks` / `terminalSequence` / `CLAUDE_ENV_FILE` | ❌ | Roadmap |
| Claude Code plugin *package* format (`hooks/hooks.json`, skills, commands as one bundle) | ❌ | v1 supports the `hooks` block authored directly in `.opencode.json` only. Users installing a Claude Code plugin manually paste the plugin's documented `hooks` block. Bundle-format loader is a follow-up. |

## Where hooks live

Inside `.opencode.json` under a top-level `hooks` key. The existing two-scope discovery applies:

| Path | Scope |
|---|---|
| `$HOME/.opencode.json` (or `$XDG_CONFIG_HOME/opencode/.opencode.json` / `$HOME/.config/opencode/.opencode.json`) | Global — applies to every project |
| `<workingDir>/.opencode.json` | Project — applies to this project only |

When both scopes declare hooks, viper's standard merge rules apply: maps are deep-merged key-by-key, but **arrays are replaced** by the later scope. In practice that means if both global and project `.opencode.json` define `hooks.PreToolUse`, the project's array overrides the global's outright. Different event names from different scopes coexist (global `PreToolUse` + project `PostToolUse` both survive). If you want both global and project hooks on the same event, declare them as separate matcher groups inside one file — opencode runs all matching groups within a single file.

> Live reload not supported: opencode reads `.opencode.json` once at startup. Restart to pick up edits. This matches every other field in `.opencode.json`.

## Minimal example: RTK-style bash rewrite

`.opencode.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "bash",
        "hooks": [
          {
            "type": "command",
            "command": "/usr/local/bin/rtk",
            "args": ["hook"]
          }
        ]
      }
    ]
  }
}
```

> opencode's canonical tool names are **lowercase** — `bash`, `edit`, `write`, `read`, `grep`, `glob`, `ls`, `patch`, `delete`, `multiedit`, `view_image`, `webfetch`, `websearch`, `sourcegraph`, `lsp`, `skill`, `question`, `todowrite`, `struct_output`, `router_send`, `croncreate`, `crondelete`, `cronlist`. A Claude Code config that uses PascalCase (`Bash`, `Edit`) ALSO matches via the case-insensitive comparison — no manual translation needed for copy-paste. New configs should prefer lowercase to stay aligned with the rest of opencode (permission rules, agent configs all use lowercase tool names).

`rtk hook` reads the event JSON on stdin, computes a rewritten command, writes the updated input back as JSON, exits 0. Opencode runs the original tool with the rewritten command.

A worked example: a hook that prepends `rtk ` to every Bash invocation:

```bash
#!/bin/bash
# .opencode/hooks/rtk-prefix.sh
input=$(cat)
cmd=$(echo "$input" | jq -r '.tool_input.command')
jq -n --arg c "rtk $cmd" '{
  hookSpecificOutput: {
    hookEventName: "PreToolUse",
    updatedInput: { command: $c }
  }
}'
```

## Event JSON shape

### `PreToolUse` — stdin

```json
{
  "session_id": "abc123",
  "cwd": "/current/working/dir",
  "hook_event_name": "PreToolUse",
  "tool_name": "bash",
  "tool_input": { "command": "git status" }
}
```

> **`tool_name` is opencode's lowercase canonical form** (`bash`, `edit`, `write`, etc.) — NOT Claude Code's PascalCase. Hook scripts comparing `tool_name` should match against lowercase. The case-insensitive *matcher* in config (`"matcher": "Bash"`) is a separate convenience for copy-paste from Claude Code configs; it doesn't change what the hook subprocess sees on stdin.

> **`cwd` vs subprocess working directory:** the JSON `cwd` field reports the opencode process's `os.Getwd()` at hook-fire time. The subprocess itself runs with its working directory set to the **project root** (also exposed via `OPENCODE_PROJECT_DIR` / `CLAUDE_PROJECT_DIR`). These usually match, but can diverge if the agent process has `chdir`'d. When resolving a relative path, prefer `OPENCODE_PROJECT_DIR` over `.` — the latter is the subprocess cwd (project root), not the JSON `cwd`.

### `PreToolUse` — stdout (exit 0)

Every field is optional. Omit fields you don't want to change.

```json
{
  "hookSpecificOutput": {
    "hookEventName": "PreToolUse",
    "permissionDecision": "allow|deny|ask",
    "permissionDecisionReason": "human-readable text shown to the agent",
    "updatedInput": { "command": "rewritten command" },
    "additionalContext": "extra context to surface to the model"
  }
}
```

### `PostToolUse` — stdin

```json
{
  "session_id": "abc123",
  "cwd": "/current/working/dir",
  "hook_event_name": "PostToolUse",
  "tool_name": "bash",
  "tool_input": { "command": "cargo test" },
  "tool_output": "200 lines of test output…"
}
```

### `PostToolUse` — stdout (exit 0)

```json
{
  "hookSpecificOutput": {
    "hookEventName": "PostToolUse",
    "updatedToolOutput": "compacted summary",
    "additionalContext": "additional context for next turn"
  }
}
```

## Exit codes

| Exit | Meaning |
|---|---|
| `0` | Process stdout JSON. Empty stdout = no decision. Malformed JSON = log WARN, no decision applied. |
| `2` | Block. For `PreToolUse`: refuse the tool call and surface stderr to the agent. For `PostToolUse`: replace the tool's output with stderr. |
| Any other non-zero | Non-blocking error. Logged at WARN. Tool execution proceeds as if the hook were absent. |

## Matchers

Match strings against the **tool name** (e.g. `bash`, `edit`, `write`, `read`, `memory_create_entities` for an MCP `memory` server tool). opencode's tool names are lowercase by convention; the exact / list form comparison is **case-insensitive**, so `"Bash"` from a Claude Code config also matches the lowercase `bash` tool. Regex stays case-sensitive — add `(?i)` to opt into case-folding for regex.

> **MCP tool naming divergence from Claude Code:** opencode registers MCP tools as `<serverName>_<toolName>` with a **single** underscore separator (e.g. `memory_create_entities`). Claude Code uses `mcp__<server>__<tool>` with a double-underscore prefix. A Claude Code regex like `^mcp__.*` will NOT match any tool in opencode. Rewrite to key off the configured server names (e.g. `^(memory|filesystem)_`) or to exact-match a known MCP tool name.

| Matcher | Interpretation | Example tool match |
|---|---|---|
| `""`, `"*"`, or omitted | Match every tool | any |
| `"bash"` | Exact match, case-insensitive (preferred form) | `bash` |
| `"Bash"` | Same as above — case-insensitive lets Claude Code configs paste in unchanged | `bash` |
| `"edit\|write"` | Pipe-separated list, case-insensitive | `edit`, `write` |
| `"edit, write"` | Comma-separated list (whitespace trimmed), case-insensitive | `edit`, `write` |
| `"^memory_"` | Regex, case-sensitive (Go RE2) | `memory_create_entities` (MCP `memory` server tool) |
| `"(?i)^Bash$"` | Regex with explicit case-insensitive flag | `bash`, `Bash`, `BASH` |

**Limitations:**
- opencode uses Go's RE2. Lookahead `(?=...)` and lookbehind `(?<=...)` are not supported and produce a `CompileMatcher` error logged at WARN. Plugins targeting both Claude Code and opencode should stick to the common subset.
- The case-insensitivity is for the exact / list form only; regex always uses RE2's natural case-sensitivity unless the author adds an inline flag.

## Decision precedence (multiple hooks per event)

When several hooks match the same event and tool:

1. **Hooks run sequentially in declaration order**, in the order they appear in the merged config. Within a single matcher group, the `hooks` array runs in array order.
2. **`updatedInput` chains.** Each hook sees the previous hook's `updatedInput` (if any) as its `tool_input`. The final value is what reaches the tool.
3. **First deny wins.** If any hook returns `permissionDecision: "deny"` (or exits 2), the tool is blocked with the FIRST such hook's reason. Subsequent hooks still run for logging / context, but the block sticks and a later hook's deny reason does NOT overwrite the first.
4. **`allow` bypasses the permission gate.** If a hook returns `permissionDecision: "allow"` and no later hook denies, opencode's standard permission dialog is skipped for this call only.
5. **`additionalContext` accumulates.** All hooks' contexts are joined with `\n` and surfaced to the agent.

For PostToolUse specifically:

- **`updatedToolOutput` chains** the same way `updatedInput` does. Emit the field with an explicit empty string (`"updatedToolOutput": ""`) to fully suppress noisy output; omit the field entirely to leave the previous value untouched.
- **First exit-2 hook wins** for the block reason, same as PreToolUse's first-deny-wins. A later exit-2 hook does NOT overwrite the earlier reason.
- **Exit-2 BlockReason takes precedence over `updatedToolOutput`** set later in the chain — exit-2 is the dominant signal.

## Hook execution model

Each hook fires as a **fresh subprocess** — no daemon, no socket. Per-call cost is ~1-5ms of `os/exec` overhead. Plugins are language-neutral; the only contract is JSON over stdin/stdout.

| Property | Value |
|---|---|
| Stdin | Event JSON (UTF-8), closed after write |
| Stdout cap | 1 MiB (excess truncated, WARN logged) |
| Stderr cap | 64 KiB |
| Timeout | 600s default; override per-hook with `"timeout": <seconds>` |
| Kill chain | SIGTERM, then SIGKILL after 2s grace; whole process group signalled |
| Env | Inherits opencode's env, plus `OPENCODE_PROJECT_DIR` and `CLAUDE_PROJECT_DIR` (alias) |
| Working dir | Project root |
| TTY | None (subprocess can't interact with the user's terminal) |

## Shell vs args form

Two ways to invoke a hook command:

**Shell form** — string runs through `sh -c "<command>"`. Standard tokenization, variable expansion, pipes, redirects. Use when authoring quick inline hooks.

```json
{ "type": "command", "command": "/path/to/script.sh" }
```

**Args form** — `command` is the executable, `args` is `argv[1:]` verbatim. No shell. No expansion. Use for untrusted inputs or when a literal `$VAR` must reach the script.

```json
{ "type": "command", "command": "node", "args": ["/path/to/hook.js", "--strict"] }
```

## Security model

- Hooks run with the user's privileges and shell. Installing a hook is equivalent to installing a script — same trust requirement.
- There is **no sandbox**: no seccomp, no namespaces, no chroot. A future change may add capability scoping; v1 trusts the operator.
- The 600s default timeout + 2s SIGKILL grace bounds runaway processes. Size caps on stdout/stderr bound memory.
- Hook stderr on exit 2 is surfaced to the agent — design accordingly if you write sensitive data there.

## Troubleshooting

- **Hook doesn't fire.** Check the matcher (case-sensitive; tool names are `Bash`, `Edit`, etc.). Check that the file path is correct and `chmod +x` is set. Check opencode logs at INFO/WARN for hook-load messages. Confirm the `hooks` block is actually under a top-level `hooks` key in `.opencode.json`, not nested inside another key.
- **Hook output ignored.** Did you exit 0? Is stdout valid JSON? Common mistake: forgetting to consume stdin (`cat > /dev/null`) before writing — some shells exit before stdout flushes.
- **Hook times out.** Increase `timeout` (seconds). Slow LLM-callout hooks can need 60+ seconds.
- **Edits to `.opencode.json` don't take effect.** Restart opencode. Live reload of `.opencode.json` is not supported in v1.

## Battle-testing with RTK

[RTK](https://github.com/rtk-ai/rtk) is the canonical plugin we mirror Claude Code's contract for. RTK rewrites verbose shell commands (`git status`, `cargo test`, `ls`) into compact `rtk`-prefixed variants that consume far fewer tokens than the raw output. It ships as a single Rust binary plus a thin Bash hook script.

### Why it just works

RTK was authored against Claude Code's `PreToolUse` hook contract. Our `.opencode.json` `hooks` block speaks the same protocol — same JSON shape on stdin, same `hookSpecificOutput.updatedInput.command` on stdout, same `permissionDecision: "allow"` semantics. **You do not need a fork of RTK or a Go-native plugin.** The shell script RTK installs into Claude Code's `settings.json` works verbatim against opencode.

### Install RTK

Pick one:

```bash
# macOS — Homebrew (compiles from source; pulls swig/ninja/sqlite; ~10 min)
brew install rtk

# Linux / macOS — upstream prebuilt binary (~30 s)
curl -fsSL https://raw.githubusercontent.com/rtk-ai/rtk/refs/heads/master/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"

# Any platform — build from source (Rust toolchain required)
cargo install --git https://github.com/rtk-ai/rtk
```

Verify:

```bash
rtk --version    # 0.23.0 or later
rtk rewrite "git status"   # should print "rtk git status"
```

You also need `jq` on PATH — the hook script uses it to parse / build JSON.

### Locate the hook script

RTK ships its hook adapter as `hooks/claude/rtk-rewrite.sh`. Three options:

1. **Vendored copy** — `scripts/test/fixtures/rtk-rewrite.sh` (already in this repo for our e2e tests). Stable across RTK upgrades.
2. **From a Claude Code install** — if you've already run `rtk init -g` for Claude Code, the script lives at the path Claude Code uses (varies; check `~/.claude/settings.json`'s `hooks.PreToolUse[].hooks[].command`).
3. **Download fresh** — `gh api 'repos/rtk-ai/rtk/contents/hooks/claude/rtk-rewrite.sh?ref=develop' --jq '.content' | base64 -d > /usr/local/bin/rtk-rewrite.sh && chmod +x /usr/local/bin/rtk-rewrite.sh`.

### Wire it into `.opencode.json`

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "bash",
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/rtk-rewrite.sh"
          }
        ]
      }
    ]
  }
}
```

The matcher is lowercase (opencode-native) but a Claude Code config block using `"matcher": "Bash"` also works — the comparison is case-insensitive.

That's it. Restart opencode and the next time the agent invokes `bash` with `git status`, RTK will silently rewrite it to `rtk git status` before the tool runs.

### Interaction with serve mode auto-approve

`opencode serve --auto-approve` marks every session as auto-approved at `permission.Service` level (`cmd/serve.go::enableAutoApprove`). RTK's hook produces `updatedInput` either WITH `permissionDecision: "allow"` (auto-allow rules) or WITHOUT (the ask-rule default in current RTK 0.42.4 — see `rtk rewrite "git status"` exits 3).

Both flow shapes work the same under auto-approve:

| RTK hook output | `HookAllowKey` set? | `IsAutoApproveSession`? | `permissions.Request` returns |
|---|---|---|---|
| `permissionDecision: "allow"` + `updatedInput` | yes | yes | `true` (first check wins) |
| `permissionDecision: "allow"` + `updatedInput` | yes | no | `true` (hook allow alone) |
| `updatedInput` only (current RTK default) | no | yes | `true` (auto-approve picks up the slack) |
| `updatedInput` only | no | no | broker dispatch — host (TUI/HTTP/bridge) prompts |

The precedence sits in `internal/permission/permission.go::Request`: hook-allow → auto-approve → cached session-permission → publish-and-wait. Auto-approve is purely additive — it doesn't matter whether RTK or any future PreToolUse plugin produced an explicit-allow signal. Locked in by `TestHookAllowAndAutoApproveCompose`.

**Practical answer for serve mode + RTK:** RTK rewrites the command via `updatedInput`; serve's auto-approve handles the permission gate; the rewritten command runs without prompting. RTK's ask-rule intent (let the host prompt) is INTENTIONALLY bypassed by auto-approve — that's what `--auto-approve` means. If you want RTK's ask rules to actually prompt, run without `--auto-approve` and let the HTTP client (or TUI / chat bridge) consume the broker events.

### Verify against your install

Run the automated battle test:

```bash
./scripts/test/rtk.sh
```

The script auto-skips with a friendly message when `rtk` isn't on PATH (so `make test-e2e` stays green on CI machines without RTK), and runs six assertions when RTK is present:

1. `git status` is rewritten with the `rtk` prefix.
2. `ls` is rewritten with the `rtk` prefix.
3. RTK's `permissionDecision: "allow"` propagates into our `ExplicitAllow` flag (bypasses the standard permission prompt).
4. Unknown commands pass through unchanged.
5. `cargo test` is rewritten.
6. The matcher is correctly scoped to `bash` — the same hook does NOT fire on `edit` or other tools.

## Capability spec

The normative requirements live in [`openspec/specs/hook-runtime/spec.md`](../openspec/specs/hook-runtime/spec.md). When a hook's behavior in opencode diverges from the documentation here, the spec wins.
