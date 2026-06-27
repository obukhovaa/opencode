# Hook Runtime

## Purpose

Defines the in-process hook runtime that fires lifecycle events around agent tool calls. Hooks are user-configured POSIX subprocesses, declared inside `.opencode.json` under a top-level `hooks` key. The runtime is Claude-Code-compatible at the JSON layer (`hooks` block, event names, matcher syntax, decision protocol) so users can copy-paste hooks blocks between `~/.claude/settings.json` and `.opencode.json`. The runtime covers two events in this slice — `PreToolUse` (can block, mutate input, or short-circuit the permission system) and `PostToolUse` (can mutate or suppress tool output) — with bounded subprocess execution and full error isolation so misbehaving hooks cannot crash the agent loop.

## Requirements

### Requirement: Hooks block lives inside `.opencode.json` and follows existing config merge rules

The runtime SHALL read its hook configuration from a top-level `hooks` key inside the existing `.opencode.json` file. No separate settings file is introduced. The same two-scope discovery the rest of `.opencode.json` already uses applies to the `hooks` block:

1. **Global scope** — one of `$HOME/.opencode.json`, `$XDG_CONFIG_HOME/opencode/.opencode.json`, or `$HOME/.config/opencode/.opencode.json` (viper-defined search order; first match wins).
2. **Project scope** — `<workingDir>/.opencode.json`.

Merging across scopes follows viper's existing deep-merge semantics that the rest of `.opencode.json` already obeys: maps are deep-merged key-by-key, but leaf values and arrays are REPLACED by the later scope. In practice, if both global and project `.opencode.json` declare the same `hooks.<EventName>` array, the project's array wins outright (it is not concatenated with the global one). Different event names from different scopes coexist (global `PreToolUse` plus project `PostToolUse` both survive because they live under different map keys).

The `hooks` block itself SHALL conform to the Claude-Code-compatible JSON shape:

```json
{
  "hooks": {
    "<EventName>": [
      {
        "matcher": "<string|optional>",
        "hooks": [
          {
            "type": "command",
            "command": "<absolute or PATH-resolved executable>",
            "args": ["<optional argv>"],
            "timeout": <seconds, optional>,
            "shell": "<sh|bash, optional>"
          }
        ]
      }
    ]
  }
}
```

The JSON shape SHALL match Claude Code's `settings.json` `hooks` block byte-for-byte for the events and hook types implemented in this capability, so a user can copy-paste the `hooks` block from a Claude Code config (including RTK's `rtk init` output) into `.opencode.json` and have it work without modification.

If no `.opencode.json` contains a `hooks` block, the runtime SHALL load zero hooks and the agent loop SHALL run as if the hooks system were absent.

Hooks SHALL be loaded once at process startup, alongside the rest of `.opencode.json`. Edits to `.opencode.json` made during a session SHALL NOT take effect until opencode is restarted, matching the existing behavior of every other field in `.opencode.json`. This is a deliberate divergence from Claude Code's "edits picked up automatically" promise, taken to avoid running a parallel reload mechanism alongside the existing viper-based config loader.

#### Scenario: Global `.opencode.json` hook fires for every project

- **GIVEN** `~/.opencode.json` contains a `PreToolUse` hook with matcher `bash`
- **AND** the project has no `.opencode.json` in the working directory
- **WHEN** the agent in any project invokes the `bash` tool
- **THEN** the global-scope hook MUST fire

#### Scenario: Project `.opencode.json` replaces global hook for the same event

- **GIVEN** `~/.opencode.json` declares `hooks.PreToolUse` containing hook `A`
- **AND** `<workingDir>/.opencode.json` declares `hooks.PreToolUse` containing hook `B`
- **WHEN** the agent fires `PreToolUse`
- **THEN** ONLY hook `B` MUST run — viper replaces the global array with the project array; concatenation does NOT happen

#### Scenario: Different event names from different scopes coexist

- **GIVEN** `~/.opencode.json` declares `hooks.PreToolUse`
- **AND** `<workingDir>/.opencode.json` declares `hooks.PostToolUse` (different event)
- **WHEN** the agent fires both events
- **THEN** the global `PreToolUse` hooks MUST fire on PreToolUse
- **AND** the project `PostToolUse` hooks MUST fire on PostToolUse

