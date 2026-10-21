# Spec Delta

## MODIFIED Requirements

### Requirement: Hooks execute as POSIX subprocesses with bounded timeout

Each `type: "command"` hook SHALL be executed as a POSIX subprocess via the Go standard library (`os/exec`). The runtime SHALL:

- Spawn `sh -c "<command>"` when `args` is omitted, so the author's `command` string undergoes normal shell tokenization, variable expansion, and pipe handling. The shell binary SHALL be `bash` if available on `PATH`, falling back to `sh`. The `shell` field MAY override this.
- Spawn `<command>` directly with `args` as `argv[1:]` when `args` is present (no shell). This SHALL bypass shell tokenization so author-controlled arguments are passed exactly as written. Operators using untrusted inputs SHOULD prefer the `args` form.
- Inherit the agent's environment filtered by the tool environment deny list (see the `tool-process-environment` capability), plus set `OPENCODE_PROJECT_DIR=<project root>` and `CLAUDE_PROJECT_DIR=<project root>` (alias for Claude-Code-targeted plugins).
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

#### Scenario: Hook does not see a denied variable

- **GIVEN** opencode runs with `OPENCODE_SERVER_PASSWORD` set
- **AND** a `PreToolUse` hook with command `env > /tmp/probe`
- **WHEN** the hook fires
- **THEN** `/tmp/probe` MUST NOT contain `OPENCODE_SERVER_PASSWORD`