#### Scenario: `.opencode.json` with no hooks block leaves agent unchanged

- **GIVEN** neither global nor project `.opencode.json` contains a `hooks` key
- **WHEN** the agent invokes any tool
- **THEN** no subprocess MUST be spawned and the agent loop MUST behave identically to a build that lacks the hook runtime

#### Scenario: Edits to `.opencode.json` require restart to take effect

- **GIVEN** the agent has already fired one `PreToolUse` event with no hooks configured
- **WHEN** the user edits `.opencode.json` to add a `PreToolUse` hook with matcher `bash`
- **AND** the agent fires its next `bash` tool call WITHIN THE SAME OPENCODE PROCESS
- **THEN** the newly added hook MUST NOT run
- **AND** restarting opencode and re-running the tool call MUST cause the new hook to fire

### Requirement: PreToolUse fires before tool execution and can block / mutate input

The runtime SHALL emit a `PreToolUse` event immediately before the agent invokes a tool's `Run` method. The event SHALL be emitted ONLY when at least one configured `PreToolUse` matcher group matches the tool's name.

The runtime SHALL pass to each matching hook a JSON object on stdin containing AT LEAST:

```json
{
  "session_id": "<current session ID>",
  "cwd": "<current working directory>",
  "hook_event_name": "PreToolUse",
  "tool_name": "<the BaseTool's Info().Name>",
  "tool_input": <the tool call's input map, as-is>
}
```

On exit code 0, the runtime SHALL parse the hook's stdout. If stdout is valid JSON, the runtime SHALL apply the decision recorded in `hookSpecificOutput`:

- `permissionDecision: "deny"` — the tool MUST NOT execute. The runtime SHALL synthesize a tool result containing `permissionDecisionReason` and surface it to the agent as the tool call's response.
- `permissionDecision: "allow"` — the tool MUST execute. The opencode permission system SHALL NOT be consulted further (the hook overrides it).
- `permissionDecision: "ask"` or missing — the tool MUST proceed through the existing permission flow.
- `updatedInput: <object>` — the tool's `Input` map MUST be replaced wholesale with this object before execution.
- `additionalContext: <string>` — this string MUST be appended to the conversation context visible to the agent on its next turn.

On exit code 2, the runtime SHALL treat the call as blocked: the tool MUST NOT execute, the hook's stderr text MUST be surfaced to the agent as the tool's response, and the agent SHALL proceed to its next turn.

On any other non-zero exit code, the runtime SHALL log the hook's stderr at WARN level and proceed AS IF the hook had not been configured (non-blocking error). The tool execution MUST continue normally.

#### Scenario: RTK rewrites `git status` to `rtk git status` before bash runs

- **GIVEN** a hook configured as `{"matcher": "bash", "hooks": [{"type": "command", "command": "/usr/local/bin/rtk", "args": ["hook"]}]}` under `PreToolUse`
- **AND** the hook reads `tool_input.command`, computes the rewritten command, and writes `{"hookSpecificOutput": {"hookEventName": "PreToolUse", "updatedInput": {"command": "rtk git status"}}}` to stdout with exit 0
- **WHEN** the agent invokes `bash` with `{"command": "git status"}`
- **THEN** the actual bash tool execution MUST receive `{"command": "rtk git status"}` and the agent's tool result MUST be the output of running `rtk git status`

#### Scenario: Hook denies a destructive bash invocation

- **GIVEN** a `PreToolUse` hook configured on `bash` that writes `{"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": "rm -rf is forbidden"}}` and exits 0 when it detects `rm -rf`
- **WHEN** the agent invokes `bash` with `{"command": "rm -rf /"}`
- **THEN** the `bash` tool's `Run` method MUST NOT be invoked
- **AND** the agent's tool result MUST contain the text `"rm -rf is forbidden"`

#### Scenario: Hook exit-2 blocks with stderr feedback

- **GIVEN** a `PreToolUse` hook that writes `"Command is unsafe"` to stderr and exits 2
- **WHEN** the hook fires
- **THEN** the tool MUST NOT execute
- **AND** the agent's tool result MUST contain the stderr text `"Command is unsafe"`

#### Scenario: Hook non-zero non-two exit is non-blocking

- **GIVEN** a `PreToolUse` hook that exits 1 with stderr `"transient error"`
- **WHEN** the hook fires
- **THEN** the runtime MUST log a WARN containing `"transient error"`
- **AND** the tool MUST execute with its original, unmodified input

#### Scenario: PreToolUse fires for the matched tool only

- **GIVEN** a `PreToolUse` matcher group with matcher `bash`
- **WHEN** the agent invokes the `edit` tool
- **THEN** the hook MUST NOT fire

### Requirement: PostToolUse fires after tool execution and can mutate output

The runtime SHALL emit a `PostToolUse` event immediately after a tool's `Run` method returns successfully (no error). The event SHALL be emitted ONLY when at least one configured `PostToolUse` matcher group matches the tool's name.

The JSON sent to each matching hook on stdin SHALL contain AT LEAST:

```json
{
  "session_id": "<current session ID>",
  "cwd": "<current working directory>",
  "hook_event_name": "PostToolUse",
  "tool_name": "<the BaseTool's Info().Name>",
  "tool_input": <the input map the tool ran with, after any PreToolUse updatedInput>,
  "tool_output": "<the tool's result content as a string>"
}
```

On exit 0 + valid JSON stdout, the runtime SHALL apply:

- `updatedToolOutput: <string>` — when the field is PRESENT (including the explicit empty string `""`), the tool's result content MUST be replaced with this string before being added to the conversation. When the field is OMITTED entirely the original tool output MUST pass through unchanged. Empty-string MUST be distinguishable from absent (the Go runtime uses `*string` for this purpose) so a hook can fully suppress a noisy result by emitting `{"updatedToolOutput": ""}`.
- `additionalContext: <string>` — appended to the conversation context for the next turn.

Exit code semantics SHALL be identical to `PreToolUse`: exit 0 (process JSON), exit 2 (block / surface stderr — for `PostToolUse` "block" means the tool's result MUST be replaced by the stderr text), any other non-zero is non-blocking (log WARN, keep the original tool output).

When MULTIPLE PostToolUse hooks match the same tool call:

- `updatedToolOutput` SHALL chain: each hook receives the previous hook's replacement as its own `tool_output`.
- The FIRST hook to exit 2 sets `BlockReason`; later exit-2 hooks SHALL NOT overwrite it (mirrors PreToolUse's first-deny-wins).
- `BlockReason` SHALL take precedence over any `UpdatedOutput` set later in the chain — exit-2 is the dominant signal.
- `additionalContext` SHALL accumulate across all hooks in declaration order (`\n`-joined).

If a tool's `Run` returned an error, `PostToolUse` MUST NOT fire (the result was not "successful"). A future `PostToolUseFailure` event MAY cover that case but is out of scope here.

#### Scenario: RTK redacts noisy cargo test output

- **GIVEN** a `PostToolUse` hook on `bash` that pipes `tool_output` through RTK's truncation and writes `{"hookSpecificOutput": {"hookEventName": "PostToolUse", "updatedToolOutput": "<~20 lines of summary>"}}` to stdout
- **AND** the agent has just run `cargo test` and the raw output is 200+ lines
- **WHEN** `PostToolUse` fires
- **THEN** the agent's conversation history MUST receive the ~20-line summary, NOT the 200-line raw output

#### Scenario: PostToolUse does not fire on tool error

- **GIVEN** a tool's `Run` returns a non-nil error
- **WHEN** the agent processes the result
- **THEN** the runtime MUST NOT fire `PostToolUse` for that tool call

### Requirement: Matcher syntax matches Claude Code with case-insensitive exact/list comparison

For tool-name-matching events (`PreToolUse`, `PostToolUse`), the matcher field SHALL be evaluated as:

- Empty string, missing, or literal `*` — matches every tool
- Composed only of `[A-Za-z0-9_, |]` characters — interpreted as either a single exact name (`bash`), a pipe-separated list (`edit|write`), or a comma-separated list (`edit, write`). Whitespace around list items SHALL be trimmed. Comparison against the tool name SHALL be **case-insensitive** — both sides are lowercased before equality check. Rationale: opencode's tool names are lowercase by convention (`bash`, `edit`, `write`, `read`, `grep`, etc.), but Claude Code documents matchers in PascalCase (`Bash`, `Edit`, `Write`). Case-insensitive comparison aligns the two so a Claude Code `settings.json` `hooks` block pastes into `.opencode.json` and matches without any manual translation.
- Any other character — interpreted as a Go regular expression (RE2). Anchoring with `^` / `$` is the author's responsibility. Regex comparison SHALL be case-sensitive by default; authors who need case-insensitive regex (e.g. a Claude Code config using `^Bash$`) SHALL add the standard RE2 inline flag `(?i)` to opt in. Case-folding the regex matcher automatically would change RE2's semantics in ways the spec cannot opaquely promise; the inline flag is the documented escape hatch.

opencode's canonical tool names are lowercase. Documentation and examples SHALL show the lowercase form. The case-insensitive exact/list comparison exists for Claude-Code-compat copy-paste, not as the recommended authoring style.

A matcher group SHALL run all of its inner `hooks` entries sequentially. If multiple matcher groups in the same event match, they SHALL all fire in declaration order. Within a single matcher group, `hooks` entries SHALL run in array order.

#### Scenario: Lowercase exact name matches the lowercase tool

- **GIVEN** matcher `bash`
- **WHEN** the agent invokes the `bash` tool
- **THEN** the hook MUST fire

#### Scenario: PascalCase matcher matches the lowercase tool (Claude Code compat)

- **GIVEN** matcher `Bash` (as authored in a Claude Code config)
- **WHEN** the agent invokes the `bash` tool
- **THEN** the hook MUST fire — case-insensitive comparison aligns Claude Code's PascalCase with opencode's lowercase

#### Scenario: Pipe list matches any listed tool

- **GIVEN** matcher `edit|write`
- **WHEN** the agent invokes the `write` tool
- **THEN** the hook MUST fire

#### Scenario: Pipe list authored in PascalCase still matches lowercase tools

- **GIVEN** matcher `Edit|Write` (as authored in a Claude Code config)
- **WHEN** the agent invokes the `write` tool
- **THEN** the hook MUST fire — case-insensitive comparison bridges the naming convention difference

#### Scenario: Regex matcher matches MCP tools (opencode form)

- **GIVEN** matcher `^memory_` (opencode MCP tools register as `<server>_<tool>` with a single underscore, NOT Claude Code's `mcp__<server>__<tool>` double-underscore form)
- **WHEN** the agent invokes a tool named `memory_create_entities`
- **THEN** the hook MUST fire
- **AND** a Claude-Code-authored regex of the form `^mcp__.*` would NOT match opencode MCP tools and must be rewritten to key off the configured server names

#### Scenario: Regex is case-sensitive without explicit flag

- **GIVEN** matcher `^Bash$` (regex form, no flag)
- **WHEN** the agent invokes the `bash` tool
- **THEN** the hook MUST NOT fire — RE2 default is case-sensitive
- **AND** changing the matcher to `(?i)^Bash$` MUST make the same tool match

#### Scenario: Wildcard matcher matches every tool

- **GIVEN** matcher `*` (or empty string, or omitted)
- **WHEN** the agent invokes any tool
- **THEN** the hook MUST fire

### Requirement: Hooks execute as POSIX subprocesses with bounded timeout

Each `type: "command"` hook SHALL be executed as a POSIX subprocess via the Go standard library (`os/exec`). The runtime SHALL:

- Spawn `sh -c "<command>"` when `args` is omitted, so the author's `command` string undergoes normal shell tokenization, variable expansion, and pipe handling. The shell binary SHALL be `bash` if available on `PATH`, falling back to `sh`. The `shell` field MAY override this.
- Spawn `<command>` directly with `args` as `argv[1:]` when `args` is present (no shell). This SHALL bypass shell tokenization so author-controlled arguments are passed exactly as written. Operators using untrusted inputs SHOULD prefer the `args` form.
- Inherit the agent's environment, plus set `OPENCODE_PROJECT_DIR=<project root>` and `CLAUDE_PROJECT_DIR=<project root>` (alias for Claude-Code-targeted plugins).
- Apply a timeout. Default 600 seconds. The hook config MAY override per-hook with `timeout: <seconds>` (a positive integer). On timeout, the subprocess MUST be sent SIGTERM, then SIGKILL after 2 additional seconds if it has not exited. Timeout SHALL be treated as a non-blocking error (logged WARN, tool execution continues).
- Pass the event JSON on stdin and close stdin immediately after writing. The hook MUST NOT block waiting for additional stdin.
- Read stdout and stderr to completion. Stdout SHALL be capped at 1 MiB (excess content truncated with a WARN log). Stderr SHALL be capped at 64 KiB.
- Run with the project root as the subprocess's working directory.

The runtime SHALL NOT allocate a controlling terminal for the subprocess. Hooks SHALL NOT be able to interact with the user's TTY.

#### Scenario: Subprocess sees OPENCODE_PROJECT_DIR

- **GIVEN** the agent is running in a session rooted at `/home/me/proj`
- **AND** a `PreToolUse` hook with command `env | grep OPENCODE_PROJECT_DIR > /tmp/probe`
- **WHEN** the hook fires
- **THEN** `/tmp/probe` MUST contain `OPENCODE_PROJECT_DIR=/home/me/proj`

#### Scenario: Slow hook is killed at the timeout

- **GIVEN** a hook configured with `timeout: 2` whose command is `sleep 30`
- **WHEN** the hook fires
- **THEN** the subprocess MUST be terminated within 5 seconds total (timeout + 2s SIGKILL grace + slack)
- **AND** the tool MUST proceed with its original input (timeout is non-blocking)

#### Scenario: args form bypasses shell tokenization

- **GIVEN** a hook configured as `{"command": "/usr/bin/echo", "args": ["$HOME"]}`
- **WHEN** the hook fires
- **THEN** the subprocess receives the literal string `$HOME` as `argv[1]` (NOT the expanded value of the environment variable)

### Requirement: Hook errors do not crash the agent loop

A panic, timeout, JSON-parse failure, command-not-found, or non-zero exit (other than the documented decision exit codes) from a hook SHALL NOT propagate to the agent's main loop. Every hook invocation SHALL be wrapped in a recover/error boundary such that the agent continues processing the tool call (using the original input/output when the hook fails non-blockingly, or the documented block behavior when exit 2).

The runtime SHALL log each hook outcome at the following levels:

- Success (exit 0, decision applied): `DEBUG` with event name, tool name, hook command, duration.
- Block (exit 2): `INFO` with event name, tool name, hook command, stderr text.
- Non-blocking error (other non-zero, timeout, JSON-parse failure, spawn failure): `WARN` with event name, tool name, hook command, error/stderr.
- Unexpected panic inside the runtime itself: `ERROR` with stack trace; the hook's effect SHALL be treated as non-blocking.

#### Scenario: Malformed JSON from hook is non-blocking

- **GIVEN** a `PreToolUse` hook that writes `not-valid-json` to stdout and exits 0
- **WHEN** the hook fires
- **THEN** the runtime MUST log a WARN naming the JSON-parse failure
- **AND** the tool MUST execute with its original input

#### Scenario: Command-not-found is non-blocking

- **GIVEN** a hook with command `/does/not/exist`
- **WHEN** the hook fires
- **THEN** the runtime MUST log a WARN containing the spawn error
- **AND** the tool MUST execute normally

#### Scenario: Multiple hooks: a failing one does not abort the others

- **GIVEN** two `PreToolUse` hooks matching `bash`: hook A exits 1 with stderr, hook B exits 0 with a valid `updatedInput`
- **WHEN** the event fires
- **THEN** hook A's failure MUST be logged as WARN
- **AND** hook B MUST still fire and its `updatedInput` MUST be applied
